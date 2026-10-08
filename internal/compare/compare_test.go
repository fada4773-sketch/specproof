package compare

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := spec.DecodeJSON([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func pointers(d []Diff) []string {
	out := []string{}
	for _, x := range d {
		out = append(out, x.Pointer)
	}
	return out
}

func TestTPL1_15_SubsetAndExact(t *testing.T) {
	exp := decode(t, `{"code":"l","name":"Logistik"}`)
	act := decode(t, `{"code":"l","name":"Logistik","id":7}`)
	if d := Values(exp, act, Options{Mode: ModeSubset}); len(d) != 0 {
		t.Errorf("subset with extra field: got %v, want no diffs", d)
	}
	d := Values(exp, act, Options{Mode: ModeExact})
	if !reflect.DeepEqual(pointers(d), []string{"/id"}) || d[0].Note != "extra field" {
		t.Errorf("exact with extra field: got %+v", d)
	}
	if d := Values(exp, act, Options{Mode: ModeExact, Ignore: []string{"id"}}); len(d) != 0 {
		t.Errorf("exact with ignored extra field: got %v", d)
	}
	if d := Values(exp, decode(t, `{"code":"c"}`), Options{Mode: ModeSubset}); !reflect.DeepEqual(pointers(d), []string{"/code", "/name"}) {
		t.Errorf("mismatch and missing: got %+v", d)
	} else if d[1].Actual != Missing || d[0].Expected != `"l"` || d[0].Actual != `"c"` {
		t.Errorf("diff content: got %+v", d)
	}
	if d := Values(exp, decode(t, `{"x":1}`), Options{Mode: ModeSchema}); d != nil {
		t.Errorf("schema mode must not compare values: got %v", d)
	}
}

func TestIgnoreNestedAndPointer(t *testing.T) {
	exp := decode(t, `{"id":1,"items":[{"id":1,"n":"a"},{"id":2,"n":"b"}],"meta":{"id":5}}`)
	act := decode(t, `{"id":9,"items":[{"id":8,"n":"a"},{"id":7,"n":"b"}],"meta":{"id":6}}`)
	if d := Values(exp, act, Options{Mode: ModeSubset, Ignore: []string{"id"}}); len(d) != 0 {
		t.Errorf("field name ignored at every level: got %v", d)
	}
	d := Values(exp, act, Options{Mode: ModeSubset, Ignore: []string{"/id", "/items/*/id"}})
	if !reflect.DeepEqual(pointers(d), []string{"/meta/id"}) {
		t.Errorf("pointer ignore: got %v, want only /meta/id", pointers(d))
	}
}

func TestArrays(t *testing.T) {
	exp := decode(t, `[{"n":"a"}]`)
	if d := Values(exp, decode(t, `[{"n":"a"},{"n":"b"}]`), Options{Mode: ModeSubset}); len(d) != 0 {
		t.Errorf("subset allows more elements: got %v", d)
	}
	if d := Values(exp, decode(t, `[{"n":"a"},{"n":"b"}]`), Options{Mode: ModeExact}); len(d) != 1 {
		t.Errorf("exact requires same length: got %v", d)
	}
	if d := Values(decode(t, `["a","b"]`), decode(t, `["b","a"]`), Options{Mode: ModeSubset}); len(d) != 2 {
		t.Errorf("arrays are ordered by default: got %v", d)
	}
	if d := Values(exp, decode(t, `[]`), Options{Mode: ModeSubset}); len(d) != 1 || !strings.Contains(d[0].Note, "0 instead of 1") {
		t.Errorf("too few elements: got %+v", d)
	}
}

func TestTPL1_16_FormatOnlyAndNumbers(t *testing.T) {
	s := &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
		"id":        {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "uuid"}},
		"createdAt": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "date-time"}},
		"day":       {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "date"}},
		"version":   {Value: &openapi3.Schema{Type: &openapi3.Types{"integer"}, ReadOnly: true}},
	}}
	exp := decode(t, `{"id":"00000000-0000-0000-0000-000000000001","createdAt":"2026-01-01T00:00:00Z","day":"2026-01-01","version":1}`)
	act := decode(t, `{"id":"5f8c7c1e-0000-4000-8000-000000000000","createdAt":"2026-09-29T10:00:00Z","day":"2026-09-29","version":7}`)
	if d := Values(exp, act, Options{Mode: ModeExact, Schema: s}); len(d) != 0 {
		t.Errorf("server-generated fields must only be checked for presence: got %v", d)
	}
	if d := Values(exp, decode(t, `{}`), Options{Mode: ModeSubset, Schema: s}); len(d) != 4 {
		t.Errorf("missing server-generated fields must be reported: got %v", d)
	}

	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{`1`, `1.0`, true},
		{`1`, `1e0`, true},
		{`9007199254740993`, `9007199254740992`, false},
		{`123456789012345678901234567890`, `123456789012345678901234567890`, true},
		{`1`, `"1"`, false},
		{`true`, `true`, true},
		{`null`, `null`, true},
		{`null`, `0`, false},
	} {
		if got := Equal(decode(t, tc.a), decode(t, tc.b)); got != tc.want {
			t.Errorf("Equal(%s, %s): got %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	if !Equal(json.Number("2"), 2.0) {
		t.Error("json.Number and float64 must compare numerically")
	}
}

func TestParseMode(t *testing.T) {
	if m, err := ParseMode("exact"); err != nil || m != ModeExact {
		t.Errorf("got %v %v", m, err)
	}
	if _, err := ParseMode("fuzzy"); err == nil {
		t.Error("unknown mode must be rejected")
	}
}

func TestResponseSchema(t *testing.T) {
	book := &openapi3.Schema{
		Type:     &openapi3.Types{"object"},
		Required: []string{"id", "price"},
		Properties: openapi3.Schemas{
			"id":    {Value: &openapi3.Schema{Type: &openapi3.Types{"integer"}}},
			"price": {Value: &openapi3.Schema{Type: &openapi3.Types{"number"}}},
		},
	}
	r := &openapi3.Response{
		Content: openapi3.Content{"application/json": {Schema: &openapi3.SchemaRef{Value: book}}},
		Headers: openapi3.Headers{
			"X-Rate": {Value: &openapi3.Header{Parameter: openapi3.Parameter{Required: true, Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"integer"}}}}}},
		},
	}
	v := spec.NewValidator()
	jsonHdr := http.Header{"Content-Type": {"application/json; charset=utf-8"}, "X-Rate": {"5"}}

	errs, body, isJSON := Response(v, r, jsonHdr, []byte(`{"id":1,"price":2.5}`))
	if len(errs) != 0 || !isJSON || body == nil {
		t.Fatalf("valid response: errs %v json %v", errs, isJSON)
	}
	errs, _, _ = Response(v, r, jsonHdr, []byte(`{"id":1,"price":"2.5"}`))
	if len(errs) != 1 || errs[0].Pointer != "/price" {
		t.Errorf("string price: got %v, want error at /price", errs)
	}
	errs, _, _ = Response(v, r, http.Header{"Content-Type": {"application/json"}}, []byte(`{"id":1,"price":1}`))
	if len(errs) != 1 || errs[0].Pointer != "header:X-Rate" {
		t.Errorf("missing required header: got %v", errs)
	}
	errs, _, _ = Response(v, r, http.Header{"Content-Type": {"application/json"}, "X-Rate": {"many"}}, []byte(`{"id":1,"price":1}`))
	if len(errs) != 1 || !strings.HasPrefix(errs[0].Pointer, "header:X-Rate") {
		t.Errorf("wrong header type: got %v", errs)
	}
	errs, _, _ = Response(v, r, http.Header{"Content-Type": {"text/html"}, "X-Rate": {"1"}}, []byte(`<html>`))
	if len(errs) != 1 || !strings.Contains(errs[0].Reason, "text/html") {
		t.Errorf("undocumented content type: got %v", errs)
	}
	errs, _, _ = Response(v, r, jsonHdr, []byte(`{"id":`))
	if len(errs) != 1 || !strings.Contains(errs[0].Reason, "not valid JSON") {
		t.Errorf("broken JSON: got %v", errs)
	}
	errs, _, _ = Response(v, &openapi3.Response{}, http.Header{}, []byte(`{"x":1}`))
	if len(errs) != 1 {
		t.Errorf("undocumented body: got %v", errs)
	}
	errs, _, _ = Response(v, &openapi3.Response{}, http.Header{}, nil)
	if len(errs) != 0 {
		t.Errorf("no content documented, no body: got %v", errs)
	}
	wild := &openapi3.Response{Content: openapi3.Content{"application/*": {}}}
	if errs, _, _ := Response(v, wild, http.Header{"Content-Type": {"application/xml"}}, []byte(`<a/>`)); len(errs) != 0 {
		t.Errorf("wildcard media type: got %v", errs)
	}
}

func TestUnorderedArrays(t *testing.T) {
	exp := decode(t, `[{"n":"a"},{"n":"b"}]`)
	act := decode(t, `[{"n":"b","x":1},{"n":"a"}]`)
	if d := Values(exp, act, Options{Mode: ModeSubset, Unordered: true}); len(d) != 0 {
		t.Errorf("unordered: got %v", d)
	}
	if d := Values(exp, decode(t, `[{"n":"a"},{"n":"a"}]`), Options{Mode: ModeSubset, Unordered: true}); len(d) != 1 || d[0].Pointer != "/1" {
		t.Errorf("each actual element may only be used once: %v", d)
	}
	s := &openapi3.Schema{Type: &openapi3.Types{"array"}, Extensions: map[string]any{"x-apitest-compare-unordered": true}}
	if d := Values(decode(t, `["a","b"]`), decode(t, `["b","a"]`), Options{Mode: ModeSubset, Schema: s}); len(d) != 0 {
		t.Errorf("x-apitest-compare-unordered on the schema: %v", d)
	}
}

// IgnoreCase matches strings and field names in any case; an exact field
// name wins, numbers and types stay exact, and without it case counts.
func TestIgnoreCase(t *testing.T) {
	exp := decode(t, `{"name":"Nord","Pilot":{"callsign":"ALPHA"},"crew":["Ana","Ben"],"capacity":4,"createdBy":"x"}`)
	act := decode(t, `{"Name":"nord","pilot":{"Callsign":"alpha"},"crew":["ana","BEN"],"capacity":4,"CreatedBy":"y"}`)
	if d := Values(exp, act, Options{Mode: ModeSubset}); len(d) != 5 {
		t.Errorf("case counts without IgnoreCase: got %+v", d)
	}
	opt := Options{Mode: ModeExact, IgnoreCase: true, Ignore: []string{"createdby"}}
	if d := Values(exp, act, opt); len(d) != 0 {
		t.Errorf("IgnoreCase: got %+v", d)
	}
	opt.Ignore = []string{"/PILOT/CALLSIGN", "createdBy"}
	if d := Values(exp, decode(t, `{"Name":"nord","pilot":{"Callsign":"x"},"crew":["ana","ben"],"capacity":"4","CreatedBy":"z"}`), opt); !reflect.DeepEqual(pointers(d), []string{"/capacity"}) {
		t.Errorf("pointer ignored in any case, numbers stay typed: got %+v", d)
	}
	// the exact name wins over one in another case
	if d := Values(decode(t, `{"name":"a"}`), decode(t, `{"Name":"b","name":"A"}`), Options{Mode: ModeSubset, IgnoreCase: true}); len(d) != 0 {
		t.Errorf("exact name first: got %+v", d)
	}
	if d := Values(decode(t, `{"name":"a"}`), decode(t, `{"name":"b"}`), Options{Mode: ModeSubset, IgnoreCase: true}); !reflect.DeepEqual(pointers(d), []string{"/name"}) {
		t.Errorf("other values still differ: got %+v", d)
	}
}
