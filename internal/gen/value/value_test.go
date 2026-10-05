package value

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

func ptr[T any](v T) *T { return &v }

// TestGeneratedValuesFit generates values for many schemas and validates
// each with the validator apitest uses.
func TestGeneratedValuesFit(t *testing.T) {
	str := &openapi3.Types{"string"}
	integer := &openapi3.Types{"integer"}
	number := &openapi3.Types{"number"}
	tests := map[string]*openapi3.Schema{
		"plain string":       {Type: str},
		"length":             {Type: str, MinLength: 12, MaxLength: ptr(uint64(14))},
		"short max":          {Type: str, MaxLength: ptr(uint64(2))},
		"uuid":               {Type: str, Format: "uuid"},
		"date-time":          {Type: str, Format: "date-time"},
		"date":               {Type: str, Format: "date"},
		"email":              {Type: str, Format: "email"},
		"uri":                {Type: str, Format: "uri"},
		"ipv4":               {Type: str, Format: "ipv4"},
		"ipv6":               {Type: str, Format: "ipv6"},
		"byte":               {Type: str, Format: "byte"},
		"enum":               {Type: str, Enum: []any{"x", "y"}},
		"integer range":      {Type: integer, Min: ptr(5.0), Max: ptr(7.0)},
		"integer max only":   {Type: integer, Max: ptr(-3.0)},
		"exclusive 3.0":      {Type: integer, Min: ptr(1.0), Max: ptr(3.0), ExclusiveMin: openapi3.ExclusiveBound{Bool: ptr(true)}, ExclusiveMax: openapi3.ExclusiveBound{Bool: ptr(true)}},
		"exclusive 3.1":      {Type: integer, ExclusiveMin: openapi3.ExclusiveBound{Value: ptr(10.0)}},
		"multipleOf":         {Type: integer, Min: ptr(1.0), MultipleOf: ptr(25.0)},
		"number":             {Type: number, Min: ptr(0.5), Max: ptr(2.5)},
		"narrow number":      {Type: number, Min: ptr(0.1), Max: ptr(0.2)},
		"boolean":            {Type: &openapi3.Types{"boolean"}},
		"3.1 nullable":       {Type: &openapi3.Types{"string", "null"}, Format: "date"},
		"unique array":       {Type: &openapi3.Types{"array"}, MinItems: 3, UniqueItems: true, Items: openapi3.NewSchemaRef("", &openapi3.Schema{Type: integer, Min: ptr(1.0), Max: ptr(100.0)})},
		"object":             {Type: &openapi3.Types{"object"}, Required: []string{"a"}, Properties: openapi3.Schemas{"a": openapi3.NewSchemaRef("", &openapi3.Schema{Type: str}), "b": openapi3.NewSchemaRef("", &openapi3.Schema{Type: integer})}},
		"enum through allOf": {Type: &openapi3.Types{"object"}, AllOf: openapi3.SchemaRefs{openapi3.NewSchemaRef("", &openapi3.Schema{Type: str, Enum: []any{"on"}})}},
		"oneOf":              {OneOf: openapi3.SchemaRefs{openapi3.NewSchemaRef("", &openapi3.Schema{Type: integer})}},
		"default wins":       {Type: integer, Default: 3},
		"const wins":         {Type: str, Const: "fixed"},
		"time":               {Type: str, Format: "time"},
		"hostname":           {Type: str, Format: "hostname"},
		"password":           {Type: str, Format: "password"},
		"number multipleOf":  {Type: number, Min: ptr(1.0), Max: ptr(10.0), MultipleOf: ptr(0.5)},
		"number exclusive":   {Type: number, ExclusiveMin: openapi3.ExclusiveBound{Value: ptr(2.0)}, ExclusiveMax: openapi3.ExclusiveBound{Value: ptr(9.0)}},
		"array of objects":   {Type: &openapi3.Types{"array"}, MaxItems: ptr(uint64(1)), Items: openapi3.NewSchemaRef("", &openapi3.Schema{Properties: openapi3.Schemas{"n": openapi3.NewSchemaRef("", &openapi3.Schema{Type: integer})}})},
		"allOf object":       {AllOf: openapi3.SchemaRefs{openapi3.NewSchemaRef("", &openapi3.Schema{Properties: openapi3.Schemas{"a": openapi3.NewSchemaRef("", &openapi3.Schema{Type: str})}}), openapi3.NewSchemaRef("", &openapi3.Schema{Properties: openapi3.Schemas{"b": openapi3.NewSchemaRef("", &openapi3.Schema{Type: &openapi3.Types{"boolean"}})}})}},
		"anyOf":              {AnyOf: openapi3.SchemaRefs{openapi3.NewSchemaRef("", &openapi3.Schema{Type: str, Format: "uuid"})}},
		"implied by enum":    {Enum: []any{"only"}},
		"implied by format":  {Format: "date"},
	}
	v := spec.NewValidator()
	for name, s := range tests {
		r := Generate(s, Context{Seed: 1, Path: name, Name: name})
		if !r.OK {
			t.Errorf("%s: no value (%s)", name, r.Reason)
			continue
		}
		if name == "enum through allOf" {
			// "type: object" with a primitive allOf cannot validate; the
			// generator still yields the primitive, the dictionary reports it
			if r.Value != "on" {
				t.Errorf("%s: %v", name, r.Value)
			}
			continue
		}
		if errs := v.Validate(s, spec.Normalize(r.Value), spec.ModePlain); len(errs) > 0 {
			t.Errorf("%s: %v does not fit: %v", name, r.Value, errs)
		}
	}
}

func TestFreeObjects(t *testing.T) {
	obj := &openapi3.Types{"object"}
	for name, tc := range map[string]struct {
		s    *openapi3.Schema
		size int
	}{
		"plain":          {&openapi3.Schema{Type: obj}, 0},
		"no type at all": {&openapi3.Schema{}, 0},
		"typed map":      {&openapi3.Schema{Type: obj, AdditionalProperties: openapi3.AdditionalProperties{Schema: openapi3.NewSchemaRef("", &openapi3.Schema{Type: &openapi3.Types{"integer"}})}}, 1},
		"minProperties":  {&openapi3.Schema{Type: obj, MinProps: 3}, 3},
		"required keys":  {&openapi3.Schema{Type: obj, Required: []string{"a", "b"}}, 2},
	} {
		r := Generate(tc.s, Context{Path: name})
		m, ok := r.Value.(map[string]any)
		if !r.OK || !ok || len(m) != tc.size {
			t.Errorf("%s: %+v", name, r)
			continue
		}
		if errs := spec.NewValidator().Validate(tc.s, spec.Normalize(r.Value), spec.ModePlain); len(errs) > 0 {
			t.Errorf("%s: %v does not fit: %v", name, r.Value, errs)
		}
	}
}

func TestSemanticNames(t *testing.T) {
	str := &openapi3.Schema{Type: &openapi3.Types{"string"}}
	for name, check := range map[string]func(string) bool{
		"ContactEmail":   func(s string) bool { return strings.HasSuffix(s, "@example.com") },
		"WebsiteUrl":     func(s string) bool { return strings.HasPrefix(s, "https://") },
		"ServerHostname": func(s string) bool { return strings.HasSuffix(s, ".example.com") },
		"UserPassword":   func(s string) bool { return strings.HasPrefix(s, "Secret-") },
	} {
		r := Generate(str, Context{Path: name, Name: name})
		if s, _ := r.Value.(string); !check(s) {
			t.Errorf("%s: %v", name, r.Value)
		}
	}
	if got := singular("categories") + singular("roles") + singular("class"); got != "categoryroleclass" {
		t.Errorf("singular: %s", got)
	}
}

func TestDeterministic(t *testing.T) {
	s := &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "uuid"}
	a := Generate(s, Context{Seed: 3, Path: "x"})
	b := Generate(s, Context{Seed: 3, Path: "x"})
	c := Generate(s, Context{Seed: 3, Path: "y"})
	if a.Value != b.Value {
		t.Error("same seed and path differ")
	}
	if a.Value == c.Value {
		t.Error("another path gives the same uuid")
	}
}

func TestNoValue(t *testing.T) {
	for name, tc := range map[string]struct {
		s      *openapi3.Schema
		reason string
	}{
		"pattern":                  {&openapi3.Schema{Type: &openapi3.Types{"string"}, Pattern: `^(?=a)b$`}, ReasonPattern},
		"closed but needs entries": {&openapi3.Schema{Type: &openapi3.Types{"object"}, MinProps: 1, AdditionalProperties: openapi3.AdditionalProperties{Has: ptr(false)}}, ReasonConstraints},
		"min above max":            {&openapi3.Schema{Type: &openapi3.Types{"integer"}, Min: ptr(10.0), Max: ptr(5.0)}, ReasonConstraints},
		"format too long":          {&openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "uuid", MaxLength: ptr(uint64(5))}, ReasonConstraints},
		"too few uniques":          {&openapi3.Schema{Type: &openapi3.Types{"array"}, MinItems: 3, UniqueItems: true, Items: openapi3.NewSchemaRef("", &openapi3.Schema{Type: &openapi3.Types{"boolean"}})}, ReasonConstraints},
		"required no value":        {&openapi3.Schema{Type: &openapi3.Types{"object"}, Required: []string{"z"}, Properties: openapi3.Schemas{"z": openapi3.NewSchemaRef("", &openapi3.Schema{Type: &openapi3.Types{"string"}, Pattern: "^(?=a)b$"})}}, ReasonPattern},
	} {
		if r := Generate(tc.s, Context{Path: name}); r.OK || r.Reason != tc.reason {
			t.Errorf("%s: %+v, want reason %s", name, r, tc.reason)
		}
	}
}
