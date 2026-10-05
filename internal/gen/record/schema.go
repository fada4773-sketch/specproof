package record

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// tableName is the table of a DTO name in lower case: DockRead, DockDto
// and Dock share one table (one sequence). A DockDetail is a table of its
// own.
func tableName(dto string) string {
	for changed := true; changed; {
		changed = false
		for _, s := range tableSuffixes {
			if len(dto) > len(s) && strings.HasSuffix(dto, s) {
				dto, changed = strings.TrimSuffix(dto, s), true
			}
		}
	}
	return strings.ToLower(dto)
}

var tableSuffixes = []string{"Read", "Update", "Upsert", "Create", "Write", "Patch", "Put", "Post", "Dto", "DTO",
	"Request", "Response", "View", "Model", "Input", "Output", "Summary", "Base", "Data", "Body", "Payload", "Entity"}

// namer names the table of a DTO with the paths of the spec: a DTO whose
// name is a resource of a path followed by Read or Part is a view of that
// resource (DockReadConfiguration, DockPartRead → dock); DockDetail stays a
// table of its own.
type namer map[string]bool

func newNamer(s *spec.Spec) namer {
	n := namer{}
	for _, op := range s.Ops {
		for _, seg := range strings.Split(strings.Trim(op.Path, "/"), "/") {
			if seg == "" || strings.HasPrefix(seg, "{") {
				continue
			}
			l := strings.ToLower(seg)
			n[l] = true
			n[strings.TrimSuffix(l, "s")] = true
		}
	}
	return n
}

// table is the table of a DTO name.
func (n namer) table(dto string) string {
	base := tableName(dto)
	if n[base] {
		return base
	}
	best := ""
	for i := 1; i < len(dto); i++ {
		rest := dto[i:]
		if !word(rest, "Read") && !word(rest, "Part") {
			continue
		}
		if p := strings.ToLower(dto[:i]); n[p] && len(p) > len(best) {
			best = p
		}
	}
	if best != "" {
		return best
	}
	return base
}

// word reports whether s starts with the word w: Read in ReadDetail, not
// in Readiness.
func word(s, w string) bool {
	if !strings.HasPrefix(s, w) {
		return false
	}
	return len(s) == len(w) || (s[len(w)] >= 'A' && s[len(w)] <= 'Z')
}

// of is the table of a schema, "" for an inline schema.
func (n namer) of(ref *openapi3.SchemaRef) string {
	if dto := dict.DTORef(ref); dto != "" {
		return n.table(dto)
	}
	return ""
}

// listOf returns the elements of a list response and their schema: the
// array itself, or the one array of a page object ("items", "content", …).
// key is the field of the page, "" for a plain array.
func listOf(ref *openapi3.SchemaRef, v any) (items *openapi3.SchemaRef, elems []any, key string, ok bool) {
	items, key, ok = listShape(ref)
	if !ok {
		return nil, nil, "", false
	}
	if key == "" {
		elems, ok = v.([]any)
		return items, elems, "", ok
	}
	obj, _ := v.(map[string]any)
	elems, ok = obj[key].([]any)
	return items, elems, key, ok
}

// listShape reports whether a schema is a list: an array, or a page object
// with one array.
func listShape(ref *openapi3.SchemaRef) (items *openapi3.SchemaRef, key string, ok bool) {
	if ref == nil || ref.Value == nil {
		return nil, "", false
	}
	s := ref.Value
	if value.Type(s) == "array" {
		return s.Items, "", s.Items != nil
	}
	props, _ := dict.Properties(s)
	var arrays []string
	for k, p := range props {
		if p.Value != nil && value.Type(p.Value) == "array" {
			arrays = append(arrays, k)
		}
	}
	sort.Strings(arrays)
	if len(arrays) != 1 || !model.Page(dict.DTORef(ref), arrays[0]) || props[arrays[0]].Value.Items == nil {
		return nil, "", false
	}
	return props[arrays[0]].Value.Items, arrays[0], true
}

// lookup returns every value a dotted field path reaches; names ignore
// case, and in a list every element counts.
func lookup(v any, path string) []any {
	cur := []any{v}
	for _, seg := range strings.Split(path, ".") {
		var next []any
		var walk func(x any)
		walk = func(x any) {
			switch t := x.(type) {
			case []any:
				for _, e := range t {
					walk(e)
				}
			case map[string]any:
				if k := fieldName(t, seg); k != "" {
					next = append(next, t[k])
				}
			}
		}
		for _, x := range cur {
			walk(x)
		}
		cur = next
	}
	return cur
}

// fieldName returns the key of an object that matches name, ignoring case.
func fieldName(o map[string]any, name string) string {
	if _, ok := o[name]; ok {
		return name
	}
	for k := range o {
		if strings.EqualFold(k, name) {
			return k
		}
	}
	return ""
}

// filled reports a value that is not null, "", [] or {}.
func filled(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

// same compares two values; a number also equals its text ("7" and 7).
func same(a, b any) bool {
	a, b = spec.Normalize(a), spec.Normalize(b)
	if compare.Equal(a, b) {
		return true
	}
	return scalar(a) && scalar(b) && text(a) == text(b)
}

func scalar(v any) bool {
	switch v.(type) {
	case string, json.Number, bool:
		return true
	}
	return false
}

func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// passes checks an answer: every mandatory field is filled somewhere, every
// equal field has its value somewhere. It returns the first reason it
// fails, "" if it passes.
func passes(v any, equal map[string]any, mandatory []string) string {
	for _, f := range mandatory {
		if strings.TrimSpace(f) == "" {
			continue
		}
		ok := false
		for _, x := range lookup(v, f) {
			ok = ok || filled(x)
		}
		if !ok {
			return f + " empty"
		}
	}
	for _, f := range sortedKeys(equal) {
		ok := false
		for _, x := range lookup(v, f) {
			ok = ok || same(x, equal[f])
		}
		if !ok {
			b, _ := json.Marshal(equal[f])
			return fmt.Sprintf("%s is not %s", f, b)
		}
	}
	return ""
}

// idOf returns the id field of an object and its value; ids are numbers
// the sequence of the table assigns.
func idOf(o map[string]any) (string, any) {
	k := fieldName(o, "id")
	if k == "" {
		return "", nil
	}
	if _, ok := o[k].(json.Number); !ok {
		return "", nil
	}
	return k, o[k]
}

// refTable returns the table a field like "storeId" refers to, "" for other
// fields.
func refTable(field string) string {
	n := len(field)
	if n <= 2 {
		return ""
	}
	switch suffix := field[n-2:]; {
	case suffix == "Id", suffix == "ID":
	case suffix == "id" && field[n-3] == '_':
	default:
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(field[:n-2], "_"))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fingerprint is a hash of everything of an operation an example depends
// on: its parameters, its request body and its 2xx responses, with every
// $ref resolved, so a changed DTO changes it.
func fingerprint(op *spec.Operation) string {
	d := map[string]any{"method": op.Method, "path": op.Path}
	var params []any
	for _, p := range op.Params {
		params = append(params, map[string]any{"name": p.Name, "in": p.In, "required": p.Required, "schema": dump(p.Schema, map[*openapi3.Schema]bool{})})
	}
	d["params"] = params
	if rb := op.Op.RequestBody; rb != nil && rb.Value != nil {
		d["body"] = content(rb.Value.Content)
	}
	if op.Op.Responses != nil {
		res := map[string]any{}
		for code, r := range op.Op.Responses.Map() {
			if strings.HasPrefix(code, "2") && r.Value != nil {
				res[code] = content(r.Value.Content)
			}
		}
		d["responses"] = res
	}
	b, _ := json.Marshal(d)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func content(c openapi3.Content) any {
	out := map[string]any{}
	for mt, m := range c {
		if m != nil {
			out[mt] = dump(m.Schema, map[*openapi3.Schema]bool{})
		}
	}
	return out
}

// dump writes a schema with its references resolved; a cycle ends at the
// name of the DTO.
func dump(ref *openapi3.SchemaRef, seen map[*openapi3.Schema]bool) any {
	if ref == nil || ref.Value == nil {
		return nil
	}
	s := ref.Value
	if seen[s] {
		return map[string]any{"cycle": ref.Ref}
	}
	seen[s] = true
	defer delete(seen, s)
	out := map[string]any{"type": s.Type.Slice(), "format": s.Format, "enum": s.Enum, "nullable": s.Nullable,
		"readOnly": s.ReadOnly, "writeOnly": s.WriteOnly, "pattern": s.Pattern, "minLength": s.MinLength, "maxLength": s.MaxLength,
		"min": s.Min, "max": s.Max, "minItems": s.MinItems, "maxItems": s.MaxItems}
	req := append([]string(nil), s.Required...)
	sort.Strings(req)
	out["required"] = req
	props := map[string]any{}
	for k, p := range s.Properties {
		props[k] = dump(p, seen)
	}
	out["properties"] = props
	out["items"] = dump(s.Items, seen)
	for name, list := range map[string]openapi3.SchemaRefs{"allOf": s.AllOf, "oneOf": s.OneOf, "anyOf": s.AnyOf} {
		var parts []any
		for _, p := range list {
			parts = append(parts, dump(p, seen))
		}
		out[name] = parts
	}
	if s.AdditionalProperties.Schema != nil {
		out["additionalProperties"] = dump(s.AdditionalProperties.Schema, seen)
	}
	return out
}
