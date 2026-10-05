package scenario

import (
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// writer writes the steps into the document.
type writer struct {
	in     Input
	res    *Result
	store  *Store
	v      *spec.Validator
	params map[*yaml.Node]written // parameter object → its example
	media  map[*yaml.Node]written // shared media type → its example
}

type written struct {
	value any
	by    string
}

func newWriter(in Input, res *Result, store *Store) *writer {
	return &writer{in: in, res: res, store: store, v: spec.NewValidator(),
		params: map[*yaml.Node]written{}, media: map[*yaml.Node]written{}}
}

// fetched writes the answers of followingDetails into operations of no
// resource.
func (w *writer) fetched() {
	for _, id := range sortedKeys(w.store.examples) {
		op := w.in.Spec.Op(id)
		if op == nil || w.in.Model.Op(op) != nil {
			continue
		}
		for _, pl := range responsePlaces(w.in.Doc, op) {
			if pl.success && !pl.named {
				w.media1(pl, w.store.examples[id], spec.ModeResponse, id)
				break
			}
		}
		for _, name := range sortedKeys(w.store.params[id]) {
			w.param(op, name, w.store.params[id][name])
		}
	}
}

func (w *writer) write(steps []*step) {
	for _, s := range steps {
		w.parameters(s)
		if s.c.Example != cases.DefaultExample {
			continue // named examples are curated
		}
		if s.body != nil {
			if pl := requestPlace(w.in.Doc, s.c.Op); pl != nil && !pl.named {
				w.media1(pl, s.body, spec.ModeRequest, s.c.Name)
			}
		}
		for _, pl := range responsePlaces(w.in.Doc, s.c.Op) {
			if !pl.success || pl.named {
				continue
			}
			if v := w.response(s, pl); v != nil {
				w.media1(pl, v, spec.ModeResponse, s.c.Name)
			}
		}
	}
}

// parameters writes the keys of the records into the path parameters.
func (w *writer) parameters(s *step) {
	for _, mp := range s.o.Params {
		var rec Record
		switch {
		case mp.Resource == s.o.Resource && s.before != nil:
			rec = s.before
		case mp.Resource == s.o.Resource:
			rec = s.after
		default:
			rec = s.others[mp.Resource]
		}
		if rec == nil {
			if recs := w.store.Records(mp.Resource.Name); len(recs) > 0 {
				rec = recs[0]
			}
		}
		if v, ok := rec[mp.Field]; ok && v != nil {
			w.param(s.c.Op, mp.Name, v)
		}
	}
}

func (w *writer) param(op *spec.Operation, name string, v any) {
	e := pathParam(w.in.Doc, op, name)
	p := specParam(op, name)
	if e == nil || p == nil || p.Schema == nil {
		return
	}
	v = coerce(v, p.Schema.Value)
	if errs := w.v.Validate(p.Schema.Value, v, spec.ModePlain); len(errs) > 0 {
		if !w.res.lint(w.in.IgnoreLinting, CodeInvalid, e.where, "the record value %s violates the schema of {%s}: %s", text(v), name, errs[0].Reason) {
			return
		}
	}
	if prev, ok := w.params[e.target]; ok && !equalJSON(prev.value, v) {
		if !e.inline() {
			w.res.problem(CodeShared, e.where, "{%s} is declared once for several operations of this path, %s needs %s and %s needs %s; declare the parameter in each operation",
				name, prev.by, text(prev.value), op.ID, text(v))
			return
		}
		w.res.Stats.Inlined++
		w.res.note(CodeInlined, e.where, "the shared parameter object holds %s for %s; it is copied into %s, so this path can have its own example %s",
			text(prev.value), prev.by, op.Path, text(v))
	}
	if prev, ok := w.params[e.target]; ok && equalJSON(prev.value, v) {
		return
	}
	w.params[e.target] = written{v, op.ID}
	var cur any
	if ex := yamldoc.Get(e.target, "example"); ex != nil {
		cur, _ = yamldoc.Decode(ex)
	}
	if equalJSON(cur, v) {
		return
	}
	if err := yamldoc.Set(e.target, "example", v); err == nil {
		w.res.Changed = true
		w.res.Stats.Examples++
	}
}

// response is the example of a 2xx response at the position of the step,
// or nil to leave it.
func (w *writer) response(s *step, pl *place) any {
	r := s.o.Resource
	base := pl.example()
	rec := s.after
	if rec == nil {
		rec = s.before
	}
	if s.o.Role == model.RoleList {
		return w.list(s, pl, base)
	}
	if rec == nil {
		return nil
	}
	if dto := dict.DTORef(pl.schema); dto != "" && containsFold(r.Schemas, dto) {
		return forget(project(pl.schema, base, rec, r, spec.ModeResponse), pl.schema, r, s.unknown)
	}
	// another DTO, e.g. {Id, Message}: only the keys of the record
	obj, ok := base.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for k, v := range obj {
		out[k] = v
	}
	props, _ := dict.Properties(pl.schema.Value)
	for k, p := range props {
		f := r.Field(k)
		if f == "" || !containsFold(r.Keys, f) || p.Value == nil || rec[f] == nil {
			continue
		}
		out[k] = coerce(rec[f], p.Value)
	}
	return out
}

// list is the example of a list: the fetched elements, each record in its
// state at this position; without a snapshot the records themselves.
func (w *writer) list(s *step, pl *place, base any) any {
	r := s.o.Resource
	ref := listSchema(pl.schema, s.o.Items)
	if ref == nil {
		return nil
	}
	baseItems, _ := listItems(base, s.o.Items)
	var items []any
	if raw, ok := w.store.lists[s.c.Op.ID]; ok {
		for _, it := range raw {
			unknown := s.unknown
			if rec := matching(r, s.list, it); rec != nil {
				it = project(ref, it, rec, r, spec.ModeResponse)
				for i, x := range s.list {
					if equalJSON(map[string]any(x), map[string]any(rec)) && i < len(s.listUnknown) {
						unknown = s.listUnknown[i]
						break
					}
				}
			}
			items = append(items, forget(it, ref, r, unknown))
		}
	} else {
		for i, rec := range s.list {
			var b any
			switch {
			case i < len(baseItems):
				b = baseItems[i]
			case len(baseItems) > 0:
				b = baseItems[0]
			}
			unknown := s.unknown
			if i < len(s.listUnknown) {
				unknown = s.listUnknown[i]
			}
			items = append(items, forget(project(ref, b, rec, r, spec.ModeResponse), ref, r, unknown))
		}
	}
	if items == nil {
		items = []any{}
	}
	if s.o.Items == "" {
		return items
	}
	out := map[string]any{}
	if obj, ok := base.(map[string]any); ok {
		for k, v := range obj {
			out[k] = v
		}
	}
	out[strings.TrimPrefix(s.o.Items, "/")] = items
	return out
}

// forget leaves the unknown fields out of an example of the resource; a
// required field stays, the schema needs it.
func forget(v any, ref *openapi3.SchemaRef, r *model.Resource, unknown []string) any {
	obj, ok := v.(map[string]any)
	if !ok || len(unknown) == 0 || ref == nil || ref.Value == nil {
		return v
	}
	_, required := dict.Properties(ref.Value)
	out := map[string]any{}
	for k, x := range obj {
		if f := r.Field(k); f != "" && containsFold(unknown, f) && !containsFold(required, k) {
			continue
		}
		out[k] = x
	}
	return out
}

// listSchema is the schema of the elements of a list response.
func listSchema(ref *openapi3.SchemaRef, items string) *openapi3.SchemaRef {
	if ref == nil || ref.Value == nil {
		return nil
	}
	if items != "" {
		props, _ := dict.Properties(ref.Value)
		ref = props[strings.TrimPrefix(items, "/")]
		if ref == nil || ref.Value == nil {
			return nil
		}
	}
	return ref.Value.Items
}

// matching finds the record whose keys an element has.
func matching(r *model.Resource, recs []Record, item any) Record {
	obj, ok := item.(map[string]any)
	if !ok || len(r.Keys) == 0 {
		return nil
	}
	for _, rec := range recs {
		same := true
		for _, k := range r.Keys {
			v := fieldOf(obj, k)
			if v == nil || !equalJSON(v, rec[k]) {
				same = false
				break
			}
		}
		if same {
			return rec
		}
	}
	return nil
}

// media1 writes the example of one media type; a shared one must get the
// same example from every operation.
func (w *writer) media1(pl *place, v any, mode spec.Mode, by string) {
	if errs := w.v.Validate(pl.schema.Value, spec.Normalize(v), mode); len(errs) > 0 {
		if !w.res.lint(w.in.IgnoreLinting, CodeInvalid, pl.where, "the example from the records violates the schema (%s: %s)", errs[0].Pointer, errs[0].Reason) {
			return
		}
	}
	if pl.shared {
		if prev, ok := w.media[pl.node]; ok && !equalJSON(prev.value, v) {
			w.res.problem(CodeShared, pl.where, "the response is shared by %s and %s, which need different examples; give one of them its own response",
				prev.by, by)
			return
		}
		w.media[pl.node] = written{v, by}
	}
	if equalJSON(pl.example(), v) {
		return
	}
	if err := yamldoc.Set(pl.node, "example", v); err == nil {
		w.res.Changed = true
		w.res.Stats.Examples++
	}
}

// messages sets the message field of every response example: "Successfully
// updated Book" for 2xx, "Error while updating Book" for the others. A
// shared response that serves different operations says "the request".
func (w *writer) messages() {
	type want struct {
		pl    *place
		field string
		texts map[string]bool
	}
	var order []*yaml.Node
	wants := map[*yaml.Node]*want{}
	for _, op := range w.in.Spec.Ops {
		for _, pl := range responsePlaces(w.in.Doc, op) {
			if pl.named {
				continue
			}
			obj, ok := pl.example().(map[string]any)
			if !ok {
				continue
			}
			field := messageField(pl.schema)
			if field == "" || obj[field] == nil {
				continue
			}
			if w.in.Defaults.Field(dict.DTORef(pl.schema), op.ID, field) != nil {
				continue
			}
			x := wants[pl.node]
			if x == nil {
				x = &want{pl: pl, field: field, texts: map[string]bool{}}
				wants[pl.node] = x
				order = append(order, pl.node)
			}
			x.texts[w.message(op, pl.success)] = true
		}
	}
	for _, n := range order {
		x := wants[n]
		msg := ""
		for t := range x.texts {
			msg = t
		}
		if len(x.texts) > 1 {
			msg = "Error while processing the request"
			if x.pl.success {
				msg = "Successfully processed the request"
			}
		}
		obj, _ := x.pl.example().(map[string]any)
		if obj[x.field] == msg {
			continue
		}
		out := map[string]any{}
		for k, v := range obj {
			out[k] = v
		}
		out[x.field] = msg
		if errs := w.v.Validate(x.pl.schema.Value, spec.Normalize(out), spec.ModeResponse); len(errs) > 0 {
			continue // e.g. a maxLength the text does not fit
		}
		if err := yamldoc.Set(x.pl.node, "example", out); err == nil {
			w.res.Changed = true
			w.res.Stats.Examples++
		}
	}
}

// message is the text of a message field for op.
func (w *writer) message(op *spec.Operation, success bool) string {
	name := ""
	if o := w.in.Model.Op(op); o != nil {
		name = o.Resource.Name
	} else if g := op.Group(); g != spec.Untagged {
		name = g
	}
	past, ing := "processed", "processing"
	switch op.Method {
	case http.MethodGet:
		past, ing = "retrieved", "retrieving"
	case http.MethodPost:
		past, ing = "created", "creating"
	case http.MethodPut, http.MethodPatch:
		past, ing = "updated", "updating"
	case http.MethodDelete:
		past, ing = "deleted", "deleting"
	}
	if name == "" {
		name, past, ing = "the request", "processed", "processing"
	}
	if success {
		return "Successfully " + past + " " + name
	}
	return "Error while " + ing + " " + name
}

// messageField is the top-level string property named "message" of a
// schema, in any case.
func messageField(ref *openapi3.SchemaRef) string {
	if ref == nil || ref.Value == nil {
		return ""
	}
	props, _ := dict.Properties(ref.Value)
	for _, k := range sortedKeys(props) {
		if strings.EqualFold(k, "message") && props[k].Value != nil {
			if t, ok := dict.Primitive(props[k].Value); ok && t == "string" {
				return k
			}
		}
	}
	return ""
}
