package cases

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestNotFoundValue(t *testing.T) {
	ptr := func(f float64) *float64 { return &f }
	u := func(n uint64) *uint64 { return &n }
	param := func(s *openapi3.Schema) *openapi3.Parameter {
		return &openapi3.Parameter{Name: "moonId", In: "path", Schema: &openapi3.SchemaRef{Value: s}}
	}
	for _, c := range []struct {
		name   string
		p      *openapi3.Parameter
		want   any
		reason string
	}{
		{"integer", param(&openapi3.Schema{Type: &openapi3.Types{"integer"}}), json.Number("999999999"), ""},
		{"maximum", param(&openapi3.Schema{Type: &openapi3.Types{"integer"}, Max: ptr(500)}), json.Number("500"), ""},
		{"uuid", param(&openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "uuid"}), "00000000-0000-4000-8000-000000000404", ""},
		{"string", param(&openapi3.Schema{Type: &openapi3.Types{"string"}}), "apitest-not-found", ""},
		{"maxLength", param(&openapi3.Schema{Type: &openapi3.Types{"string"}, MaxLength: u(5)}), "apite", ""},
		{"minLength", param(&openapi3.Schema{Type: &openapi3.Types{"string"}, MinLength: 20}), "apitest-not-foundxxx", ""},
		{"enum", param(&openapi3.Schema{Type: &openapi3.Types{"string"}, Enum: []any{"luna"}}), nil, "is an enum"},
		{"pattern", param(&openapi3.Schema{Type: &openapi3.Types{"string"}, Pattern: "^[0-9]+$"}), nil, "no value for {moonId} that fits its schema"},
	} {
		v, why := notFoundValue(c.p)
		if v != c.want || (c.reason == "") != (why == "") || !strings.Contains(why, c.reason) {
			t.Errorf("%s: %v %q", c.name, v, why)
		}
	}
	p := param(&openapi3.Schema{Type: &openapi3.Types{"string"}, Pattern: "^[0-9]+$"})
	p.Extensions = map[string]any{notFoundExt: "0000"}
	if v, why := notFoundValue(p); v != "0000" || why != "" {
		t.Errorf("x-apitest-not-found: %v %q", v, why)
	}
}
