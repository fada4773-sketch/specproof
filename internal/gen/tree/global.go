package tree

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Filter is a condition of the whole file (where at the top of tree.yaml):
// every record of the resource must hold it, at every node that reads one
// (lists, searches, reads of one record, require).
//
//	where:
//	  Garden.name: [Rose, Mint]
//	  Garden:
//	    code: [rose, mint]
type Filter struct {
	// Resource names the resource or its schema (Garden).
	Resource string
	Clause   Clause
	Line     int
}

// filters reads the where or prefer at the top (key): "Resource.field:
// test", or a mapping per resource of fields to tests.
func (p *parser) filters(v *yaml.Node, key string) []Filter {
	if v.Kind != yaml.MappingNode {
		p.errf(v, "%s must be a mapping: Garden.name: [Rose, Mint], or Garden: {name: [Rose, Mint]}", key)
		return nil
	}
	var out []Filter
	add := func(res, field string, k, val *yaml.Node) {
		x, err := yamldoc.Decode(val)
		if err != nil {
			p.errf(val, "%s.%s.%s: %v", key, res, field, err)
			return
		}
		out = append(out, Filter{Resource: res, Clause: Clause{Field: field, Test: x, Note: noteOf(val)}, Line: k.Line})
	}
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, val := v.Content[i], v.Content[i+1]
		if val.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(val.Content); j += 2 {
				add(k.Value, val.Content[j].Value, val.Content[j], val.Content[j+1])
			}
			continue
		}
		res, field, ok := strings.Cut(k.Value, ".")
		if !ok || res == "" || field == "" {
			p.errf(k, "%s: %q: Resource.field (Garden.name: [Rose, Mint]), or a mapping per resource", key, k.Value)
			continue
		}
		add(res, field, k, val)
	}
	return out
}

// filterSchema finds the schema a filter names: the schema of a resource
// of that name, or a schema of the spec by its name.
func filterSchema(s *spec.Spec, m *model.Model, name string) (string, bool) {
	for _, r := range m.Resources {
		if strings.EqualFold(r.Name, name) && len(r.Schemas) > 0 {
			return r.Schemas[0], true
		}
	}
	if s.Doc.Components != nil {
		for k := range s.Doc.Components.Schemas {
			if strings.EqualFold(k, name) {
				return k, true
			}
		}
	}
	return "", false
}

// checkFilters reports where and prefer of a resource or field the spec
// does not have.
func checkFilters(s *spec.Spec, t *Tree) []string {
	if len(t.Where)+len(t.Prefer) == 0 {
		return nil
	}
	m := model.Detect(s, nil)
	var out []string
	for i, f := range append(slices.Clone(t.Where), t.Prefer...) {
		key := "where"
		if i >= len(t.Where) {
			key = "prefer"
		}
		schema, ok := filterSchema(s, m, f.Resource)
		if !ok {
			out = append(out, fmt.Sprintf("line %d: %s %s.%s: %s is no resource or schema of the spec", f.Line, key, f.Resource, f.Clause.Field, f.Resource))
			continue
		}
		ref := s.Doc.Components.Schemas[schema]
		top, _, _ := strings.Cut(f.Clause.Field, ".")
		if ref == nil || ref.Value == nil {
			continue
		}
		if props, _ := dict.Properties(ref.Value); len(props) > 0 {
			if _, found := propertyFoldIn(props, top); !found {
				names := make([]string, 0, len(props))
				for k := range props {
					names = append(names, k)
				}
				slices.Sort(names)
				out = append(out, fmt.Sprintf("line %d: %s %s.%s: %s has no field %s (fields: %s)", f.Line, key, f.Resource, f.Clause.Field, schema, top, strings.Join(names, ", ")))
			}
		}
	}
	return out
}

// globals are the filters a node's records must hold: those of the
// resource whose records it reads, matched by resource name or schema.
func (f *fetcher) globals(n *Node) []Clause {
	if len(f.t.Where) == 0 {
		return nil
	}
	if c, ok := f.global[n]; ok {
		return c
	}
	out := f.clausesOf(n, f.t.Where, f.filtered)
	f.global[n] = out
	return out
}

// prefers are the prefer clauses of the records of a node.
func (f *fetcher) prefers(n *Node) []Clause {
	if len(f.t.Prefer) == 0 {
		return nil
	}
	if c, ok := f.prefer[n]; ok {
		return c
	}
	res := map[string]bool{}
	out := f.clausesOf(n, f.t.Prefer, res)
	for r := range res {
		f.preferRes[n] = append(f.preferRes[n], r)
	}
	slices.Sort(f.preferRes[n])
	f.prefer[n] = out
	return out
}

// clausesOf are the clauses of the filters that apply to the records of a
// node; used collects the resources applied.
func (f *fetcher) clausesOf(n *Node, filters []Filter, used map[string]bool) []Clause {
	if f.model == nil {
		f.model = model.Detect(f.s, nil)
	}
	// the schema of the records the node reads, and the name of its
	// resource when that schema is one of the resource (the details of a
	// garden are no garden)
	var names []string
	if op := lookupOp(f.s, n.Call); op != nil {
		if dto := itemDTO(op); dto != "" {
			names = append(names, dto)
			if r := resourceOf(f.model, op); r != nil && len(r.Schemas) > 0 && r.Schemas[0] == dto {
				names = append(names, r.Name)
			}
		}
	}
	var out []Clause
	for _, flt := range filters {
		if slices.ContainsFunc(names, func(x string) bool { return strings.EqualFold(x, flt.Resource) }) {
			out = append(out, flt.Clause)
			used[flt.Resource] = true
		}
	}
	return out
}

// preferred reports whether a record holds the prefer of its node.
func (f *fetcher) preferred(n *Node, rec any) bool {
	cs := f.prefers(n)
	for _, c := range cs {
		if !c.holds(rec) {
			return false
		}
	}
	return len(cs) > 0
}

// preferBelow reports whether a prefer applies to the node or a node
// below it.
func (f *fetcher) preferBelow(n *Node) bool {
	if len(f.t.Prefer) == 0 || !n.Enabled() {
		return false
	}
	if v, ok := f.preferSub[n]; ok {
		return v
	}
	v := len(f.prefers(n)) > 0
	for _, c := range n.Each {
		v = v || f.preferBelow(c)
	}
	f.preferSub[n] = v
	return v
}

// preferOrder puts first the candidates that are preferred, then those
// below which preferred records are (the moon of the preferred garden),
// more prefers before fewer, then the others; ties keep their order.
func (f *fetcher) preferOrder(ctx context.Context, n *Node, cands []cand, parent *Record) ([]cand, error) {
	score := make([]int, len(cands))
	for i, c := range cands {
		if f.preferred(n, c.value) {
			score[i] = len(f.t.Prefer) + 1
			continue
		}
		found := map[string]bool{}
		if err := f.reach(ctx, n, c.value, parent, 0, found); err != nil {
			return nil, err
		}
		score[i] = len(found)
	}
	idx := make([]int, len(cands))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return score[b] - score[a] })
	out := make([]cand, len(cands))
	for i, j := range idx {
		out[i] = cands[j]
	}
	return out, nil
}

// reach collects the resources of the preferred records below a candidate
// of n: it reads the child nodes that lead to a prefer, up to three levels
// down (answers are cached, the fetch reads them again).
func (f *fetcher) reach(ctx context.Context, n *Node, rec any, parent *Record, depth int, found map[string]bool) error {
	probe := &Record{ID: n.Name + "?", Node: n, Parent: parent, Value: rec}
	for _, c := range n.Each {
		if !f.preferBelow(c) || len(unsetParams(f.t, c)) > 0 {
			continue
		}
		op := lookupOp(f.s, c.Call)
		if op == nil {
			continue
		}
		reqs, err := f.requests(c, op, probe)
		if err != nil {
			continue
		}
		for _, rq := range reqs {
			a, err := f.send(ctx, op.Method, rq.path, rq.query, rq.header, rq.body, rq.hasBody)
			if err != nil {
				return err
			}
			if a.status/100 != 2 {
				continue
			}
			els, isList := listOf(c, op, a.body)
			if !isList {
				els = []any{a.body}
			}
			for _, el := range els {
				if !f.passes(c, el) {
					continue
				}
				if f.preferred(c, el) {
					for _, r := range f.preferRes[c] {
						found[r] = true
					}
				}
				if depth < 2 {
					if err := f.reach(ctx, c, el, probe, depth+1, found); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// unusedPrefers notes the prefer no record of the fetch holds.
func (f *fetcher) unusedPrefers() {
	hit := map[string]bool{}
	for _, r := range f.data.Records {
		if f.preferred(r.Node, r.Value) {
			for _, res := range f.preferRes[r.Node] {
				hit[res] = true
			}
		}
	}
	var done []string
	for _, flt := range f.t.Prefer {
		if hit[flt.Resource] || slices.Contains(done, flt.Resource) {
			continue
		}
		done = append(done, flt.Resource)
		if !f.preferAt[flt.Resource] {
			f.note("prefer %s: no node of the tree reads records of %s; the prefer did nothing", flt.Resource, flt.Resource)
		} else {
			f.note("prefer %s: no record of the stage the tree reaches holds it; the picks are as without (check the values and the where)", flt.Resource)
		}
	}
}

// passes reports whether a record holds the where of its node and the
// where of the file.
func (f *fetcher) passes(n *Node, rec any) bool {
	if !n.Holds(rec) {
		return false
	}
	for _, c := range f.globals(n) {
		if !c.holds(rec) {
			return false
		}
	}
	return true
}

// itemDTO is the schema of one record of an answer: the item of a list.
func itemDTO(op *spec.Operation) string {
	ref := responseRef(op, lowestSuccess(op))
	if ref == nil || ref.Value == nil {
		return ""
	}
	if ptr, ok := listPointer(op); ok {
		switch {
		case ptr == "" && ref.Value.Items != nil:
			ref = ref.Value.Items
		case ptr != "":
			props, _ := dict.Properties(ref.Value)
			for k, v := range props {
				if strings.EqualFold(k, strings.TrimPrefix(ptr, "/")) && v != nil && v.Value != nil && v.Value.Items != nil {
					ref = v.Value.Items
				}
			}
		}
	}
	return dict.DTORef(ref)
}
