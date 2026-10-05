package scenario

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/plan"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Order returns the cases in the order apitest runs them with the Config
// of "$apitest", and the bindings apitest uses.
func Order(s *spec.Spec, run defaults.Run) ([]*cases.Case, *bind.Set, error) {
	binds, err := bind.Resolve(s)
	if err != nil {
		return nil, nil, err
	}
	all, err := cases.Build(s, cases.Options{})
	if err != nil {
		return nil, nil, err
	}
	cases.ApplyMethodOrder(all, run.MethodOrder)
	opt := cases.Options{Tags: run.Tags, IncludeOps: run.IncludeOps, ExcludeOps: run.ExcludeOps}
	if err := cases.CheckOptions(s, opt); err != nil {
		return nil, nil, fmt.Errorf("\"$apitest\": %w", err)
	}
	p, err := plan.Build(all, func(c *cases.Case) bool { return cases.Selected(c.Op, opt) }, binds, plan.Options{Tags: run.Tags, DeleteLast: run.DeleteLast})
	if err != nil {
		return nil, nil, err
	}
	return p.Cases(), binds, nil
}

// ref names a record: one of the start records or one a POST created.
type ref struct {
	idx     int
	created *spec.Operation
}

// state is the data at one point of the run.
type state struct {
	records map[string][]Record // resource → records; nil once deleted
	created map[*spec.Operation]Record
	order   []*spec.Operation // creates in the order they ran
	changed map[string]string // record → the case that changed it last
	// unknown are the fields of a resource an operation of no resource has
	// changed: resource → field → that case (SIDE_EFFECT)
	unknown map[string]map[string]string
	server  map[*spec.Operation][]string // created record → keys the server assigns
}

func newState(store *Store) *state {
	st := &state{records: map[string][]Record{}, created: map[*spec.Operation]Record{}, changed: map[string]string{},
		unknown: map[string]map[string]string{}, server: map[*spec.Operation][]string{}}
	for name, recs := range store.records {
		for _, r := range recs {
			st.records[name] = append(st.records[name], r.clone())
		}
	}
	return st
}

func (st *state) get(r *model.Resource, at ref) Record {
	if at.created != nil {
		return st.created[at.created]
	}
	recs := st.records[strings.ToLower(r.Name)]
	if at.idx < len(recs) {
		return recs[at.idx]
	}
	return nil
}

func (st *state) put(r *model.Resource, at ref, rec Record) {
	if at.created != nil {
		if _, ok := st.created[at.created]; !ok {
			st.order = append(st.order, at.created)
		}
		st.created[at.created] = rec
		return
	}
	recs := st.records[strings.ToLower(r.Name)]
	if at.idx < len(recs) {
		recs[at.idx] = rec
	}
}

func (st *state) list(r *model.Resource) []Record {
	var out []Record
	for _, rec := range st.records[strings.ToLower(r.Name)] {
		if rec != nil {
			out = append(out, rec.clone())
		}
	}
	return out
}

// all returns the records of r a list can return: the start records, then
// the created ones in the order they were created; deleted ones are left out.
func (st *state) all(r *model.Resource, m *model.Model) []Record {
	out := st.list(r)
	for _, op := range st.order {
		rec := st.created[op]
		if o := m.Op(op); o == nil || o.Resource != r || rec == nil {
			continue
		}
		// the same keys are the same record: the POST replaced it
		replaced := false
		for i, x := range out {
			if matching(r, []Record{x}, map[string]any(rec)) != nil {
				out[i], replaced = rec.clone(), true
				break
			}
		}
		if !replaced {
			out = append(out, rec.clone())
		}
	}
	return out
}

// filter keeps the records whose fields have the values of the list's own
// path parameters, taken from rec (/book/class/{class} lists the books of
// one class).
func filter(o *model.Op, recs []Record, rec Record) []Record {
	var own []model.Param
	for _, mp := range o.Params {
		if mp.Resource == o.Resource {
			own = append(own, mp)
		}
	}
	if len(own) == 0 || rec == nil {
		return recs
	}
	var out []Record
	for _, r := range recs {
		keep := true
		for _, mp := range own {
			keep = keep && compare.Equal(spec.Normalize(r[mp.Field]), spec.Normalize(rec[mp.Field]))
		}
		if keep {
			out = append(out, r)
		}
	}
	return out
}

func key(r *model.Resource, at ref) string {
	if at.created != nil {
		return r.Name + " created by " + at.created.ID
	}
	return fmt.Sprintf("%s #%d", r.Name, at.idx+1)
}

// step is one case and the data it meets.
type step struct {
	c      *cases.Case
	o      *model.Op
	at     ref
	before Record // the record before the case; nil for lists and creates
	after  Record // the record after it; nil after a DELETE
	// others are the records of the other resources the path parameters
	// address: the first record of each
	others  map[*model.Resource]Record
	list    []Record
	body    any      // request body to write for the default example
	unknown []string // fields the examples leave out (SIDE_EFFECT, CREATED_KEY)
	// listUnknown are the fields left out per element of list
	listUnknown [][]string
}

type timeline struct {
	in    Input
	res   *Result
	store *Store
	binds *bind.Set
	st    *state
}

// play runs the cases in apitest's order on the records.
func (t *timeline) play() ([]*step, error) {
	order, binds, err := Order(t.in.Spec, t.in.Defaults.RunConfig())
	if err != nil {
		return nil, err
	}
	t.binds = binds
	t.st = newState(t.store)
	var steps []*step
	for _, c := range order {
		if c.Kind != cases.Positive || c.Skip != "" {
			continue
		}
		o := t.in.Model.Op(c.Op)
		if o == nil {
			t.effects(c)
			continue
		}
		s := &step{c: c, o: o, others: t.others(o)}
		r := o.Resource
		switch o.Role {
		case model.RoleList:
			s.at = t.target(c, o)
			s.before = t.st.get(r, s.at)
			s.list = filter(o, t.st.all(r, t.in.Model), s.before)
		case model.RoleCreate:
			if ownKey(o) && !strings.HasSuffix(c.Op.Path, "}") {
				// POST /Book/{code}/copy: the key addresses the first record
				if recs := t.st.all(r, t.in.Model); len(recs) > 0 {
					s.before = recs[0]
				}
			}
			s.at = ref{created: c.Op}
			var server []string
			s.after, s.body, server = t.create(c, o, s.others)
			// apitest binds the value of the first successful case of a
			// producer, so later creates of the same operation do not count
			if t.st.created[c.Op] == nil {
				t.st.put(r, s.at, s.after)
				t.st.server[c.Op] = server
			}
		default:
			s.at = t.target(c, o)
			s.before = t.st.get(r, s.at)
			if s.before == nil {
				if s.at.created != nil || s.at.idx < len(t.store.Records(r.Name)) {
					t.res.note(CodeUpdate, c.Op.Where, "%s runs after %s was deleted; its example is not changed", c.Name, key(r, s.at))
				}
				continue
			}
			s.after = s.before
			switch o.Role {
			case model.RoleUpdate:
				sent := c.Body
				if c.Example == cases.DefaultExample {
					s.after, s.body = t.update(c, o, s.before)
					if s.body != nil {
						sent = s.body
					}
				} else if obj, ok := c.Body.(map[string]any); ok {
					s.after = overlay(r, s.before, obj)
				}
				// what the update sends is known again
				if obj, ok := sent.(map[string]any); ok {
					for k := range obj {
						if f := r.Field(k); f != "" {
							delete(t.st.unknown[strings.ToLower(r.Name)], f)
						}
					}
				}
				t.st.put(r, s.at, s.after)
				t.st.changed[key(r, s.at)] = c.Name
				t.res.Stats.Updates++
			case model.RoleDelete:
				s.after = nil
				t.st.put(r, s.at, nil)
				t.st.changed[key(r, s.at)] = c.Name
			}
		}
		s.unknown = t.unknown(r, s.at.created)
		for _, rec := range s.list {
			s.listUnknown = append(s.listUnknown, t.unknown(r, t.createdBy(r, rec)))
		}
		steps = append(steps, s)
	}
	return steps, nil
}

// effects follows a writing case of no resource: the fields of the records
// it touches that the model cannot follow are unknown from then on, and the
// examples after it leave them out. A case that touches one resource
// (PUT /Booking/{id}/cancel) may change any field of it that is no key or
// relation; a POST that creates something below it does not. A case that
// links records of several resources
// (PUT /Dock/{code}/Ship/{id}/link) changes their fields named after the
// other resource (Ship.DockName) or referring to it (Ship.DockId), and the
// derived lists and objects of the resources below them.
func (t *timeline) effects(c *cases.Case) {
	if c.Op.Method == http.MethodGet {
		return
	}
	m := t.in.Model
	touched := m.Touched(c.Op)
	mark := func(r *model.Resource, f string) {
		name := strings.ToLower(r.Name)
		if t.st.unknown[name] == nil {
			t.st.unknown[name] = map[string]string{}
		}
		if _, ok := t.st.unknown[name][f]; !ok {
			t.st.unknown[name][f] = c.Name
		}
	}
	var changed []string
	// a POST below a record creates something there and leaves the record
	// alone (POST /gardens/{code}/orders)
	acts := c.Op.Method != http.MethodPost || strings.HasSuffix(c.Op.Path, "}")
	if len(touched) == 1 && acts {
		r := touched[0]
		for _, f := range r.FieldNames() {
			if !r.Fixed(f) {
				mark(r, f)
				changed = append(changed, r.Name+"."+f)
			}
		}
	}
	if len(touched) > 1 {
		for _, y := range m.Resources {
			var in *model.Resource // the touched resource y is or lies below
			for _, x := range touched {
				if x == y || slices.Contains(y.Ancestors(), x) {
					in = x
					break
				}
			}
			if in == nil {
				continue
			}
			for _, f := range y.FieldNames() {
				if slices.Contains(y.Keys, f) {
					continue
				}
				to, _ := y.Ref(f)
				hit := false
				for _, x := range touched {
					if x == y || x == in || slices.Contains(y.Ancestors(), x) {
						continue
					}
					hit = hit || to == x || named(f, x, y != in)
				}
				if s := y.Schema(f); !hit && y != in && s != nil && s.Value != nil && !simple(s.Value) {
					hit = true // a derived list or object of a record below
				}
				if hit {
					mark(y, f)
					changed = append(changed, y.Name+"."+f)
				}
			}
		}
	}
	if len(changed) > 0 {
		t.res.note(CodeSideEffect, c.Op.Where, "%s changes records of %s the model cannot follow; the examples after it leave out %s",
			c.Name, names(touched), strings.Join(changed, ", "))
	}
}

// named reports whether a field is named after the resource x: x itself or
// its plural (Dock, Docks), or x and one of its fields (DockName). In a
// resource below a touched one (a view) any name that starts with x counts.
func named(f string, x *model.Resource, view bool) bool {
	if len(f) < len(x.Name) || !strings.EqualFold(f[:len(x.Name)], x.Name) {
		return false
	}
	rest := f[len(x.Name):]
	return view || rest == "" || strings.EqualFold(rest, "s") || x.Field(rest) != ""
}

// unknown are the fields of a record of r the examples leave out: those an
// operation of no resource changed, and for a record a POST created the
// keys the server assigned.
func (t *timeline) unknown(r *model.Resource, created *spec.Operation) []string {
	out := sortedKeys(t.st.unknown[strings.ToLower(r.Name)])
	if created != nil {
		for _, k := range t.st.server[created] {
			if !containsFold(out, k) && !t.required(r, k) {
				out = append(out, k)
			}
		}
	}
	return out
}

// createdBy returns the POST that created a record of a list, or nil.
func (t *timeline) createdBy(r *model.Resource, rec Record) *spec.Operation {
	for _, op := range t.st.order {
		if x := t.st.created[op]; x != nil && t.in.Model.Op(op) != nil && t.in.Model.Op(op).Resource == r &&
			equalJSON(map[string]any(x), map[string]any(rec)) {
			return op
		}
	}
	return nil
}

func names(rs []*model.Resource) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return strings.Join(out, ", ")
}

// others returns the first record of every other resource o's path
// parameters address: the first start record that still exists, else the
// first one the test created.
func (t *timeline) others(o *model.Op) map[*model.Resource]Record {
	out := map[*model.Resource]Record{}
	for _, mp := range o.Params {
		if mp.Resource == o.Resource {
			continue
		}
		if _, ok := out[mp.Resource]; ok {
			continue
		}
		if recs := t.st.all(mp.Resource, t.in.Model); len(recs) > 0 {
			out[mp.Resource] = recs[0]
		}
	}
	return out
}

// target is the record a read, update or delete addresses: the one a POST
// created when apitest binds the key to that POST, else the first record.
func (t *timeline) target(c *cases.Case, o *model.Op) ref {
	for _, mp := range o.Params {
		if mp.Resource != o.Resource {
			continue
		}
		p := specParam(c.Op, mp.Name)
		if p == nil {
			continue
		}
		// a bound key comes from the producer at run time, even after the
		// record it created was deleted
		if b := t.binds.For(c.Op, p); b != nil {
			if po := t.in.Model.Op(b.Producer); po != nil && po.Role == model.RoleCreate && po.Resource == o.Resource {
				if _, created := t.st.created[b.Producer]; created {
					return ref{created: b.Producer}
				}
			}
		}
	}
	return ref{}
}

// update builds the body of the default example of an update: every simple
// field gets a new valid value, except keys, fields that refer to the
// parent and fields an operation default sets ("UpdateBook.Name").
func (t *timeline) update(c *cases.Case, o *model.Op, before Record) (Record, any) {
	r := o.Resource
	pl := requestPlace(t.in.Doc, c.Op)
	if pl == nil || pl.named {
		return before, nil
	}
	base, _ := pl.example().(map[string]any)
	after := before.clone()
	props, _ := dict.Properties(pl.schema.Value)
	var changes []string
	for _, k := range sortedKeys(props) {
		ps := props[k].Value
		f := r.Field(k)
		if ps == nil || ps.ReadOnly || f == "" || r.Fixed(f) {
			continue
		}
		if !simple(ps) {
			if _, has := after[f]; !has && base != nil && base[k] != nil {
				after[f] = spec.Normalize(base[k])
			}
			continue
		}
		// the new value depends only on the record, never on the example
		// written last time, so a second run gives the same body
		cur := after[f]
		var v any
		if e := t.in.Defaults.Scoped(c.Op.ID, k); e != nil {
			v = coerce(e.Value, ps)
		} else if nv, ok := (&builder{in: t.in}).different(ps, cur, "update."+c.Op.ID+"."+k, k, r.Read()); ok {
			v = nv
		}
		if v == nil || compare.Equal(v, cur) {
			continue
		}
		after[f] = v
		changes = append(changes, fmt.Sprintf("%s %s → %s", k, text(cur), text(v)))
	}
	if len(changes) > 0 {
		t.res.note(CodeUpdate, c.Op.Where, "%s changes %s: %s", c.Name, key(r, t.target(c, o)), strings.Join(changes, ", "))
	}
	return after, project(pl.schema, base, after, r, spec.ModeRequest)
}

// create builds the record a POST creates: its body, with the fields that
// refer to another record set to the record the path addresses or the first
// one of that resource, and what its response adds. A key of the default
// body that a path parameter holds gets a value that fits the parameter.
// The keys in a path that ends with a key (/Book/{code}) belong to the
// record; in /Book/{code}/copy they address an existing one. It also
// returns the keys the server assigns.
func (t *timeline) create(c *cases.Case, o *model.Op, others map[*model.Resource]Record) (Record, any, []string) {
	r := o.Resource
	rec := Record{}
	var body any
	pl := requestPlace(t.in.Doc, c.Op)
	def := c.Example == cases.DefaultExample && (pl == nil || !pl.named)
	var obj map[string]any
	if pl != nil {
		if def {
			obj, _ = pl.example().(map[string]any)
		} else {
			obj, _ = c.Body.(map[string]any)
		}
		for k, v := range obj {
			if f := r.Field(k); f != "" {
				rec[f] = spec.Normalize(v)
			}
		}
		for _, f := range r.Relations {
			to, k := r.Ref(f)
			if to == nil {
				continue
			}
			src := others[to]
			if src == nil {
				if recs := t.st.all(to, t.in.Model); len(recs) > 0 {
					src = recs[0]
				}
			}
			if src != nil && src[k] != nil {
				rec[f] = coerce(src[k], r.Schema(f).Value)
			}
		}
	}
	for _, mp := range o.Params {
		p := specParam(c.Op, mp.Name)
		if mp.Resource != r || rec[mp.Field] != nil || p == nil || !strings.HasSuffix(c.Op.Path, "}") {
			continue
		}
		var v any
		if def {
			v = paramExample(t.in.Doc, c.Op, p)
		} else if val, ok := params.Resolve(p, params.Inputs{CaseName: c.ParamSource(), OpID: c.Op.ID}); ok {
			v = val.V
		}
		if v != nil {
			rec[mp.Field] = coerce(v, r.Schema(mp.Field).Value)
		}
	}
	if def {
		(&builder{in: t.in, res: t.res}).fitKeys(r, []Record{rec}, false, "created."+c.Op.ID)
		if pl != nil {
			body = project(pl.schema, obj, rec, r, spec.ModeRequest)
		}
	}
	sent := rec.clone()
	for _, p := range responsePlaces(t.in.Doc, c.Op) {
		if !p.success {
			continue
		}
		if obj, ok := p.example().(map[string]any); ok {
			for k, v := range obj {
				if f := r.Field(k); f != "" && rec[f] == nil {
					rec[f] = spec.Normalize(v)
				}
			}
		}
		break
	}
	var server []string
	for _, k := range r.Keys {
		ref := r.Schema(k)
		if _, has := sent[k]; has || ref == nil || ref.Value == nil || ref.Value.ReadOnly || presenceOnly(ref.Value) {
			continue
		}
		server = append(server, k)
		if t.required(r, k) {
			t.res.note(CodeCreatedKey, c.Op.Where, "the server assigns %s.%s when %s runs; the examples of the cases that read the created %s show %s, the server will return another value. Mark %s readOnly in the spec (apitest then only checks that it is there) or add it to IgnoreFields",
				r.Name, k, c.Op.ID, r.Name, text(rec[k]), k)
			continue
		}
		t.res.note(CodeCreatedKey, c.Op.Where, "the server assigns %s.%s when %s runs; the examples of the cases that read the created %s leave it out",
			r.Name, k, c.Op.ID, r.Name)
	}
	return rec, body, server
}

// required reports whether the read DTO of r requires the field, so an
// example cannot leave it out.
func (t *timeline) required(r *model.Resource, field string) bool {
	if t.in.Spec.Doc.Components == nil {
		return false
	}
	ref := t.in.Spec.Doc.Components.Schemas[r.Read()]
	if ref == nil || ref.Value == nil {
		return false
	}
	_, req := dict.Properties(ref.Value)
	return containsFold(req, field)
}

// overlay lays the fields of a sent body over a record. A nested object
// keeps the fields the body leaves out, such as the readOnly id of a
// nested DTO that a request must not contain.
func overlay(r *model.Resource, rec Record, body map[string]any) Record {
	out := rec.clone()
	for k, v := range body {
		f := r.Field(k)
		if f == "" {
			f = k
		}
		out[f] = merge(out[f], spec.Normalize(v))
	}
	return out
}

// merge lays top over base: objects field by field, lists of the same
// length element by element; anything else is replaced by top.
func merge(base, top any) any {
	switch t := top.(type) {
	case map[string]any:
		b, ok := base.(map[string]any)
		if !ok {
			return top
		}
		out := make(map[string]any, len(b)+len(t))
		for k, v := range b {
			out[k] = v
		}
		for k, v := range t {
			out[k] = merge(b[k], v)
		}
		return out
	case []any:
		b, ok := base.([]any)
		if !ok || len(b) != len(t) {
			return top
		}
		out := make([]any, len(t))
		for i := range t {
			out[i] = merge(b[i], t[i])
		}
		return out
	}
	return top
}

// presenceOnly reports the formats apitest only checks for presence.
func presenceOnly(s *openapi3.Schema) bool {
	switch s.Format {
	case "uuid", "date-time", "date":
		return true
	}
	return false
}

func specParam(op *spec.Operation, name string) *openapi3.Parameter {
	for _, p := range op.Params {
		if p.In == openapi3.ParameterInPath && p.Name == name {
			return p
		}
	}
	return nil
}

// project lays the fields of a record over an example of a schema: every
// property the record has gets its value, the others keep theirs.
func project(ref *openapi3.SchemaRef, base any, rec Record, r *model.Resource, mode spec.Mode) any {
	if ref == nil || ref.Value == nil {
		return base
	}
	out := map[string]any{}
	if obj, ok := base.(map[string]any); ok {
		for k, v := range obj {
			out[k] = v
		}
	}
	props, _ := dict.Properties(ref.Value)
	for k, p := range props {
		if p.Value == nil || (mode == spec.ModeRequest && p.Value.ReadOnly) || (mode == spec.ModeResponse && p.Value.WriteOnly) {
			continue
		}
		f := r.Field(k)
		if f == "" {
			continue
		}
		if v, ok := rec[f]; ok && v != nil {
			out[k] = visible(coerce(v, p.Value), p, mode, 0)
		}
	}
	return out
}

// visible keeps the fields of a nested value the schema has in this mode:
// a record holds the readOnly fields of a nested DTO (from the response),
// a request body must not.
func visible(v any, ref *openapi3.SchemaRef, mode spec.Mode, depth int) any {
	if ref == nil || ref.Value == nil || depth > 20 {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := dict.Properties(ref.Value)
		if len(props) == 0 {
			return v
		}
		out := make(map[string]any, len(x))
		for k, cv := range x {
			c := props[k]
			if c == nil || c.Value == nil || (mode == spec.ModeRequest && c.Value.ReadOnly) || (mode == spec.ModeResponse && c.Value.WriteOnly) {
				continue
			}
			out[k] = visible(cv, c, mode, depth+1)
		}
		return out
	case []any:
		if ref.Value.Items == nil {
			return v
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = visible(e, ref.Value.Items, mode, depth+1)
		}
		return out
	}
	return v
}
