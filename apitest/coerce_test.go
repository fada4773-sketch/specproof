package apitest

import (
	"encoding/json"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// TestCoerceSchema: a filled value takes the types of its schema, as a
// parameter does: an id saved from a Location header (text) in an integer
// field, a number in a string field, also in lists and allOf; other
// values stay.
func TestCoerceSchema(t *testing.T) {
	integer := &openapi3.Schema{Type: &openapi3.Types{"integer"}}
	text := &openapi3.Schema{Type: &openapi3.Types{"string"}}
	log := &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
		"shipId": {Value: integer},
		"code":   {Value: text},
		"crew":   {Value: &openapi3.Schema{Type: &openapi3.Types{"array"}, Items: &openapi3.SchemaRef{Value: integer}}},
	}}
	wrapped := &openapi3.Schema{AllOf: openapi3.SchemaRefs{{Value: log}}}
	got := coerceSchema(map[string]any{"shipId": "8", "code": json.Number("7"), "crew": []any{"1", "x"}, "name": "8"}, wrapped).(map[string]any)
	if got["shipId"] != json.Number("8") || got["code"] != "7" || got["name"] != "8" {
		t.Errorf("scalars: %#v", got)
	}
	if crew := got["crew"].([]any); crew[0] != json.Number("1") || crew[1] != "x" {
		t.Errorf("list: %#v", crew)
	}
	if v := coerceSchema("8", nil); v != "8" {
		t.Errorf("no schema: %#v", v)
	}
}
