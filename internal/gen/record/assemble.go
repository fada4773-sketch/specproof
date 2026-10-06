package record

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// maxAssembleDepth stops the recursion in cyclic schemas.
const maxAssembleDepth = 12

// indexed is an object a GET answered, found below the schema of a DTO.
type indexed struct {
	v   map[string]any
	src string // "#12 GET /Dock/D2"
	// selected marks an object inside a selected record: it is taken first
	selected bool
}

// assembler builds a body or an answer for a schema that no GET answers as
// a whole (an allOf of several DTOs, $refs, properties of its own): part
// by part from the data the run read. A part with the DTO of a table takes
// the selected record of that table, so its ids agree with the paths; a
// DTO without a table takes an object an answer holds; a required field
// without data gets a generated value.
type assembler struct {
	w    *writes
	mode spec.Mode
	// create fills the optional parts of a POST that creates a record too;
	// any other write keeps what its record has and fills only what the
	// schema requires
	create bool
	seed   string // the operation, seeds generated values
	// from tells where each part not taken from the data itself comes
	// from: JSON pointer → origin
	from  map[string]string
	order []string
}

func (w *writes) assembler(op *spec.Operation, mode spec.Mode) *assembler {
	create := op.Method == http.MethodPost
	if x := w.byOp[op.ID]; x != nil {
		create = x.kind == kindCreate || x.kind == kindAction
	}
	return &assembler{w: w, mode: mode, create: create, seed: op.ID, from: map[string]string{}}
}

// build is the value for the schema ref from src (the data of a record, a
// GET answer); what src lacks comes from the other data of the run. A
// value src holds that violates the schema stays: the data of the
// instance is not hidden.
func (a *assembler) build(ref *openapi3.SchemaRef, src any) any {
	if ref == nil || ref.Value == nil {
		return src
	}
	v, _ := a.fill(ref, src, "", true, 0)
	return a.repair(ref, v)
}

// origins are the parts that did not come from the data itself, one line
// each, for the log.
func (a *assembler) origins(what string) []string {
	var out []string
	for _, p := range a.order {
		out = append(out, fmt.Sprintf("%s%s ← %s", what, strings.ReplaceAll(p, "/", "."), a.from[p]))
	}
	return out
}

func (a *assembler) note(path, src string) {
	if _, ok := a.from[path]; !ok {
		a.order = append(a.order, path)
	}
	a.from[path] = src
}

// fill takes src for the schema, part by part; ok is false if there is no
// value.
func (a *assembler) fill(ref *openapi3.SchemaRef, src any, path string, required bool, depth int) (any, bool) {
	if ref == nil || ref.Value == nil || depth > maxAssembleDepth {
		return src, src != nil
	}
	s := ref.Value
	props, req := dict.Properties(s)
	if branches := append(append([]*openapi3.SchemaRef{}, s.OneOf...), s.AnyOf...); len(branches) > 0 && len(props) == 0 {
		b := a.branch(branches, src)
		// the discriminator names the part; it goes in before the part is
		// filled, so it is not generated as any required field
		if d := s.Discriminator; d != nil && d.PropertyName != "" {
			o, _ := src.(map[string]any)
			if fieldName(o, d.PropertyName) == "" {
				with := map[string]any{d.PropertyName: discriminatorValue(d, b)}
				for k, v := range o {
					with[k] = v
				}
				src = with
				a.note(path+"/"+d.PropertyName, "the discriminator of the chosen oneOf/anyOf part")
			}
		}
		return a.fill(b, src, path, required, depth+1)
	}
	switch x := src.(type) {
	case map[string]any:
		if len(props) == 0 {
			return src, true
		}
		out := map[string]any{}
		for _, k := range sortedKeys(props) {
			p := props[k]
			if p == nil || p.Value == nil || (a.mode == spec.ModeRequest && p.Value.ReadOnly) || (a.mode == spec.ModeResponse && p.Value.WriteOnly) {
				continue
			}
			isReq := slices.Contains(req, k)
			sub := path + "/" + k
			if f := fieldName(x, k); f != "" {
				if x[f] == nil && !isReq {
					out[k] = nil
					continue
				}
				if x[f] != nil {
					if v, ok := a.fill(p, x[f], sub, isReq, depth+1); ok {
						out[k] = v
					}
					continue
				}
			}
			if v, ok := a.find(p, k, x, sub, isReq, depth+1); ok {
				out[k] = v
			}
		}
		return out, true
	case []any:
		if s.Items == nil {
			return src, true
		}
		out := make([]any, 0, len(x))
		for i, e := range x {
			if v, ok := a.fill(s.Items, e, path+"/"+strconv.Itoa(i), true, depth+1); ok {
				out = append(out, v)
			}
		}
		return out, true
	case nil:
		name := path[strings.LastIndex(path, "/")+1:]
		return a.find(ref, name, nil, path, required, depth)
	}
	return src, true
}

// find is the value of a part the data lacks: a field of a selected record
// (dockId → the id of the dock), the record of the DTO's table, an object
// an answer holds, and for a required part a generated value. A list of
// records is only filled if the schema requires it: a POST would create
// those records too. A part that its object refers to (dock next to
// dockId in parent) is that record, or none.
func (a *assembler) find(ref *openapi3.SchemaRef, name string, parent map[string]any, path string, required bool, depth int) (any, bool) {
	if ref == nil || ref.Value == nil || depth > maxAssembleDepth || (!required && !a.create) {
		return nil, false
	}
	s := ref.Value
	rd := a.w.rd
	if _, prim := dict.Primitive(s); prim {
		if v, src, ok := a.scalar(name); ok {
			a.note(path, src)
			return coerce(v, s), true
		}
		if required {
			return a.generate(s, name, path)
		}
		return nil, false
	}
	if value.Type(s) == "array" {
		if !required || s.Items == nil {
			return nil, false
		}
		e, ok := a.find(s.Items, strings.TrimSuffix(name, "s"), nil, path+"/0", true, depth+1)
		if !ok {
			return nil, false
		}
		out := []any{e}
		for i := 1; i < int(s.MinItems) && !s.UniqueItems; i++ {
			out = append(out, e)
		}
		return out, true
	}
	t := ""
	if len(dict.DTOParts(ref)) > 0 {
		t = rd.n.of(ref)
	}
	want := refersTo(parent, t)
	if r := a.record(t, want); r != nil {
		v, _ := a.fill(ref, r.data, path, required, depth+1)
		a.note(path, r.origin())
		return v, true
	}
	idx := a.w.index()
	for _, dto := range dict.DTOParts(ref) {
		for _, e := range idx[dto] {
			if _, id := idOf(e.v); want != nil && !same(id, want) {
				continue
			}
			v, _ := a.fill(ref, e.v, path, required, depth+1)
			a.note(path, fmt.Sprintf("the %s in the answer of %s", dto, e.src))
			return v, true
		}
	}
	if want != nil && !required {
		return nil, false // no data of the record it refers to
	}
	if e, ok := a.match(s); ok {
		v, _ := a.fill(ref, e.v, path, required, depth+1)
		a.note(path, fmt.Sprintf("an object with the same fields in the answer of %s", e.src))
		return v, true
	}
	if !required {
		return nil, false
	}
	v, _ := a.fill(ref, map[string]any{}, path, true, depth+1)
	return v, true
}

// refersTo is the id the object refers to a record of table t by (dockId),
// nil if it has none.
func refersTo(parent map[string]any, t string) any {
	if t == "" {
		return nil
	}
	for _, k := range sortedKeys(parent) {
		if refTable(k) == t && scalar(parent[k]) {
			return parent[k]
		}
	}
	return nil
}

// record is the record of table t with the id want, else the selected one
// of the table if it has that id or want is nil.
func (a *assembler) record(t string, want any) *rec {
	rd := a.w.rd
	if t == "" {
		return nil
	}
	if want != nil {
		for _, r := range rd.all[t] {
			if r.hasID(want) {
				return r
			}
		}
	}
	if r := rd.recs[t]; r != nil && (want == nil || r.hasID(want)) {
		return r
	}
	return nil
}

// scalar is a field of a selected record for a part by its name: a
// reference (dockId → the id of the dock) or a name that is not generic.
func (a *assembler) scalar(name string) (any, string, bool) {
	k := a.w.rd.k
	if k == nil {
		return nil, "", false
	}
	if t := refTable(name); t != "" {
		if v, ok := k.value(t, "id"); ok {
			return v.v, v.src, true
		}
	}
	if generic(strings.ToLower(name)) {
		return nil, "", false
	}
	if v, ok := k.fieldAt(nil, name); ok {
		return v.v, v.src, true
	}
	return nil, "", false
}

// generate is a value for a required part no data has, as apitest-gen
// generates it: default, enum, format, pattern.
func (a *assembler) generate(s *openapi3.Schema, name, path string) (any, bool) {
	r := value.Generate(s, value.Context{Seed: 1, Path: a.seed + path, Name: name})
	if !r.OK {
		return nil, false
	}
	a.note(path, "generated: the schema requires it, no data read has it")
	if a.w.generated == nil {
		a.w.generated = map[string]bool{}
	}
	a.w.generated[a.seed+" "+path] = true
	return spec.Normalize(r.Value), true
}

// branch is the oneOf or anyOf part src fits best: the one with the most
// of its fields.
func (a *assembler) branch(branches []*openapi3.SchemaRef, src any) *openapi3.SchemaRef {
	best, score := branches[0], -1
	o, _ := src.(map[string]any)
	for _, b := range branches {
		if b == nil || b.Value == nil {
			continue
		}
		props, _ := dict.Properties(b.Value)
		n := 0
		for k := range props {
			if fieldName(o, k) != "" {
				n++
			}
		}
		if n > score {
			best, score = b, n
		}
	}
	return best
}

// discriminatorValue is the value the discriminator takes for a part: its
// key in "mapping", else the name of its DTO.
func discriminatorValue(d *openapi3.Discriminator, b *openapi3.SchemaRef) string {
	for _, k := range sortedKeys(d.Mapping) {
		if m := d.Mapping[k]; m.Ref == b.Ref || strings.HasSuffix(b.Ref, "/"+m.Ref) {
			return k
		}
	}
	if dto := dict.DTORef(b); dto != "" {
		return dto
	}
	return b.Ref[strings.LastIndex(b.Ref, "/")+1:]
}

// match is the object of an answer that has the most fields of an inline
// schema: at least two and half of them.
func (a *assembler) match(s *openapi3.Schema) (indexed, bool) {
	props, _ := dict.Properties(s)
	if len(props) < 2 {
		return indexed{}, false
	}
	var best indexed
	score := 0
	for _, e := range a.w.index()[""] {
		n := 0
		for k := range props {
			if fieldName(e.v, k) != "" {
				n++
			}
		}
		if n > score {
			best, score = e, n
		}
	}
	return best, score >= 2 && 2*score >= len(props)
}

// repair gives the required parts the validator still misses, and the
// parts taken from other data that violate their schema, a generated
// value. A value of the data itself stays as it is.
func (a *assembler) repair(ref *openapi3.SchemaRef, v any) any {
	val := spec.NewValidator()
	for range 3 {
		errs := val.Validate(ref.Value, spec.Normalize(v), a.mode)
		changed := false
		for _, e := range errs {
			cur := at(v, e.Pointer)
			if _, missing := cur.(missing); !missing {
				if _, assembled := a.from[e.Pointer]; !assembled {
					continue
				}
			}
			sub := schemaAt(ref, e.Pointer)
			if sub == nil || sub.Value == nil {
				continue
			}
			name := e.Pointer[strings.LastIndex(e.Pointer, "/")+1:]
			var nv any
			var ok bool
			if _, prim := dict.Primitive(sub.Value); prim {
				nv, ok = a.generate(sub.Value, name, e.Pointer)
			} else {
				nv, ok = a.fill(sub, map[string]any{}, e.Pointer, true, 0)
			}
			if ok && setAt(v, e.Pointer, nv) {
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return v
}

// schemaAt is the schema at a JSON pointer below ref.
func schemaAt(ref *openapi3.SchemaRef, pointer string) *openapi3.SchemaRef {
	cur := ref
	for _, seg := range strings.Split(strings.Trim(pointer, "/"), "/") {
		if seg == "" {
			continue
		}
		if cur == nil || cur.Value == nil {
			return nil
		}
		if _, err := strconv.Atoi(seg); err == nil && cur.Value.Items != nil {
			cur = cur.Value.Items
			continue
		}
		props, _ := dict.Properties(cur.Value)
		cur = propOf(props, seg)
	}
	return cur
}

// setAt sets the value at a JSON pointer; the object or list holding it
// must exist.
func setAt(v any, pointer string, nv any) bool {
	segs := strings.Split(strings.Trim(pointer, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return false
	}
	parent := at(v, "/"+strings.Join(segs[:len(segs)-1], "/"))
	last := segs[len(segs)-1]
	switch p := parent.(type) {
	case map[string]any:
		p[last] = nv
		return true
	case []any:
		if i, err := strconv.Atoi(last); err == nil && i < len(p) {
			p[i] = nv
			return true
		}
	}
	return false
}

// index are the objects the GETs answered, by the DTO whose schema they
// were found below ("" holds every object): built again when a GET was
// read since.
func (w *writes) index() map[string][]indexed {
	if w.idx != nil && w.idxN == len(w.rd.gets) {
		return w.idx
	}
	w.idx, w.idxN = map[string][]indexed{}, len(w.rd.gets)
	ids := sortedKeys(w.rd.gets)
	slices.SortStableFunc(ids, func(a, b string) int { return w.rd.gets[a].resp.Seq - w.rd.gets[b].resp.Seq })
	for _, id := range ids {
		f := w.rd.gets[id]
		w.indexValue(f.resp.Body, responseSchema(f.op, f.resp.Status), fmt.Sprintf("#%d GET %s", f.resp.Seq, f.url), false, 0)
	}
	for k, l := range w.idx {
		slices.SortStableFunc(l, func(a, b indexed) int {
			switch {
			case a.selected == b.selected:
				return 0
			case a.selected:
				return -1
			}
			return 1
		})
		w.idx[k] = l
	}
	return w.idx
}

func (w *writes) indexValue(v any, ref *openapi3.SchemaRef, src string, selected bool, depth int) {
	if ref == nil || ref.Value == nil || depth > maxAssembleDepth {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		if t := w.rd.n.of(ref); t != "" && w.rd.taken(t, x) != nil {
			selected = true
		}
		for _, dto := range dict.DTOParts(ref) {
			w.idx[dto] = append(w.idx[dto], indexed{v: x, src: src, selected: selected})
		}
		w.idx[""] = append(w.idx[""], indexed{v: x, src: src, selected: selected})
		props, _ := dict.Properties(ref.Value)
		for _, k := range sortedKeys(x) {
			if p := propOf(props, k); p != nil {
				w.indexValue(x[k], p, src, selected, depth+1)
			}
		}
	case []any:
		for _, e := range x {
			w.indexValue(e, ref.Value.Items, src, selected, depth+1)
		}
	}
}
