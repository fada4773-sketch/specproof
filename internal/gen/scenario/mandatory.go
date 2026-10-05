package scenario

import (
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// A mandatory field is a dotted path into a fetched element:
// "author", "Book.Author", "BookDetail.Author" or "ReadDTO.BookDetail.Author".
// A segment is a field name (case-insensitive), the name of a DTO or of its
// resource (Book for BookRead): the path then
// continues at the object of that DTO, the element itself or the first one
// found below it. Inside a list one element with a value is enough.

// hasAll reports whether every path has a value in item: not missing, not
// null, not "", not an empty list and not an empty object.
func hasAll(item any, ref *openapi3.SchemaRef, paths [][]string) bool {
	for _, p := range paths {
		if !walk(item, ref, p, 0, nonEmpty) {
			return false
		}
	}
	return true
}

// equals reports whether the value at the path is want; values compare as
// JSON values, a number also equals its text ("7" and 7).
func equals(item any, ref *openapi3.SchemaRef, segs []string, want any) bool {
	return walk(item, ref, segs, 0, func(v any) bool {
		if v == nil || want == nil {
			return v == nil && want == nil
		}
		return compare.Equal(spec.Normalize(v), spec.Normalize(want)) || params.Scalar(v) == params.Scalar(want)
	})
}

// valueAt returns the first single value with a value at the path (not an
// object, not a list, not null, not ""), or nil.
func valueAt(item any, ref *openapi3.SchemaRef, segs []string) any {
	var got any
	walk(item, ref, segs, 0, func(v any) bool {
		switch v.(type) {
		case map[string]any, []any:
			return false
		}
		if nonEmpty(v) {
			got = v
			return true
		}
		return false
	})
	return got
}

func nonEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// walk follows the path through v and reports whether leaf holds for the
// value at its end. In a list one element is enough.
func walk(v any, ref *openapi3.SchemaRef, segs []string, depth int, leaf func(any) bool) bool {
	if depth > 30 {
		return false
	}
	if len(segs) == 0 {
		if list, ok := v.([]any); ok && len(list) > 0 && !leaf(list) {
			for _, el := range list {
				if leaf(el) {
					return true
				}
			}
			return false
		}
		return leaf(v)
	}
	if list, ok := v.([]any); ok {
		var items *openapi3.SchemaRef
		if ref != nil && ref.Value != nil {
			items = ref.Value.Items
		}
		for _, el := range list {
			if walk(el, items, segs, depth+1, leaf) {
				return true
			}
		}
		return false
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return false
	}
	seg := segs[0]
	for k, child := range obj {
		if strings.EqualFold(k, seg) {
			return walk(child, property(ref, k), segs[1:], depth+1, leaf)
		}
	}
	if isDTO(ref, seg) {
		return walk(v, ref, segs[1:], depth+1, leaf)
	}
	// the DTO further down: every field that holds it or contains it
	for k, child := range obj {
		c := property(ref, k)
		if c != nil && contains(c, seg, 0) && walk(child, c, segs, depth+1, leaf) {
			return true
		}
	}
	return false
}

// resolvable reports whether a path can exist in a schema at all, to catch
// typos before any element is rejected.
func resolvable(ref *openapi3.SchemaRef, segs []string, depth int) bool {
	if len(segs) == 0 {
		return true
	}
	if ref == nil || ref.Value == nil || depth > 30 {
		return false
	}
	if ref.Value.Items != nil {
		return resolvable(ref.Value.Items, segs, depth+1)
	}
	props, _ := dict.Properties(ref.Value)
	for k, c := range props {
		if strings.EqualFold(k, segs[0]) {
			return resolvable(c, segs[1:], depth+1)
		}
	}
	if isDTO(ref, segs[0]) {
		return resolvable(ref, segs[1:], depth+1)
	}
	for _, c := range props {
		if contains(c, segs[0], 0) && resolvable(c, segs, depth+1) {
			return true
		}
	}
	return false
}

// isDTO reports whether ref is the DTO name, directly or as a part of its
// allOf (BookRead = allOf [BookUpdate, …] is a BookUpdate too).
func isDTO(ref *openapi3.SchemaRef, name string) bool {
	if ref == nil {
		return false
	}
	// the DTO name (BookRead) or its resource name (Book): a list of
	// BookRead holds Books, so "Book.Author" is the Author of each element
	for _, dto := range []string{dict.DTORef(ref), strings.TrimPrefix(ref.Ref, "#/components/schemas/")} {
		if dto != "" && slices.ContainsFunc(model.Stems(dto), func(s string) bool { return strings.EqualFold(s, name) }) {
			return true
		}
	}
	if ref.Value != nil {
		for _, part := range ref.Value.AllOf {
			if isDTO(part, name) {
				return true
			}
		}
	}
	return false
}

// contains reports whether the DTO name is ref itself or somewhere below it.
func contains(ref *openapi3.SchemaRef, name string, depth int) bool {
	if ref == nil || ref.Value == nil || depth > 8 {
		return false
	}
	if isDTO(ref, name) {
		return true
	}
	if ref.Value.Items != nil {
		return contains(ref.Value.Items, name, depth+1)
	}
	props, _ := dict.Properties(ref.Value)
	for _, c := range props {
		if contains(c, name, depth+1) {
			return true
		}
	}
	return false
}

// property returns the schema of field k of ref, ignoring case.
func property(ref *openapi3.SchemaRef, k string) *openapi3.SchemaRef {
	if ref == nil || ref.Value == nil {
		return nil
	}
	props, _ := dict.Properties(ref.Value)
	if c := props[k]; c != nil {
		return c
	}
	for name, c := range props {
		if strings.EqualFold(name, k) {
			return c
		}
	}
	return nil
}

// itemRef is the schema of one element of o's response, with its $ref.
func itemRef(o *model.Op) *openapi3.SchemaRef {
	ref := successSchema(o.Op)
	if ref == nil || ref.Value == nil {
		return nil
	}
	if o.Role != model.RoleList {
		return ref
	}
	if o.Items != "" {
		p := property(ref, strings.TrimPrefix(o.Items, "/"))
		if p == nil || p.Value == nil {
			return nil
		}
		return p.Value.Items
	}
	return ref.Value.Items
}
