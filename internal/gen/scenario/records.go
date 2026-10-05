package scenario

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Store holds the start state: the records of every resource, by resource
// name, and the lists the snapshot fetched, by operationId.
type Store struct {
	records map[string][]Record
	lists   map[string][]any
	fetched map[string]bool
	// examples are fetched answers of operations of no resource, by
	// operationId (followingDetails)
	examples map[string]any
	params   map[string]map[string]any // their path and query parameter values
}

func newStore() *Store {
	return &Store{records: map[string][]Record{}, lists: map[string][]any{}, fetched: map[string]bool{}, examples: map[string]any{}, params: map[string]map[string]any{}}
}

// Records returns the records of a resource.
func (s *Store) Records(resource string) []Record {
	return s.records[strings.ToLower(resource)]
}

// Fetched reports whether the records of a resource come from the
// instance.
func (s *Store) Fetched(resource string) bool { return s.fetched[strings.ToLower(resource)] }

func (s *Store) set(r *model.Resource, recs []Record) { s.records[strings.ToLower(r.Name)] = recs }

// has reports whether the records of a resource are made, fetched or
// generated.
func (s *Store) has(resource string) bool {
	_, ok := s.records[strings.ToLower(resource)]
	return ok
}

// builder creates the records.
type builder struct {
	in    Input
	res   *Result
	store *Store
	v     *spec.Validator
	// keys are the key values the defaults set: resource → field → value
	keys map[string]map[string]keyDefault
	// detailOf is the operation of "followingDetails" the records of a
	// resource come from, for resources without a source of their own
	detailOf map[string]*model.Op
	// origins are the requests the records of a resource were fetched
	// with, by record, for the messages
	origins map[string][]string
	// seeds are the "seed" values of the entries fetched so far, in order
	seeds []seedSets
}

func (b *builder) setOrigins(r *model.Resource, origins []string) {
	if b.origins == nil {
		b.origins = map[string][]string{}
	}
	b.origins[strings.ToLower(r.Name)] = origins
}

// origin is the request record i of r was fetched with.
func (b *builder) origin(r *model.Resource, i int) string {
	if o := b.origins[strings.ToLower(r.Name)]; i < len(o) {
		return o[i]
	}
	return "the snapshot"
}

type keyDefault struct {
	key   string
	value any
}

func (b *builder) validator() *spec.Validator {
	if b.v == nil {
		b.v = spec.NewValidator()
	}
	return b.v
}

// ordered returns the resources, parents before their children and every
// resource a relation refers to before the resource with the relation.
func ordered(m *model.Model) []*model.Resource {
	depths := map[*model.Resource]int{}
	var depth func(r *model.Resource, seen int) int
	depth = func(r *model.Resource, seen int) int {
		if d, ok := depths[r]; ok {
			return d
		}
		if seen > 10 { // a cycle of relations
			return 0
		}
		n := 0
		if r.Parent != nil {
			n = depth(r.Parent, seen+1) + 1
		}
		for _, f := range r.Relations {
			if to, _ := r.Ref(f); to != nil && to != r {
				n = max(n, depth(to, seen+1)+1)
			}
		}
		if seen == 0 {
			depths[r] = n
		}
		return n
	}
	out := append([]*model.Resource(nil), m.Resources...)
	for _, r := range out {
		depth(r, 0)
	}
	sort.SliceStable(out, func(i, j int) bool { return depths[out[i]] < depths[out[j]] })
	return out
}

// count is the number of records of a resource ("$snapshot" count).
func (b *builder) count(r *model.Resource) int {
	if s, ok := b.in.Defaults.SnapshotFor(r.Name); ok {
		return s.Records()
	}
	return 1
}

// generate builds the records from the examples apply wrote and the
// defaults, without a running instance.
func (b *builder) generate() {
	b.keyDefaults()
	for _, r := range ordered(b.in.Model) {
		b.generated(r)
	}
}

// generated builds the records of one resource: the first from the
// examples of its operations, laid over each other (a list first, then the
// reads, then the bodies), the defaults on top; the others are generated.
func (b *builder) generated(r *model.Resource) {
	kept := b.kept(r)
	first := Record{}
	if len(kept) > 0 {
		first = kept[0].clone()
	}
	merge := func(v any) {
		obj, ok := v.(map[string]any)
		if !ok {
			return
		}
		for _, k := range sortedKeys(obj) {
			if f := r.Field(k); f != "" {
				if _, has := first[f]; !has {
					first[f] = spec.Normalize(obj[k])
				}
			}
		}
	}
	for _, role := range []model.Role{model.RoleList, model.RoleRead} {
		for _, o := range r.OpsWith(role) {
			v := b.responseExample(o)
			if role == model.RoleList {
				if items, ok := listItems(v, o.Items); ok && len(items) > 0 {
					merge(items[0])
				}
				continue
			}
			merge(v)
		}
	}
	for _, role := range []model.Role{model.RoleUpdate, model.RoleCreate} {
		for _, o := range r.OpsWith(role) {
			merge(b.requestExample(o))
		}
	}
	b.fieldDefaults(r, first, false)
	b.setKeys(r, first)
	b.relate(r, first)
	recs := []Record{first}
	for i := 1; i < b.count(r); i++ {
		if i < len(kept) {
			recs = append(recs, kept[i])
			continue
		}
		recs = append(recs, b.another(r, first, i))
	}
	b.fit(r, recs, false)
	b.store.set(r, recs)
}

// fit makes the keys of the records valid for every path parameter that
// holds them: a parameter often has a pattern the DTO field lacks
// ({Code} with ^[a-z]+$, Code without). A generated key gets a new value
// that fits both; a fetched one or one from the defaults is a problem.
func (b *builder) fit(r *model.Resource, recs []Record, fetched bool) {
	b.fitKeys(r, recs, fetched, "records."+r.Name)
}

// fitKeys is fit with the path the new values are seeded with.
func (b *builder) fitKeys(r *model.Resource, recs []Record, fetched bool, path string) {
	for _, res := range b.in.Model.Resources {
		for _, o := range res.Ops {
			for _, mp := range o.Params {
				p := specParam(o.Op, mp.Name)
				if mp.Resource != r || p == nil || p.Schema == nil || p.Schema.Value == nil {
					continue
				}
				ps := p.Schema.Value
				for i, rec := range recs {
					if rec[mp.Field] == nil {
						continue
					}
					v := coerce(rec[mp.Field], ps)
					if b.valid(ps, v) {
						continue
					}
					where := fmt.Sprintf("%s.parameters[%s]", o.Op.Where, mp.Name)
					_, fromDefault := b.keys[strings.ToLower(r.Name)][mp.Field]
					if fetched || (i == 0 && fromDefault) {
						b.res.lint(b.in.IgnoreLinting, CodeInvalid, where, "%s #%d has %s = %s, which violates the schema of {%s}; the spec or the data is wrong",
							r.Name, i+1, mp.Field, text(v), mp.Name)
						continue
					}
					field := r.Schema(mp.Field)
					for try := 0; try < 8; try++ {
						g := value.Generate(ps, value.Context{Seed: b.in.Seed, Path: fmt.Sprintf("%s.%d.%s#%d", path, i, mp.Field, try), Name: mp.Field, Parent: r.Read()})
						nv := spec.Normalize(g.Value)
						if g.OK && (field == nil || b.valid(field.Value, coerce(nv, field.Value))) {
							if field != nil {
								nv = coerce(nv, field.Value)
							}
							rec[mp.Field] = nv
							break
						}
					}
				}
			}
		}
	}
}

// kept returns the records of r from the dictionary of the last run, as
// long as every value still fits its field: the start state stays the same
// from run to run, while the examples show the state after the updates.
func (b *builder) kept(r *model.Resource) []Record {
	if b.in.Dict == nil {
		return nil
	}
	var out []Record
	for name, recs := range b.in.Dict.Records {
		if !strings.EqualFold(name, r.Name) {
			continue
		}
		for _, raw := range recs {
			rec := Record{}
			for k, v := range raw {
				f := r.Field(k)
				ref := r.Schema(k)
				if f == "" || ref == nil || !b.valid(ref.Value, spec.Normalize(v)) {
					continue
				}
				rec[f] = spec.Normalize(v)
			}
			out = append(out, rec)
		}
	}
	return out
}

// another generates record i of a resource from the first one: every
// simple field gets a new value, the fields that refer to the parent keep
// theirs.
func (b *builder) another(r *model.Resource, first Record, i int) Record {
	rec := first.clone()
	for _, f := range sortedKeys(rec) {
		ref := r.Schema(f)
		if ref == nil || ref.Value == nil || !simple(ref.Value) || containsFold(r.Relations, f) {
			continue
		}
		if v, ok := b.different(ref.Value, rec[f], fmt.Sprintf("records.%s.%d.%s", r.Name, i, f), f, r.Read()); ok {
			rec[f] = v
		}
	}
	return rec
}

// different generates a valid value for s that differs from cur.
func (b *builder) different(s *openapi3.Schema, cur any, path, name, parent string) (any, bool) {
	for try := 0; try < 8; try++ {
		r := value.Generate(s, value.Context{Seed: b.in.Seed, Path: fmt.Sprintf("%s#%d", path, try), Name: name, Parent: parent})
		if !r.OK {
			return nil, false
		}
		v := spec.Normalize(r.Value)
		if b.valid(s, v) && !compare.Equal(v, cur) {
			return v, true
		}
	}
	return nil, false
}

// relate sets the fields that refer to another record (BookCode of an
// Article) to the key of the first record of that resource.
func (b *builder) relate(r *model.Resource, rec Record) {
	for _, f := range r.Relations {
		to, k := r.Ref(f)
		if to == nil {
			continue
		}
		if recs := b.store.Records(to.Name); len(recs) > 0 && recs[0][k] != nil {
			rec[f] = coerce(recs[0][k], r.Schema(f).Value)
		}
	}
}

// fieldDefaults lays the defaults of the fields over the first record. A
// fetched record keeps what the instance returned: the examples must show
// what the server answers.
func (b *builder) fieldDefaults(r *model.Resource, rec Record, fetched bool) {
	for _, f := range sortedFields(r) {
		e := b.fieldDefault(r, f)
		if e == nil {
			continue
		}
		ref := r.Schema(f)
		v := coerce(e.Value, ref.Value)
		if !b.valid(ref.Value, v) {
			continue // apply has reported it as DEFAULT_INVALID
		}
		if fetched {
			if !r.Fixed(f) && rec[f] != nil && !compare.Equal(v, rec[f]) {
				b.res.note(CodeSnapshotWins, r.Name+"."+f, "%q = %s is not used for the fetched %s: the instance returns %s, and the examples must show what the server answers",
					e.Key, text(v), r.Name, text(rec[f]))
			}
			continue
		}
		rec[f] = v
	}
}

func (b *builder) fieldDefault(r *model.Resource, f string) *defaults.Entry {
	for _, dto := range r.Schemas {
		if e := b.in.Defaults.Field(dto, "", f); e != nil {
			return e
		}
	}
	return nil
}

// keyDefaults collects the key values the defaults set through path
// parameters: "<operationId>.<param>" and "/path/{id}" for the last
// parameter of a path. Two different values for one key are a problem.
func (b *builder) keyDefaults() {
	b.keys = map[string]map[string]keyDefault{}
	for _, r := range b.in.Model.Resources {
		for _, o := range r.Ops {
			for _, p := range o.Params {
				var entries []*defaults.Entry
				if o.Op.HasOperationID {
					entries = append(entries, b.in.Defaults.Scoped(o.Op.ID, p.Name))
				}
				if lastParam(o.Op.Path) == p.Name {
					entries = append(entries, b.in.Defaults.Plain(o.Op.Path))
				}
				for _, e := range entries {
					if e != nil {
						b.addKey(p.Resource, p.Field, e)
					}
				}
			}
		}
		for _, k := range r.Keys {
			if e := b.fieldDefault(r, k); e != nil {
				b.addKey(r, k, e)
			}
		}
	}
}

func (b *builder) addKey(r *model.Resource, field string, e *defaults.Entry) {
	ref := r.Schema(field)
	if ref == nil || ref.Value == nil {
		return
	}
	v := coerce(e.Value, ref.Value)
	name := strings.ToLower(r.Name)
	if b.keys[name] == nil {
		b.keys[name] = map[string]keyDefault{}
	}
	if old, ok := b.keys[name][field]; ok {
		if !compare.Equal(old.value, v) {
			b.res.problem(CodeDefault, r.Name+"."+field, "%q = %s and %q = %s set the same key of the first %s differently; keep one value",
				old.key, text(old.value), e.Key, text(v), r.Name)
		}
		return
	}
	b.keys[name][field] = keyDefault{e.Key, v}
}

// setKeys sets the key values from the defaults in a generated record.
func (b *builder) setKeys(r *model.Resource, rec Record) {
	for f, k := range b.keys[strings.ToLower(r.Name)] {
		rec[f] = k.value
	}
}

// keepInDict keeps the records in the dictionary, and the values of the
// first record in the fields of the resource's DTOs, so the dictionary shows
// what the examples use.
func (b *builder) keepInDict() {
	d := b.in.Dict
	if d == nil {
		return
	}
	d.Records = map[string][]map[string]any{}
	for _, r := range b.in.Model.Resources {
		recs := b.store.Records(r.Name)
		if len(recs) == 0 {
			continue
		}
		for _, rec := range recs {
			d.Records[r.Name] = append(d.Records[r.Name], map[string]any(rec.clone()))
		}
		for _, name := range r.Schemas {
			n := d.Schemas[name]
			for i := 0; n != nil && n.Ref != "" && i < 30; i++ {
				n = d.Schemas[n.Ref]
			}
			if n == nil {
				continue
			}
			for k, child := range n.Properties {
				f := r.Field(k)
				v, ok := recs[0][f]
				if f == "" || !ok || child == nil || !child.Leaf() || child.Ref != "" {
					continue
				}
				if ref := r.Schema(f); ref != nil && b.valid(ref.Value, v) {
					child.Value = v
				}
			}
		}
	}
}

// responseExample is the example of the lowest 2xx JSON response of o.
func (b *builder) responseExample(o *model.Op) any {
	for _, p := range responsePlaces(b.in.Doc, o.Op) {
		if p.success {
			return p.example()
		}
	}
	return nil
}

// requestExample is the example of the JSON request body of o.
func (b *builder) requestExample(o *model.Op) any {
	if p := requestPlace(b.in.Doc, o.Op); p != nil {
		return p.example()
	}
	return nil
}

func (b *builder) valid(s *openapi3.Schema, v any) bool {
	return s != nil && len(b.validator().Validate(s, spec.Normalize(v), spec.ModePlain)) == 0
}

// listItems returns the list inside a list response.
func listItems(v any, pointer string) ([]any, bool) {
	if pointer != "" {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v = obj[strings.TrimPrefix(pointer, "/")]
	}
	items, ok := v.([]any)
	return items, ok
}

// simple reports whether a schema holds a single value.
func simple(s *openapi3.Schema) bool {
	_, ok := dict.Primitive(s)
	return ok
}

func sortedFields(r *model.Resource) []string { return r.FieldNames() }

// coerce adapts a default to the type of the schema: numeric strings to
// numbers and numbers to strings.
func coerce(v any, s *openapi3.Schema) any {
	if s == nil {
		return spec.Normalize(v)
	}
	t := value.Type(s)
	if p, ok := dict.Primitive(s); ok {
		t = p
	}
	switch x := v.(type) {
	case string:
		if t == "integer" || t == "number" {
			if _, err := strconv.ParseFloat(x, 64); err == nil {
				return json.Number(x)
			}
		}
	case json.Number:
		if t == "string" {
			return x.String()
		}
	case bool:
		if t == "string" {
			return strconv.FormatBool(x)
		}
	}
	return spec.Normalize(v)
}

// text renders a value for messages.
func text(v any) string {
	if v == nil {
		return "none"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func lastParam(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if strings.HasPrefix(segs[i], "{") && strings.HasSuffix(segs[i], "}") {
			return segs[i][1 : len(segs[i])-1]
		}
	}
	return ""
}

func equalJSON(a, b any) bool {
	return reflect.DeepEqual(spec.Normalize(a), spec.Normalize(b))
}
