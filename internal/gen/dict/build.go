package dict

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// schemaRef is the prefix of references to DTOs.
const schemaRef = "#/components/schemas/"

// Codes of the notes Build reports.
const (
	CodeValueNew       = "VALUE_NEW"       // a value was generated
	CodeValueReused    = "VALUE_REUSED"    // a value was taken from a field with the same name
	CodeValueInvalid   = "VALUE_INVALID"   // a kept value no longer fits its schema
	CodeValueRepaired  = "VALUE_REPAIRED"  // an invalid value was regenerated (Repair)
	CodePatternPending = "PATTERN_PENDING" // no value: the pattern is not solved yet
	CodeNoValue        = "NO_VALUE"        // no value for another reason, e.g. a free object
	CodeParamConflict  = "PARAM_CONFLICT"  // a parameter has different schemas in different operations
	CodeTypeConflict   = "TYPE_CONFLICT"   // "type: object" with an allOf of an enum: no value can be valid
	CodeRemoved        = "REMOVED"         // a node of the old dictionary is no longer in the spec
)

// Note is one message of Build.
type Note struct {
	Code    string
	Where   string // dictionary path, e.g. "schemas.Garden.Name"
	Message string
}

// Options control Build.
type Options struct {
	Seed uint64
	// Repair regenerates values that no longer fit their schema; without
	// it they are kept and reported.
	Repair bool
}

// Stats counts what Build did.
type Stats struct {
	DTOs, Fields, Parameters int
	New, Reused, Kept        int
	Invalid, Repaired        int
	Missing                  int
}

// Build derives the dictionary from the spec. Constraints always come from
// the spec; values of old are kept where the same path exists and the value
// still fits. New fields take the value of a field with the same name
// (ignoring case) if it fits, otherwise a generated one, so equal names get
// equal values across DTOs and parameters.
func Build(s *spec.Spec, old *Dict, opt Options) (*Dict, []Note, Stats) {
	if old == nil {
		old = New()
	}
	b := &builder{
		opt:       opt,
		validator: spec.NewValidator(),
		byName:    map[string][]any{},
		out:       New(),
	}
	b.remember(old)

	if c := s.Doc.Components; c != nil {
		for _, name := range sorted(c.Schemas) {
			ref := c.Schemas[name]
			if ref == nil || ref.Value == nil {
				continue
			}
			b.stats.DTOs++
			if strings.HasPrefix(ref.Ref, schemaRef) {
				b.out.Schemas[name] = &Node{Ref: strings.TrimPrefix(ref.Ref, schemaRef)}
				continue
			}
			b.out.Schemas[name] = b.node(ref.Value, "schemas."+name, name, old.Schemas[name], false, 0)
		}
	}

	for _, op := range s.Ops {
		for _, p := range op.Params {
			if p.Schema == nil || p.Schema.Value == nil {
				continue // content-based parameters are serialized objects
			}
			key := p.In + "." + p.Name
			if have, ok := b.out.Parameters[key]; ok {
				if fresh := meta(p.Schema.Value); !sameConstraints(have, fresh) {
					b.note(CodeParamConflict, "parameters."+key, fmt.Sprintf("%s uses another schema for this parameter than an earlier operation; the first one is kept", op.ID))
				}
				continue
			}
			b.stats.Parameters++
			b.out.Parameters[key] = b.leafOrNode(p.Schema.Value, "parameters."+key, p.Name, old.Parameters[key], p.Required)
		}
	}

	// values of generic path parameters are kept as long as the path exists
	paths := map[string]bool{}
	for _, op := range s.Ops {
		paths[op.Path] = true
	}
	for _, p := range sorted(old.Paths) {
		if paths[p] {
			b.out.Paths[p] = old.Paths[p]
		}
	}

	b.assign()

	for _, name := range sorted(old.Schemas) {
		if b.out.Schemas[name] == nil {
			b.note(CodeRemoved, "schemas."+name, "the DTO is no longer in the spec; its values are dropped")
		}
	}
	for _, key := range sorted(old.Parameters) {
		if b.out.Parameters[key] == nil {
			b.note(CodeRemoved, "parameters."+key, "the parameter is no longer in the spec; its value is dropped")
		}
	}
	sort.SliceStable(b.notes, func(i, j int) bool { return b.notes[i].Where < b.notes[j].Where })
	// the records are the start state of the examples; apitest-gen reuses
	// them, so a second run gives the same examples
	b.out.Records = old.Records
	return b.out, b.notes, b.stats
}

type builder struct {
	// sites are the leaves waiting for a value, see assign
	sites     []site
	opt       Options
	validator *spec.Validator
	// byName holds the known values per lower-case field name, in the
	// order they were found, for consistent values across DTOs.
	byName map[string][]any
	out    *Dict
	notes  []Note
	stats  Stats
}

func (b *builder) note(code, where, msg string) {
	b.notes = append(b.notes, Note{Code: code, Where: where, Message: msg})
}

// remember registers the values of the old dictionary by field name, so a
// value set by hand also reaches new fields with the same name.
func (b *builder) remember(old *Dict) {
	var walk func(name string, n *Node)
	walk = func(name string, n *Node) {
		if n == nil {
			return
		}
		if n.Value != nil {
			b.add(name, n.Value)
		}
		for _, k := range sorted(n.Properties) {
			walk(k, n.Properties[k])
		}
	}
	for _, name := range sorted(old.Schemas) {
		walk(name, old.Schemas[name])
	}
	for _, key := range sorted(old.Parameters) {
		_, pname, _ := strings.Cut(key, ".")
		walk(pname, old.Parameters[key])
	}
}

func (b *builder) add(name string, v any) {
	k := strings.ToLower(name)
	b.byName[k] = append(b.byName[k], v)
}

// node builds the node of schema s at path. old is the node at the same
// path in the previous dictionary.
func (b *builder) node(s *openapi3.Schema, path, name string, old *Node, required bool, depth int) *Node {
	if depth > 20 {
		return &Node{}
	}
	props, req := Properties(s)
	if len(props) == 0 && s.Items == nil {
		return b.leafOrNode(s, path, name, old, required)
	}
	n := meta(s)
	n.Required = required
	if len(props) > 0 {
		n.Properties = map[string]*Node{}
		for _, k := range sorted(props) {
			ref := props[k]
			var oldChild *Node
			if old != nil {
				oldChild = old.Properties[k]
			}
			n.Properties[k] = b.child(ref, path+"."+k, k, oldChild, slices.Contains(req, k), depth)
		}
	}
	if s.Items != nil && len(props) == 0 {
		var oldItems *Node
		if old != nil {
			oldItems = old.Items
		}
		if target := DTORef(s.Items); target != "" {
			n.Items = &Node{Ref: target}
		} else if items := s.Items.Value; items != nil {
			if p, _ := Properties(items); len(p) > 0 {
				n.Items = b.node(items, path+"[]", name, oldItems, false, depth+1)
			} else {
				// an array of primitives is one leaf with an array value
				return b.leafOrNode(s, path, name, old, required)
			}
		}
	}
	return n
}

// child builds a property: a reference to another DTO stays a reference.
func (b *builder) child(ref *openapi3.SchemaRef, path, name string, old *Node, required bool, depth int) *Node {
	b.stats.Fields++
	if target := DTORef(ref); target != "" {
		n := &Node{Ref: target, Required: required}
		n.ReadOnly, n.WriteOnly = ref.Value.ReadOnly, ref.Value.WriteOnly
		return n
	}
	if ref.Value == nil {
		return &Node{Required: required}
	}
	return b.node(ref.Value, path, name, old, required, depth+1)
}

// DTORef returns the DTO a property refers to: a direct $ref, or an allOf
// with one $ref and nothing else, as generators write it to add a
// description or readOnly. Primitive targets such as enums are no DTOs.
func DTORef(ref *openapi3.SchemaRef) string {
	if ref == nil || ref.Value == nil {
		return ""
	}
	if _, isPrimitive := Primitive(ref.Value); isPrimitive {
		return ""
	}
	if strings.HasPrefix(ref.Ref, schemaRef) {
		return strings.TrimPrefix(ref.Ref, schemaRef)
	}
	// allOf with one $ref and otherwise only extensions or descriptions,
	// e.g. [{$ref: X}, {x-go-type: X}]
	s := ref.Value
	if len(s.Properties) > 0 || len(s.AllOf) == 0 {
		return ""
	}
	target := ""
	for _, part := range s.AllOf {
		switch {
		case strings.HasPrefix(part.Ref, schemaRef) && target == "":
			target = strings.TrimPrefix(part.Ref, schemaRef)
		case part.Ref == "" && blank(part.Value):
		default:
			return ""
		}
	}
	return target
}

// blank reports whether s constrains nothing, e.g. a schema with only
// extensions or a description.
func blank(s *openapi3.Schema) bool {
	return s == nil || (s.Type.IsEmpty() && len(s.Properties) == 0 && s.Items == nil && len(s.Enum) == 0 &&
		len(s.AllOf) == 0 && len(s.OneOf) == 0 && len(s.AnyOf) == 0 && s.Format == "" && s.Pattern == "")
}

// site is a leaf that gets its value in assign.
type site struct {
	node   *Node
	schema *openapi3.Schema
	path   string
	name   string
	old    *Node
}

// leafOrNode builds a leaf; its value is assigned later.
func (b *builder) leafOrNode(s *openapi3.Schema, path, name string, old *Node, required bool) *Node {
	n := meta(s)
	n.Required = required
	b.sites = append(b.sites, site{node: n, schema: s, path: path, name: name, old: old})
	return n
}

// assign gives every leaf its value. Leaves with the strictest constraints
// come first (pattern, then enum or format, then the rest), so a value
// shared by name is one that also fits the strict places: a path parameter
// planetCode with ^[a-z]+$ and a field PlanetCode without pattern get
// the same value.
func (b *builder) assign() {
	weight := func(s *openapi3.Schema) int {
		switch {
		case patternOf(s) != "":
			return 0
		case len(s.Enum) > 0 || s.Format != "":
			return 1
		}
		return 2
	}
	sort.SliceStable(b.sites, func(i, j int) bool { return weight(b.sites[i].schema) < weight(b.sites[j].schema) })
	for _, st := range b.sites {
		st.node.Value = b.valueFor(st.schema, st.path, st.name, st.old)
	}
}

// shared reports whether fields of this name share one value: compound
// names like PlanetCode do, generic ones like Name, Id or Version mean
// something else in every DTO.
func shared(name string) bool { return len(value.Words(name)) >= 2 }

func (b *builder) valueFor(s *openapi3.Schema, path, name string, old *Node) any {
	if old != nil && old.Value != nil {
		if b.valid(s, old.Value) {
			b.stats.Kept++
			b.add(name, old.Value)
			return old.Value
		}
		if !b.opt.Repair {
			b.stats.Invalid++
			b.note(CodeValueInvalid, path, fmt.Sprintf("the value %s no longer fits the schema; fix it or run with -repair", short(old.Value)))
			return old.Value
		}
		b.stats.Repaired++
		b.note(CodeValueRepaired, path, fmt.Sprintf("the value %s did not fit the schema and was regenerated", short(old.Value)))
	}
	for _, v := range b.byName[strings.ToLower(name)] {
		if !shared(name) {
			break
		}
		if b.valid(s, v) {
			b.stats.Reused++
			b.note(CodeValueReused, path, fmt.Sprintf("takes %s from another field named %q", short(v), name))
			return v
		}
	}
	r := value.Generate(s, value.Context{Seed: b.opt.Seed, Path: path, Name: name, Parent: parentOf(path)})
	switch {
	case r.OK && b.valid(s, r.Value):
		v := spec.Normalize(r.Value)
		b.stats.New++
		b.add(name, v)
		b.note(CodeValueNew, path, short(v))
		return v
	case r.Reason == value.ReasonPattern || (r.OK && patternOf(s) != ""):
		b.stats.Missing++
		b.note(CodePatternPending, path, fmt.Sprintf("no value for pattern %s yet; set one in defaults.json", patternOf(s)))
	case s.Type.Is("object") && primitiveAllOf(s):
		b.stats.Missing++
		b.note(CodeTypeConflict, path, `the schema says "type: object", but its allOf is a primitive (e.g. an enum), so no value can be valid; remove "type: object" in the spec`)
	default:
		b.stats.Missing++
		b.note(CodeNoValue, path, noValueReason(r))
	}
	return nil
}

func primitiveAllOf(s *openapi3.Schema) bool {
	if len(s.Properties) > 0 {
		return false
	}
	for _, part := range s.AllOf {
		if _, ok := Primitive(part.Value); ok {
			return true
		}
	}
	return false
}

// parentOf is the DTO of a dictionary path: "schemas.Garden.Name" → "Garden".
func parentOf(path string) string {
	parts := strings.Split(path, ".")
	if len(parts) >= 3 && parts[0] == "schemas" {
		return parts[1]
	}
	return ""
}

// patternOf is the pattern of a string or of the items of an array.
func patternOf(s *openapi3.Schema) string {
	if s.Pattern == "" && s.Items != nil && s.Items.Value != nil {
		return s.Items.Value.Pattern
	}
	return s.Pattern
}

func noValueReason(r value.Result) string {
	switch {
	case r.OK:
		return "the generated value does not fit the schema; set one in defaults.json"
	case r.Reason == value.ReasonUnsupported:
		return "free object without properties; set its value in the dictionary or in defaults.json"
	case r.Reason == value.ReasonConstraints:
		return "the constraints contradict each other (e.g. minimum > maximum)"
	}
	return r.Reason
}

func (b *builder) valid(s *openapi3.Schema, v any) bool {
	return len(b.validator.Validate(s, spec.Normalize(v), spec.ModePlain)) == 0
}

// Properties returns the properties of s including those of an allOf.
func Properties(s *openapi3.Schema) (openapi3.Schemas, []string) {
	out := openapi3.Schemas{}
	var req []string
	var add func(x *openapi3.Schema, depth int)
	add = func(x *openapi3.Schema, depth int) {
		if x == nil || depth > 20 {
			return
		}
		for k, v := range x.Properties {
			out[k] = v
		}
		req = append(req, x.Required...)
		for _, p := range x.AllOf {
			add(p.Value, depth+1)
		}
	}
	add(s, 0)
	return out, req
}

// Primitive reports the primitive type of s, following an allOf as
// code generators write enum references.
func Primitive(s *openapi3.Schema) (string, bool) {
	t := value.Type(s)
	switch t {
	case "string", "integer", "number", "boolean":
		return t, true
	}
	if p, _ := Properties(s); len(p) == 0 {
		for _, part := range s.AllOf {
			if t, ok := Primitive(part.Value); ok {
				return t, true
			}
		}
	}
	return "", false
}

// meta copies the constraints of s into a node.
func meta(s *openapi3.Schema) *Node {
	n := &Node{
		Type:      value.Type(s),
		Format:    s.Format,
		Pattern:   s.Pattern,
		Enum:      s.Enum,
		Minimum:   s.Min,
		Maximum:   s.Max,
		MinLength: s.MinLength,
		MaxLength: s.MaxLength,
		MinItems:  s.MinItems,
		MaxItems:  s.MaxItems,
		Nullable:  s.Nullable || s.Type.Includes("null"),
		ReadOnly:  s.ReadOnly,
		WriteOnly: s.WriteOnly,
	}
	if t, ok := Primitive(s); ok && n.Type != t {
		n.Type = t
	}
	return n
}

func sameConstraints(a, b *Node) bool {
	return a.Type == b.Type && a.Format == b.Format && a.Pattern == b.Pattern
}

func short(v any) string {
	s := fmt.Sprintf("%v", v)
	if b, ok := v.(string); ok {
		s = fmt.Sprintf("%q", b)
	}
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

func sorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
