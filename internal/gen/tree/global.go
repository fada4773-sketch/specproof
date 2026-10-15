package tree

import (
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

// filters reads the where at the top: "Resource.field: test", or a
// mapping per resource of fields to tests.
func (p *parser) filters(v *yaml.Node) []Filter {
	if v.Kind != yaml.MappingNode {
		p.errf(v, "where must be a mapping: Garden.name: [Rose, Mint], or Garden: {name: [Rose, Mint]}")
		return nil
	}
	var out []Filter
	add := func(res, field string, k, val *yaml.Node) {
		x, err := yamldoc.Decode(val)
		if err != nil {
			p.errf(val, "where.%s.%s: %v", res, field, err)
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
			p.errf(k, "where: %q: Resource.field (Garden.name: [Rose, Mint]), or a mapping per resource", k.Value)
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

// checkFilters reports filters of a resource or field the spec does not
// have.
func checkFilters(s *spec.Spec, t *Tree) []string {
	if len(t.Where) == 0 {
		return nil
	}
	m := model.Detect(s, nil)
	var out []string
	for _, f := range t.Where {
		schema, ok := filterSchema(s, m, f.Resource)
		if !ok {
			out = append(out, fmt.Sprintf("line %d: where %s.%s: %s is no resource or schema of the spec", f.Line, f.Resource, f.Clause.Field, f.Resource))
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
				out = append(out, fmt.Sprintf("line %d: where %s.%s: %s has no field %s (fields: %s)", f.Line, f.Resource, f.Clause.Field, schema, top, strings.Join(names, ", ")))
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
	for _, flt := range f.t.Where {
		if slices.ContainsFunc(names, func(x string) bool { return strings.EqualFold(x, flt.Resource) }) {
			out = append(out, flt.Clause)
			f.filtered[flt.Resource] = true
		}
	}
	f.global[n] = out
	return out
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
