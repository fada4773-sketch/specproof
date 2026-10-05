package cases

import (
	"sort"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// maxDepth stops composition of recursive schemas.
const maxDepth = 12

// defaultBody builds the body of a "default" case (FR-CASE-02): the media
// type's example, the schema's example, or a value composed from the
// examples of the individual properties. missing names the first required
// value without any source, as a JSON pointer.
func defaultBody(media *openapi3.MediaType) (body any, ok bool, missing string) {
	if media.Example != nil {
		return spec.Normalize(media.Example), true, ""
	}
	if media.Schema == nil || media.Schema.Value == nil {
		return nil, false, "/"
	}
	v, ok, missing := compose(media.Schema.Value, "", 0)
	if !ok {
		return nil, false, missing
	}
	return spec.Normalize(v), true, ""
}

func compose(s *openapi3.Schema, ptr string, depth int) (any, bool, string) {
	if depth > maxDepth {
		return nil, false, ptr + " (recursive schema)"
	}
	if v, ok := explicit(s); ok {
		return v, true, ""
	}
	if len(s.AllOf) > 0 {
		merged := map[string]any{}
		for _, part := range s.AllOf {
			if part == nil || part.Value == nil {
				continue
			}
			v, ok, missing := compose(part.Value, ptr, depth+1)
			if !ok {
				return nil, false, missing
			}
			if obj, isObj := v.(map[string]any); isObj {
				for k, val := range obj {
					merged[k] = val
				}
			}
		}
		if obj, ok, missing := composeObject(s, ptr, depth); ok {
			for k, val := range obj {
				merged[k] = val
			}
		} else if missing != "" {
			return nil, false, missing
		}
		return merged, true, ""
	}
	for _, alts := range []openapi3.SchemaRefs{s.OneOf, s.AnyOf} {
		for _, alt := range alts {
			if alt != nil && alt.Value != nil {
				return compose(alt.Value, ptr, depth+1)
			}
		}
	}
	switch {
	case s.Type.Is("object") || (s.Type.IsEmpty() && len(s.Properties) > 0):
		obj, ok, missing := composeObject(s, ptr, depth)
		return obj, ok, missing
	case s.Type.Is("array"):
		if s.Items == nil || s.Items.Value == nil {
			return []any{}, true, ""
		}
		item, ok, missing := compose(s.Items.Value, ptr+"/0", depth+1)
		if !ok {
			// Without an item value an empty array is still valid, unless
			// the schema requires elements.
			if s.MinItems == 0 {
				return []any{}, true, ""
			}
			return nil, false, missing
		}
		return []any{item}, true, ""
	}
	if v, ok := fallback(s); ok {
		return v, true, ""
	}
	if ptr == "" {
		ptr = "/"
	}
	return nil, false, ptr
}

// composeObject composes required properties (always) and optional
// properties that have an explicit example. readOnly properties are left out
// because the server sets them.
func composeObject(s *openapi3.Schema, ptr string, depth int) (map[string]any, bool, string) {
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	names := make([]string, 0, len(s.Properties))
	for n := range s.Properties {
		names = append(names, n)
	}
	sort.Strings(names)
	obj := map[string]any{}
	for _, n := range names {
		ref := s.Properties[n]
		if ref == nil || ref.Value == nil || ref.Value.ReadOnly {
			continue
		}
		child := ptr + "/" + n
		if !required[n] {
			if v, ok := explicit(ref.Value); ok {
				obj[n] = v
			} else if v, ok, _ := compose(ref.Value, child, depth+1); ok && hasExplicitExample(ref.Value, depth+1) {
				obj[n] = v
			}
			continue
		}
		v, ok, missing := compose(ref.Value, child, depth+1)
		if !ok {
			return nil, false, missing
		}
		obj[n] = v
	}
	return obj, true, ""
}

// explicit returns an example written for this schema.
func explicit(s *openapi3.Schema) (any, bool) {
	if s.Example != nil {
		return s.Example, true
	}
	if len(s.Examples) > 0 {
		return s.Examples[0], true
	}
	return nil, false
}

// fallback returns default, const or the first enum value.
func fallback(s *openapi3.Schema) (any, bool) {
	switch {
	case s.Default != nil:
		return s.Default, true
	case s.Const != nil:
		return s.Const, true
	case len(s.Enum) > 0:
		return s.Enum[0], true
	}
	return nil, false
}

// hasExplicitExample reports whether an optional nested schema contains any
// explicit example, so that it is worth including.
func hasExplicitExample(s *openapi3.Schema, depth int) bool {
	if depth > maxDepth {
		return false
	}
	if _, ok := explicit(s); ok {
		return true
	}
	for _, ref := range s.Properties {
		if ref != nil && ref.Value != nil && hasExplicitExample(ref.Value, depth+1) {
			return true
		}
	}
	if s.Items != nil && s.Items.Value != nil {
		return hasExplicitExample(s.Items.Value, depth+1)
	}
	for _, refs := range []openapi3.SchemaRefs{s.AllOf, s.OneOf, s.AnyOf} {
		for _, ref := range refs {
			if ref != nil && ref.Value != nil && hasExplicitExample(ref.Value, depth+1) {
				return true
			}
		}
	}
	return false
}
