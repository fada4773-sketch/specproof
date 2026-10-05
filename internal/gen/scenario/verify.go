package scenario

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Codes of Verify.
const (
	CodeStale    = "EXAMPLE_STALE"   // an example does not show the record as it is at its case
	CodeNoRecord = "PARAM_NO_RECORD" // a parameter addresses a record that does not exist
)

// Verify plays the cases of the written spec the way apitest builds and
// orders them, on the start records, independently of how the examples were
// made: it takes the parameters and bodies apitest will send, applies every
// update to the record it addresses, and checks that every example of a
// read, a list and a response shows the record as it is at that point.
func Verify(written *spec.Spec, defs *defaults.Defaults, start *Store) []string {
	if start == nil || len(start.records) == 0 {
		return nil
	}
	m := model.Detect(written, defs.Model)
	order, binds, err := Order(written, defs.RunConfig())
	if err != nil {
		return []string{fmt.Sprintf("%s the cases of the written spec cannot be ordered: %v", CodePlan, err)}
	}
	v := &verifier{m: m, binds: binds, st: newState(start), start: start,
		ignore: defs.RunConfig().IgnoreFields, server: map[*spec.Operation][]string{}}
	for _, c := range order {
		if c.Kind != cases.Positive || c.Skip != "" || c.NotBuildable != "" {
			continue
		}
		if o := m.Op(c.Op); o != nil {
			v.play(c, o)
		}
	}
	return v.problems
}

type verifier struct {
	m        *model.Model
	binds    *bind.Set
	st       *state
	start    *Store
	ignore   []string
	server   map[*spec.Operation][]string // created record → keys the server assigns
	problems []string
}

func (v *verifier) fail(code, format string, args ...any) {
	v.problems = append(v.problems, code+" "+fmt.Sprintf(format, args...))
}

// play applies one case to the records and checks its examples.
func (v *verifier) play(c *cases.Case, o *model.Op) {
	r := o.Resource
	at, ok := v.address(c, o)
	if !ok {
		return
	}
	switch o.Role {
	case model.RoleList:
		v.checkList(c, o)
	case model.RoleCreate:
		rec := Record{}
		if obj, ok := c.Body.(map[string]any); ok {
			rec = overlay(r, rec, obj)
		}
		var server []string
		for _, k := range r.Keys {
			if _, sent := rec[k]; !sent {
				server = append(server, k)
			}
		}
		if obj, ok := c.Expect.Example.(map[string]any); ok && c.Expect.HasExample {
			for k, val := range obj {
				if f := r.Field(k); f != "" && rec[f] == nil {
					rec[f] = spec.Normalize(val)
				}
			}
		}
		if v.st.created[c.Op] == nil { // apitest binds the first one
			v.server[c.Op] = server
			v.st.put(r, ref{created: c.Op}, rec)
		}
	case model.RoleRead:
		v.checkRecord(c, r, at)
	case model.RoleUpdate:
		if obj, ok := c.Body.(map[string]any); ok && c.HasBody {
			v.st.put(r, at, overlay(r, v.st.get(r, at), obj))
			v.st.changed[key(r, at)] = c.Name
		}
		v.checkRecord(c, r, at)
	case model.RoleDelete:
		v.st.put(r, at, nil)
		v.st.changed[key(r, at)] = c.Name
	}
}

// address finds the record a case addresses through its path parameters,
// as apitest resolves them: a bound parameter takes the record of its
// producer, every other one its example.
func (v *verifier) address(c *cases.Case, o *model.Op) (ref, bool) {
	at := ref{}
	for _, mp := range o.Params {
		p := specParam(c.Op, mp.Name)
		if p == nil {
			continue
		}
		if b := v.binds.For(c.Op, p); b != nil {
			if _, created := v.st.created[b.Producer]; created && mp.Resource == o.Resource {
				at = ref{created: b.Producer}
			}
			continue // the value comes from the producer at run time
		}
		val, ok := params.Resolve(p, params.Inputs{CaseName: c.ParamSource(), OpID: c.Op.ID})
		if !ok {
			return at, false
		}
		idx := -1
		for i, rec := range v.st.records[strings.ToLower(mp.Resource.Name)] {
			if rec != nil && compare.Equal(spec.Normalize(rec[mp.Field]), spec.Normalize(val.V)) {
				idx = i
				break
			}
		}
		if idx < 0 && mp.Resource != o.Resource && v.createdWith(mp.Resource, mp.Field, val.V) {
			continue // a record the test created before
		}
		if idx < 0 {
			v.fail(CodeNoRecord, "%s sends {%s} = %s, but no %s has %s %s at this point; the request would not find it",
				c.Name, mp.Name, text(val.V), mp.Resource.Name, mp.Field, text(val.V))
			return at, false
		}
		if mp.Resource == o.Resource {
			at = ref{idx: idx}
		}
	}
	if o.Role != model.RoleList && o.Role != model.RoleCreate && v.st.get(o.Resource, at) == nil {
		return at, false // deleted before: the order of the run, not an example
	}
	return at, true
}

// matches reports whether an element shows the record: it has every key of
// it, apart from the keys the server assigns to a record the test created,
// which the examples leave out.
func (v *verifier) matches(r *model.Resource, rec Record, obj map[string]any) bool {
	if matching(r, []Record{rec}, obj) != nil {
		return true
	}
	at := v.locate(r, rec)
	if at.created == nil || len(r.Keys) == 0 {
		return false
	}
	seen := 0
	for _, k := range r.Keys {
		val := fieldOf(obj, k)
		switch {
		case val == nil && containsFold(v.server[at.created], k):
			continue
		case val == nil || !equalJSON(val, rec[k]):
			return false
		}
		seen++
	}
	return seen > 0
}

// createdWith reports whether a record the test created and did not delete
// has the value in the field.
func (v *verifier) createdWith(r *model.Resource, field string, val any) bool {
	for _, op := range v.st.order {
		if x := v.st.created[op]; x != nil && v.m.Op(op) != nil && v.m.Op(op).Resource == r &&
			compare.Equal(spec.Normalize(x[field]), spec.Normalize(val)) {
			return true
		}
	}
	return false
}

// checkRecord compares the expected body of a case with the record.
func (v *verifier) checkRecord(c *cases.Case, r *model.Resource, at ref) {
	if !c.Expect.HasExample {
		return
	}
	obj, ok := c.Expect.Example.(map[string]any)
	if !ok {
		return
	}
	rec := v.st.get(r, at)
	family := c.Expect.Response != nil && slices.ContainsFunc(responseDTOs(c), func(dto string) bool { return containsFold(r.Schemas, dto) })
	for _, k := range sortedKeys(obj) {
		f := r.Field(k)
		if f == "" || v.ignored(c, r, f) || (at.created != nil && containsFold(v.server[at.created], f)) {
			continue
		}
		if !family && !containsFold(r.Keys, f) {
			continue // another DTO: only its keys belong to the record
		}
		want, has := rec[f]
		if !has || want == nil || compare.Equal(spec.Normalize(obj[k]), want) {
			continue
		}
		v.fail(CodeStale, "%s expects %s = %s, but %s has %s at this point%s",
			c.Name, k, text(obj[k]), key(r, at), text(want), v.since(r, at))
	}
}

// checkList compares the elements of a list example with the records.
func (v *verifier) checkList(c *cases.Case, o *model.Op) {
	if !c.Expect.HasExample {
		return
	}
	r := o.Resource
	items, ok := listItems(c.Expect.Example, o.Items)
	if !ok {
		return
	}
	recs := v.st.all(r, v.m)
	_, fetched := v.start.lists[c.Op.ID]
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		idx := -1
		for j, rec := range recs {
			if rec != nil && v.matches(r, rec, obj) {
				idx = j
				break
			}
		}
		if !slices.ContainsFunc(r.Keys, func(k string) bool { return fieldOf(obj, k) != nil }) && !fetched && i < len(recs) {
			idx = i // an element without keys: the generated list holds the records in order
		}
		if idx < 0 {
			if !fetched && i < len(recs) {
				v.fail(CodeStale, "%s expects element %d with keys the records do not have", c.Name, i)
			}
			continue
		}
		at := v.locate(r, recs[idx])
		for _, k := range sortedKeys(obj) {
			f := r.Field(k)
			if f == "" || v.ignored(c, r, f) || (at.created != nil && containsFold(v.server[at.created], f)) {
				continue
			}
			want := recs[idx][f]
			if want == nil || compare.Equal(spec.Normalize(obj[k]), want) {
				continue
			}
			v.fail(CodeStale, "%s expects element %d %s = %s, but %s has %s at this point%s",
				c.Name, i, k, text(obj[k]), key(r, at), text(want), v.since(r, at))
		}
	}
}

// locate finds where a record of a list comes from, for messages.
func (v *verifier) locate(r *model.Resource, rec Record) ref {
	for i, x := range v.st.records[strings.ToLower(r.Name)] {
		if x != nil && matching(r, []Record{x}, map[string]any(rec)) != nil {
			return ref{idx: i}
		}
	}
	for _, op := range v.st.order {
		if x := v.st.created[op]; x != nil && v.m.Op(op) != nil && v.m.Op(op).Resource == r && matching(r, []Record{x}, map[string]any(rec)) != nil {
			return ref{created: op}
		}
	}
	return ref{}
}

func (v *verifier) since(r *model.Resource, at ref) string {
	if by := v.st.changed[key(r, at)]; by != "" {
		return " (changed by " + by + ")"
	}
	return ""
}

// ignored reports whether apitest leaves a field out of the comparison:
// IgnoreFields, x-apitest-ignore, and readOnly or uuid/date fields, which
// apitest only checks for presence.
func (v *verifier) ignored(c *cases.Case, r *model.Resource, field string) bool {
	if containsFold(v.ignore, field) || containsFold(c.Ignore, field) {
		return true
	}
	ref := r.Schema(field)
	return ref != nil && ref.Value != nil && (ref.Value.ReadOnly || presenceOnly(ref.Value))
}

// responseDTOs are the DTOs the expected response of a case is made of.
func responseDTOs(c *cases.Case) []string {
	if c.Expect.Response == nil {
		return nil
	}
	if _, m := jsonMedia(c.Expect.Response.Content); m != nil {
		return dict.DTOParts(m.Schema)
	}
	return nil
}
