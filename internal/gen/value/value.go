// Package value generates example values that fit an OpenAPI schema. The
// values are deterministic: the same seed and field path always give the
// same value, so adding a field never changes the values of the others.
//
// Patterns are not solved here yet; a string with a pattern that no other
// rule satisfies yields no value (see Result.Reason), never a wrong one.
package value

import (
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/regexgen"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// maxDepth stops recursion in cyclic schemas.
const maxDepth = 12

// Reasons why no value was generated.
const (
	ReasonPattern     = "pattern"     // the string has a pattern that is not solved yet
	ReasonUnsupported = "unsupported" // e.g. a free object without properties
	ReasonDepth       = "depth"       // recursion limit reached
	ReasonConstraints = "constraints" // the constraints contradict each other
)

// Context identifies the field a value is generated for.
type Context struct {
	Seed uint64
	// Path is a stable location, e.g. "schemas.Garden.Name"; it seeds the
	// random source of this value.
	Path string
	// Name is the field or parameter name, used for semantic values such
	// as e-mail addresses.
	Name string
	// Parent is the DTO the field belongs to, e.g. "Garden" for Garden.Name,
	// so a name of a garden reads differently from a name of a person.
	Parent string
}

// Result is a generated value, or the reason why there is none.
type Result struct {
	Value  any
	OK     bool
	Reason string
}

// Generate returns a value for s.
func Generate(s *openapi3.Schema, ctx Context) Result {
	rnd := rand.New(rand.NewPCG(ctx.Seed, hash(ctx.Path)))
	g := &generator{rnd: rnd, fake: gofakeit.New(rnd.Uint64()), parent: ctx.Parent}
	v, reason := g.value(s, ctx.Name, 0)
	return Result{Value: v, OK: reason == "", Reason: reason}
}

func hash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

type generator struct {
	rnd    *rand.Rand
	fake   *gofakeit.Faker
	parent string
	// variant selects another enum value, for unique array items.
	variant int
}

func (g *generator) value(s *openapi3.Schema, name string, depth int) (any, string) {
	if s == nil {
		return nil, ReasonUnsupported
	}
	if depth > maxDepth {
		return nil, ReasonDepth
	}
	switch {
	case s.Const != nil:
		return s.Const, ""
	case s.Default != nil:
		return s.Default, ""
	case len(s.Enum) > 0:
		var values []any
		for _, e := range s.Enum {
			if e != nil {
				values = append(values, e)
			}
		}
		if len(values) > 0 {
			return values[g.variant%len(values)], ""
		}
	}
	if len(s.AllOf) > 0 && len(s.Properties) == 0 {
		if prim := primitivePart(s.AllOf); prim != nil {
			// "type: object" with an allOf of an enum or primitive: the
			// primitive wins, as code generators mean it that way.
			return g.value(prim, name, depth+1)
		}
		return g.object(merged(s), depth)
	}
	if len(s.OneOf) > 0 {
		return g.value(s.OneOf[0].Value, name, depth+1)
	}
	if len(s.AnyOf) > 0 {
		return g.value(s.AnyOf[0].Value, name, depth+1)
	}
	switch Type(s) {
	case "string":
		return g.str(s, name)
	case "integer":
		return g.integer(s)
	case "number":
		return g.number(s)
	case "boolean":
		return true, ""
	case "array":
		return g.array(s, name, depth)
	case "object", "":
		// no type at all accepts anything; an object is the safest choice
		return g.object(s, depth)
	}
	return nil, ReasonUnsupported
}

// Type is the type of s: its single type, the non-null type of a 3.1 type
// list, or the type implied by its keywords.
func Type(s *openapi3.Schema) string {
	if s == nil {
		return ""
	}
	for _, t := range s.Type.Slice() {
		if t != "null" {
			return t
		}
	}
	switch {
	case len(s.Properties) > 0:
		return "object"
	case s.Items != nil:
		return "array"
	case len(s.Enum) > 0:
		switch s.Enum[0].(type) {
		case string:
			return "string"
		case float64, int, int64:
			return "number"
		case bool:
			return "boolean"
		}
	case s.Pattern != "" || s.Format != "" || s.MaxLength != nil || s.MinLength > 0:
		return "string"
	}
	return ""
}

func primitivePart(parts openapi3.SchemaRefs) *openapi3.Schema {
	for _, p := range parts {
		if p.Value == nil {
			continue
		}
		switch Type(p.Value) {
		case "string", "integer", "number", "boolean":
			return p.Value
		}
	}
	return nil
}

// merged combines the properties of an allOf into one object schema.
func merged(s *openapi3.Schema) *openapi3.Schema {
	out := &openapi3.Schema{Properties: openapi3.Schemas{}}
	var add func(x *openapi3.Schema, depth int)
	add = func(x *openapi3.Schema, depth int) {
		if x == nil || depth > maxDepth {
			return
		}
		for k, v := range x.Properties {
			out.Properties[k] = v
		}
		out.Required = append(out.Required, x.Required...)
		for _, p := range x.AllOf {
			add(p.Value, depth+1)
		}
	}
	add(s, 0)
	return out
}

func (g *generator) object(s *openapi3.Schema, depth int) (any, string) {
	if len(s.Properties) == 0 {
		return g.freeObject(s, depth)
	}
	out := map[string]any{}
	names := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		v, reason := g.value(s.Properties[k].Value, k, depth+1)
		if reason != "" {
			if slices.Contains(s.Required, k) {
				return nil, reason
			}
			continue
		}
		out[k] = v
	}
	return out, ""
}

// freeObject fills an object without properties: {} fits every such
// schema, unless additionalProperties or minProperties ask for entries.
func (g *generator) freeObject(s *openapi3.Schema, depth int) (any, string) {
	out := map[string]any{}
	extra := s.AdditionalProperties
	closed := extra.Has != nil && !*extra.Has
	var item *openapi3.Schema
	n := int(s.MinProps)
	if extra.Schema != nil && extra.Schema.Value != nil {
		item = extra.Schema.Value
		n = max(n, 1) // one entry shows what the map holds
	}
	keys := append([]string(nil), s.Required...)
	for i := 0; len(keys) < n; i++ {
		keys = append(keys, fmt.Sprintf("key%d", i+1))
	}
	if closed && len(keys) > 0 {
		return nil, ReasonConstraints // entries required, but none allowed
	}
	for _, k := range keys {
		if item == nil {
			out[k] = g.word()
			continue
		}
		v, reason := g.value(item, k, depth+1)
		if reason != "" {
			return nil, reason
		}
		out[k] = v
	}
	if s.MaxProps != nil && uint64(len(out)) > *s.MaxProps {
		return nil, ReasonConstraints
	}
	return out, ""
}

func (g *generator) array(s *openapi3.Schema, name string, depth int) (any, string) {
	n := max(1, int(s.MinItems))
	if s.MaxItems != nil && uint64(n) > *s.MaxItems {
		n = int(*s.MaxItems)
	}
	var items *openapi3.Schema
	if s.Items != nil {
		items = s.Items.Value
	}
	seen := func(out []any, v any) bool {
		return slices.ContainsFunc(out, func(o any) bool { return fmt.Sprint(o) == fmt.Sprint(v) })
	}
	out := make([]any, 0, n)
	for tries := 0; len(out) < n; tries++ {
		if tries > n+20 {
			return nil, ReasonConstraints // uniqueItems cannot be met
		}
		g.variant = tries
		v, reason := g.value(items, singular(name), depth+1)
		g.variant = 0
		if reason != "" {
			return nil, reason
		}
		if s.UniqueItems && seen(out, v) {
			continue
		}
		out = append(out, v)
	}
	return out, ""
}

func (g *generator) integer(s *openapi3.Schema) (any, string) {
	lo, hi := bounds(s)
	if lo > hi {
		return nil, ReasonConstraints
	}
	v := lo + g.rnd.Int64N(hi-lo+1)
	if s.MultipleOf != nil && *s.MultipleOf >= 1 {
		m := int64(*s.MultipleOf)
		v = (v / m) * m
		if v < lo {
			v += m
		}
		if v > hi {
			return nil, ReasonConstraints
		}
	}
	return v, ""
}

// bounds returns the inclusive range for integers; without limits it is
// 1..1000, and with only one limit it extends 1000 from it.
func bounds(s *openapi3.Schema) (int64, int64) {
	lo, hi := int64(1), int64(1000)
	hasLo, hasHi := false, false
	if s.Min != nil {
		lo, hasLo = int64(math.Ceil(*s.Min)), true
		if s.ExclusiveMin.IsTrue() && float64(lo) == *s.Min {
			lo++
		}
	}
	if v := s.ExclusiveMin.Value; v != nil {
		lo, hasLo = int64(math.Floor(*v))+1, true
	}
	if s.Max != nil {
		hi, hasHi = int64(math.Floor(*s.Max)), true
		if s.ExclusiveMax.IsTrue() && float64(hi) == *s.Max {
			hi--
		}
	}
	if v := s.ExclusiveMax.Value; v != nil {
		hi, hasHi = int64(math.Ceil(*v))-1, true
	}
	switch {
	case hasLo && !hasHi:
		hi = lo + 999
	case hasHi && !hasLo:
		lo = min(1, hi)
		if hi-lo > 999 {
			lo = hi - 999
		}
	}
	return lo, hi
}

func (g *generator) number(s *openapi3.Schema) (any, string) {
	lo, hi := bounds(s)
	if lo > hi {
		// a narrow range such as 0.1..0.2: take its middle
		if s.Min != nil && s.Max != nil && *s.Min < *s.Max {
			return round2((*s.Min + *s.Max) / 2), ""
		}
		return nil, ReasonConstraints
	}
	v := float64(lo) + float64(g.rnd.IntN(100))/100
	if v > float64(hi) {
		v = float64(hi)
	}
	if s.MultipleOf != nil && *s.MultipleOf > 0 {
		m := *s.MultipleOf
		v = math.Ceil(v/m) * m
		if v > float64(hi) {
			return nil, ReasonConstraints
		}
	}
	return round2(v), ""
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func (g *generator) word() string { return g.fake.Noun() }

func (g *generator) str(s *openapi3.Schema, name string) (any, string) {
	lim := regexgen.Limits{Min: int(s.MinLength), Max: -1}
	if s.MaxLength != nil {
		lim.Max = int(*s.MaxLength)
	}
	// a format or a meaningful value, if it also meets pattern and length
	v, ok := g.formatted(s.Format, name)
	if !ok {
		v, ok = g.semantic(name)
	}
	if s.Pattern != "" {
		if ok && fits(v, s.Pattern, lim) {
			return v, ""
		}
		if v, ok := regexgen.Generate(s.Pattern, lim, g.rnd, 60); ok {
			return v, ""
		}
		return nil, ReasonPattern
	}
	if !ok {
		v = g.word()
	}
	for utf8.RuneCountInString(v) < lim.Min {
		v += " " + g.word()
	}
	if lim.Max >= 0 && utf8.RuneCountInString(v) > lim.Max {
		if s.Format != "" {
			return nil, ReasonConstraints // cutting would break the format
		}
		v = strings.TrimSpace(string([]rune(v)[:lim.Max]))
		for utf8.RuneCountInString(v) < lim.Min { // trimming removed a space
			v += "x"
		}
	}
	return v, ""
}

func fits(v, pattern string, lim regexgen.Limits) bool {
	n := utf8.RuneCountInString(v)
	if n < lim.Min || (lim.Max >= 0 && n > lim.Max) {
		return false
	}
	m, err := spec.CompilePattern(pattern)
	return err == nil && m.MatchString(v)
}

// formatted returns a value for a string format, or for a field name that
// clearly means one (email, url, …).
func (g *generator) formatted(format, name string) (string, bool) {
	lower := strings.ToLower(name)
	switch {
	case format == "uuid":
		return g.uuid(), true
	case format == "date-time":
		return fmt.Sprintf("2026-%02d-%02dT%02d:%02d:00Z", 1+g.rnd.IntN(12), 1+g.rnd.IntN(28), g.rnd.IntN(24), g.rnd.IntN(60)), true
	case format == "date":
		return fmt.Sprintf("2026-%02d-%02d", 1+g.rnd.IntN(12), 1+g.rnd.IntN(28)), true
	case format == "time":
		return fmt.Sprintf("%02d:%02d:00", g.rnd.IntN(24), g.rnd.IntN(60)), true
	case format == "email" || (format == "" && strings.Contains(lower, "email")):
		return g.word() + "@example.com", true
	case format == "uri" || format == "url" || (format == "" && (strings.HasSuffix(lower, "url") || strings.HasSuffix(lower, "uri"))):
		return "https://example.com/" + g.word(), true
	case format == "hostname" || (format == "" && strings.HasSuffix(lower, "hostname")):
		return g.word() + ".example.com", true
	case format == "ipv4":
		return fmt.Sprintf("10.%d.%d.%d", g.rnd.IntN(256), g.rnd.IntN(256), 1+g.rnd.IntN(254)), true
	case format == "ipv6":
		return fmt.Sprintf("2001:db8::%x", 1+g.rnd.IntN(0xfffe)), true
	case format == "byte":
		return base64.StdEncoding.EncodeToString([]byte(g.word())), true
	case format == "password" || (format == "" && strings.Contains(lower, "password")):
		return "Secret-" + fmt.Sprint(1000+g.rnd.IntN(9000)), true
	}
	return "", false
}

func (g *generator) uuid() string {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(g.rnd.IntN(256))
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// singular is a rough singular of a field name, for array items
// ("roles" → "role").
func singular(name string) string {
	switch {
	case strings.HasSuffix(name, "ies"):
		return strings.TrimSuffix(name, "ies") + "y"
	case strings.HasSuffix(name, "s") && !strings.HasSuffix(name, "ss"):
		return strings.TrimSuffix(name, "s")
	}
	return name
}
