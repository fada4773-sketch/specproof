package contract

import (
	"context"
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/discover"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// maxLearned is how many records are learned per resource.
const maxLearned = 10

// Learn reads records of a running stage with the list operations of the
// resources (GET only, no path parameters) and returns their fields by
// resource; the generator takes realistic values from them. A resource
// whose list fails is left out; the notes say why.
func Learn(ctx context.Context, s *spec.Spec, opt discover.Options) (map[string][]map[string]any, []string) {
	m := model.Detect(s, nil)
	out := map[string][]map[string]any{}
	var notes []string
	for _, r := range m.Resources {
		for _, mo := range r.OpsWith(model.RoleList) {
			if hasPathParam(mo.Op) || hasRequiredQuery(mo.Op) {
				continue
			}
			v, err := discover.Get(ctx, opt, mo.Op.Path)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s: GET %s: %v", r.Name, mo.Op.Path, err))
				break
			}
			for _, el := range listOf(v) {
				if obj, ok := el.(map[string]any); ok {
					out[r.Name] = append(out[r.Name], obj)
				}
				if len(out[r.Name]) == maxLearned {
					break
				}
			}
			break
		}
	}
	return out, notes
}

func hasPathParam(op *spec.Operation) bool {
	for _, p := range op.Params {
		if p.In == openapi3.ParameterInPath {
			return true
		}
	}
	return false
}

func hasRequiredQuery(op *spec.Operation) bool {
	for _, p := range op.Params {
		if p.In == openapi3.ParameterInQuery && p.Required {
			return true
		}
	}
	return false
}

// listOf is the list in an answer: the answer itself, or its one field
// that is a list.
func listOf(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any:
		var found []any
		n := 0
		for _, c := range x {
			if l, ok := c.([]any); ok {
				found = l
				n++
			}
		}
		if n == 1 {
			return found
		}
	}
	return nil
}
