package record

import (
	"slices"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// generatedSchemaMark is the comment at a schema example record generated
// because no entry holds a value of that schema; only an example with this
// comment is replaced by a recorded value later.
const generatedSchemaMark = "apitest-gen record: generated example"

// Priorities of the values found for a schema: answers before requests (a
// request leaves out the readOnly fields), sent entries before ignored ones.
const (
	fromResponse = iota
	fromRequest
	fromIgnoredResponse
	fromIgnoredRequest
)

// candidate is a value an entry holds at a schema of the components.
type candidate struct {
	value any
	prio  int
}

// collect notes the values v holds at the named schemas of the components,
// following the schema node n through $refs, properties, items,
// additionalProperties and allOf/oneOf/anyOf.
func (w *writer) collect(n *yaml.Node, v any, prio, depth int) {
	if n == nil || v == nil || depth > 32 {
		return
	}
	if name := componentName(yamldoc.Ref(n)); name != "" {
		w.found[name] = append(w.found[name], candidate{v, prio})
	}
	n, err := w.doc.Resolve(n)
	if err != nil || n == nil {
		return
	}
	for _, k := range []string{"allOf", "oneOf", "anyOf"} {
		if l := yamldoc.Get(n, k); l != nil && l.Kind == yaml.SequenceNode {
			for _, part := range l.Content {
				w.collect(part, v, prio, depth+1)
			}
		}
	}
	switch v := v.(type) {
	case map[string]any:
		props := yamldoc.Get(n, "properties")
		extra := yamldoc.Get(n, "additionalProperties")
		if extra != nil && extra.Kind != yaml.MappingNode {
			extra = nil
		}
		for _, k := range sortedKeys(v) {
			if p := yamldoc.Get(props, k); p != nil {
				w.collect(p, v[k], prio, depth+1)
			} else if extra != nil {
				w.collect(extra, v[k], prio, depth+1)
			}
		}
	case []any:
		if items := yamldoc.Get(n, "items"); items != nil {
			for _, x := range v {
				w.collect(items, x, prio, depth+1)
			}
		}
	}
}

// componentName is the schema a local $ref names: "#/components/schemas/Dock"
// (or "#/definitions/Dock" in Swagger 2.0) → "Dock".
func componentName(ref string) string {
	for _, prefix := range []string{"#/components/schemas/", "#/definitions/"} {
		if name, ok := strings.CutPrefix(ref, prefix); ok && !strings.Contains(name, "/") {
			return strings.ReplaceAll(strings.ReplaceAll(name, "~1", "/"), "~0", "~")
		}
	}
	return ""
}

// schemas is the mapping of the named schemas: components/schemas, or
// definitions in Swagger 2.0.
func (w *writer) schemas() *yaml.Node {
	if n := yamldoc.Path(w.doc.Root, "components", "schemas"); n != nil {
		return n
	}
	return yamldoc.Get(w.doc.Root, "definitions")
}

// schemaExamples gives every schema of the components an example, so a
// linter that wants one per schema is content: the first value an entry
// holds at the schema that fits it (answers before requests, sent entries
// before ignored ones), else a value generated from the schema. An example
// the schema already has stays as long as it fits, unless record generated
// it and an entry now holds a value. A schema whose example apitest reads
// (readByApitest) is left alone, so the cases stay what they are.
func (w *writer) schemaExamples(s *spec.Spec, v *spec.Validator) {
	schemas := w.schemas()
	if schemas == nil || s.Doc.Components == nil {
		return
	}
	read := w.readByApitest()
	var generated, failed, left []string
	for _, name := range yamldoc.Keys(schemas) {
		node := yamldoc.Get(schemas, name)
		if read[name] {
			if yamldoc.Get(node, "example") == nil && yamldoc.Get(node, "examples") == nil {
				left = append(left, name)
			}
			continue
		}
		ref := s.Doc.Components.Schemas[name]
		if node == nil || node.Kind != yaml.MappingNode || yamldoc.Ref(node) != "" || ref == nil || ref.Value == nil {
			continue // a $ref: its target gets the example
		}
		schema := ref.Value
		cur := yamldoc.Get(node, "example")
		if cur == nil && yamldoc.Get(node, "examples") != nil {
			continue // OpenAPI 3.1: examples of the schema
		}
		ownGenerated := cur != nil && marked(node, "example")
		curFits := cur != nil && firstViolation(v, schema, decode(cur), spec.ModePlain) == ""
		if curFits && !ownGenerated {
			continue
		}
		val, ok := w.recorded(name, schema, v)
		mark := ""
		if !ok {
			if curFits {
				continue // generated before, still nothing recorded
			}
			gen, msg := w.generate(schema, "schema."+name, "", spec.ModePlain, v)
			if msg != "" {
				failed = append(failed, name+" ("+msg+")")
				continue
			}
			val, mark = gen, generatedSchemaMark
			generated = append(generated, name)
		}
		if cur != nil && compare.Equal(decode(cur), spec.Normalize(val)) && ownGenerated == (mark != "") {
			continue
		}
		n := valueNode(val)
		compact(n)
		if yamldoc.SetNode(node, "example", n) != nil {
			continue
		}
		n.LineComment = mark // SetNode keeps the comment of the old example
		unmark(node, "example")
		w.changed = true
		w.res.SchemaExamples++
	}
	if len(generated) > 0 {
		w.res.note(CodeGenerated, "components", "no entry holds a value of these schemas; their example is generated from the schema: %s", strings.Join(generated, ", "))
	}
	if len(failed) > 0 {
		w.res.note(CodeGenerated, "components", "these schemas get no example; give them one in the spec: %s", strings.Join(failed, ", "))
	}
	if len(left) > 0 {
		w.res.note(CodeGenerated, "components", "these schemas get no example, because apitest would use it for a parameter or request body without example and the cases would change; give those an entry in the record file: %s", strings.Join(left, ", "))
	}
}

// marked reports whether record generated the example at key: the mark
// is at the value, or at the key when the value spans several lines.
func marked(n *yaml.Node, key string) bool {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return strings.Contains(n.Content[i].LineComment+n.Content[i+1].LineComment, generatedSchemaMark)
		}
	}
	return false
}

// unmark removes the mark from the key of an example.
func unmark(n *yaml.Node, key string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i]; k.Value == key && strings.Contains(k.LineComment, generatedSchemaMark) {
			k.LineComment = ""
		}
	}
}

// readByApitest are the schemas of the components whose example apitest
// reads: the schema of a parameter without example (its value when nothing
// else gives one) and every schema a request body without example is
// composed from.
func (w *writer) readByApitest() map[string]bool {
	read := map[string]bool{}
	paths := yamldoc.Get(w.doc.Root, "paths")
	for _, p := range yamldoc.Keys(paths) {
		item, err := w.doc.Resolve(yamldoc.Get(paths, p))
		if err != nil {
			continue
		}
		for _, m := range yamldoc.Keys(item) {
			op := yamldoc.Get(item, m)
			if m == "parameters" {
				op = item
			} else if !slices.Contains(methods, m) {
				continue
			}
			if list := yamldoc.Get(op, "parameters"); list != nil && list.Kind == yaml.SequenceNode {
				for _, e := range list.Content {
					t, err := w.doc.Resolve(e)
					if err != nil || yamldoc.Get(t, "example") != nil {
						continue
					}
					in := yamldoc.Get(t, "in")
					w.reads(yamldoc.Get(t, "schema"), read, in != nil && in.Value == "body", map[*yaml.Node]bool{})
				}
			}
			rb, err := w.doc.Resolve(yamldoc.Get(op, "requestBody"))
			if err != nil {
				continue
			}
			content := yamldoc.Get(rb, "content")
			for _, mt := range yamldoc.Keys(content) {
				media, err := w.doc.Resolve(yamldoc.Get(content, mt))
				if err != nil || yamldoc.Get(media, "example") != nil || yamldoc.Get(media, "examples") != nil {
					continue
				}
				w.reads(yamldoc.Get(media, "schema"), read, true, map[*yaml.Node]bool{})
			}
		}
	}
	return read
}

var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// reads marks the named schemas n refers to: the chain of $refs, and with
// deep everything below it too.
func (w *writer) reads(n *yaml.Node, read map[string]bool, deep bool, seen map[*yaml.Node]bool) {
	if n == nil || seen[n] {
		return
	}
	seen[n] = true
	if ref := yamldoc.Ref(n); ref != "" {
		if name := componentName(ref); name != "" {
			read[name] = true
			w.reads(yamldoc.Get(w.schemas(), name), read, deep, seen)
		}
		return
	}
	if !deep {
		return
	}
	for i, c := range n.Content {
		if n.Kind == yaml.MappingNode && i%2 == 0 {
			continue // a key
		}
		w.reads(c, read, deep, seen)
	}
}

// recorded is the first value an entry holds at a schema that fits it.
// An object keeps only the properties the schema declares, if that fits:
// the part of a composed schema (allOf) gets its own fields, not those of
// the others.
func (w *writer) recorded(name string, schema *openapi3.Schema, v *spec.Validator) (any, bool) {
	cands := slices.Clone(w.found[name])
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].prio < cands[j].prio })
	for _, c := range cands {
		for _, val := range []any{own(c.value, schema), c.value} {
			if val != nil && firstViolation(v, schema, val, spec.ModePlain) == "" {
				return val, true
			}
		}
	}
	return nil, false
}

// own is an object with only the properties the schema declares, its own
// and those of its allOf parts; nil if the value is no object or the
// schema declares none.
func own(v any, schema *openapi3.Schema) any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	declared := map[string]bool{}
	declaredProperties(schema, declared, 0)
	if len(declared) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, x := range m {
		if declared[k] {
			out[k] = x
		}
	}
	return out
}

func declaredProperties(s *openapi3.Schema, declared map[string]bool, depth int) {
	if s == nil || depth > 32 {
		return
	}
	for k := range s.Properties {
		declared[k] = true
	}
	for _, part := range s.AllOf {
		if part != nil {
			declaredProperties(part.Value, declared, depth+1)
		}
	}
}
