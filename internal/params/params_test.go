package params

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func schema(mut func(s *openapi3.Schema)) *openapi3.SchemaRef {
	s := &openapi3.Schema{Type: &openapi3.Types{"string"}}
	mut(s)
	return &openapi3.SchemaRef{Value: s}
}

func TestTPL1_10_ParamRanks(t *testing.T) {
	full := func() (*openapi3.Parameter, Inputs) {
		p := &openapi3.Parameter{
			Name:     "code",
			In:       "path",
			Required: true,
			Example:  "r4",
			Examples: openapi3.Examples{"valid": {Value: &openapi3.Example{Value: "r3"}}},
			Schema: schema(func(s *openapi3.Schema) {
				s.Example = "r5"
				s.Default = "r6"
				s.Enum = []any{"r7", "x"}
			}),
		}
		in := Inputs{
			CaseName: "valid",
			Fixed:    map[string]string{"code": "r2"},
			Binding:  func(*openapi3.Parameter) (any, bool) { return "r1", true },
		}
		return p, in
	}
	// Remove the sources one by one, highest rank first.
	steps := []struct {
		want   string
		rank   Rank
		remove func(p *openapi3.Parameter, in *Inputs)
	}{
		{"r1", RankBinding, func(*openapi3.Parameter, *Inputs) {}},
		{"r2", RankFixed, func(_ *openapi3.Parameter, in *Inputs) { in.Binding = nil }},
		{"r3", RankNamedExample, func(_ *openapi3.Parameter, in *Inputs) { in.Fixed = nil }},
		{"r4", RankExample, func(p *openapi3.Parameter, _ *Inputs) { p.Examples = nil }},
		{"r5", RankSchemaExample, func(p *openapi3.Parameter, _ *Inputs) { p.Example = nil }},
		{"r6", RankDefault, func(p *openapi3.Parameter, _ *Inputs) { p.Schema.Value.Example = nil }},
		{"r7", RankEnum, func(p *openapi3.Parameter, _ *Inputs) { p.Schema.Value.Default = nil }},
	}
	p, in := full()
	for _, st := range steps {
		st.remove(p, &in)
		v, ok := Resolve(p, in)
		if !ok || v.V != st.want || v.Rank != st.rank {
			t.Errorf("got %v/%v/%v, want %v/%v", v.V, v.Rank, ok, st.want, st.rank)
		}
	}
	p.Schema.Value.Enum = nil
	if _, ok := Resolve(p, in); ok {
		t.Error("no source left: got ok, want not buildable")
	}

	// Schema examples from OpenAPI 3.1 ("examples" array) count as rank 5.
	p31 := &openapi3.Parameter{Name: "x", In: "query", Required: true, Schema: schema(func(s *openapi3.Schema) { s.Examples = []any{"a", "b"} })}
	if v, ok := Resolve(p31, Inputs{}); !ok || v.V != "a" || v.Rank != RankSchemaExample {
		t.Errorf("3.1 schema examples: got %v %v", v, ok)
	}
}

func TestTPL1_11_OptionalQueryOnlyExplicit(t *testing.T) {
	withDefault := &openapi3.Parameter{Name: "limit", In: "query", Schema: schema(func(s *openapi3.Schema) { s.Default = 10 })}
	if v, ok := Resolve(withDefault, Inputs{}); ok {
		t.Errorf("optional parameter with only a default must not be set, got %v", v)
	}
	withEnum := &openapi3.Parameter{Name: "sort", In: "query", Schema: schema(func(s *openapi3.Schema) { s.Enum = []any{"asc"} })}
	if _, ok := Resolve(withEnum, Inputs{}); ok {
		t.Error("optional parameter with only an enum must not be set")
	}
	withExample := &openapi3.Parameter{Name: "q", In: "query", Example: "go"}
	if v, ok := Resolve(withExample, Inputs{}); !ok || v.V != "go" {
		t.Errorf("optional parameter with an example must be set, got %v %v", v, ok)
	}
	fixed := &openapi3.Parameter{Name: "tenant", In: "query"}
	if v, ok := Resolve(fixed, Inputs{Fixed: map[string]string{"tenant": "t1"}}); !ok || v.V != "t1" {
		t.Errorf("optional parameter from Config.Params must be set, got %v %v", v, ok)
	}
}

func explode(b bool) *bool { return &b }

func TestTPL1_14_QuerySerialization(t *testing.T) {
	arr := []any{"a", "b"}
	obj := map[string]any{"role": "admin", "age": json.Number("3")}
	tests := []struct {
		name string
		p    *openapi3.Parameter
		v    any
		want string
	}{
		{"array explode=false", &openapi3.Parameter{Name: "tags", Explode: explode(false)}, arr, "tags=a%2Cb"},
		{"array default explode", &openapi3.Parameter{Name: "tags"}, arr, "tags=a&tags=b"},
		{"object explode", &openapi3.Parameter{Name: "f"}, obj, "age=3&role=admin"},
		{"object no explode", &openapi3.Parameter{Name: "f", Explode: explode(false)}, obj, "f=age%2C3%2Crole%2Cadmin"},
		{"pipe", &openapi3.Parameter{Name: "tags", Style: "pipeDelimited", Explode: explode(false)}, arr, "tags=a%7Cb"},
		{"space", &openapi3.Parameter{Name: "tags", Style: "spaceDelimited", Explode: explode(false)}, arr, "tags=a+b"},
		{"deepObject", &openapi3.Parameter{Name: "f", Style: "deepObject"}, obj, "f%5Bage%5D=3&f%5Brole%5D=admin"},
		{"number", &openapi3.Parameter{Name: "n"}, json.Number("1.5"), "n=1.5"},
		{"bool", &openapi3.Parameter{Name: "b"}, true, "b=true"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pairs, err := Query(tc.p, tc.v)
			if err != nil {
				t.Fatal(err)
			}
			if got := EncodeQuery(pairs); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPathAndHeaderSerialization(t *testing.T) {
	arr := []any{"a", "b"}
	obj := map[string]any{"x": "1", "y": "2"}
	tests := []struct {
		name string
		p    *openapi3.Parameter
		v    any
		want string
	}{
		{"simple scalar escaped", &openapi3.Parameter{Name: "id"}, "a b/c", "a%20b%2Fc"},
		{"simple array", &openapi3.Parameter{Name: "id"}, arr, "a,b"},
		{"simple object", &openapi3.Parameter{Name: "id"}, obj, "x,1,y,2"},
		{"simple object explode", &openapi3.Parameter{Name: "id", Explode: explode(true)}, obj, "x=1,y=2"},
		{"label", &openapi3.Parameter{Name: "id", Style: "label"}, arr, ".a,b"},
		{"label explode", &openapi3.Parameter{Name: "id", Style: "label", Explode: explode(true)}, arr, ".a.b"},
		{"matrix", &openapi3.Parameter{Name: "id", Style: "matrix"}, "5", ";id=5"},
		{"matrix array explode", &openapi3.Parameter{Name: "id", Style: "matrix", Explode: explode(true)}, arr, ";id=a;id=b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Path(tc.p, tc.v)
			if err != nil || got != tc.want {
				t.Errorf("got %q (%v), want %q", got, err, tc.want)
			}
		})
	}
	if got, _ := Header(&openapi3.Parameter{Name: "X-Ids"}, arr); got != "a,b" {
		t.Errorf("header: got %q", got)
	}
	if _, err := Path(&openapi3.Parameter{Name: "id", Style: "form"}, "1"); err == nil {
		t.Error("style form on a path parameter must be rejected")
	}
	c, err := Cookie(&openapi3.Parameter{Name: "prefs"}, arr)
	if err != nil || !reflect.DeepEqual(c, Pair{"prefs", "a,b"}) {
		t.Errorf("cookie: got %v %v", c, err)
	}
	content := &openapi3.Parameter{Name: "filter", Content: openapi3.Content{"application/json": {}}}
	pairs, _ := Query(content, map[string]any{"a": json.Number("1")})
	if got := EncodeQuery(pairs); got != "filter=%7B%22a%22%3A1%7D" {
		t.Errorf("content parameter: got %q", got)
	}
}

func TestFixedPerOperation(t *testing.T) {
	p := &openapi3.Parameter{Name: "id", In: "path", Required: true, Schema: schema(func(*openapi3.Schema) {})}
	fixed := map[string]string{"id": "any", "getRegion.id": "r1"}
	for _, tc := range []struct{ op, want string }{
		{"getRegion", "r1"},
		{"getZone", "any"},
		{"", "any"},
	} {
		v, ok := Resolve(p, Inputs{Fixed: fixed, OpID: tc.op})
		if !ok || v.V != tc.want || v.Rank != RankFixed {
			t.Errorf("%q: got %v %v %v, want %q", tc.op, v.V, v.Rank, ok, tc.want)
		}
	}
}

func TestGenerated(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	u := func(v uint64) *uint64 { return &v }
	typ := func(s string) *openapi3.Types { return &openapi3.Types{s} }
	for _, c := range []struct {
		name string
		s    *openapi3.Schema
		want string
	}{
		{"integer", &openapi3.Schema{Type: typ("integer")}, "1"},
		{"minimum", &openapi3.Schema{Type: typ("integer"), Min: f(10)}, "10"},
		{"default", &openapi3.Schema{Type: typ("string"), Default: "luna"}, "luna"},
		{"enum", &openapi3.Schema{Type: typ("string"), Enum: []any{"moon", "planet"}}, "moon"},
		{"uuid", &openapi3.Schema{Type: typ("string"), Format: "uuid"}, "00000000-0000-4000-8000-000000000001"},
		{"date", &openapi3.Schema{Type: typ("string"), Format: "date"}, "2026-01-01"},
		{"maxLength", &openapi3.Schema{Type: typ("string"), MaxLength: u(3)}, "api"},
		{"boolean", &openapi3.Schema{Type: typ("boolean")}, "true"},
		{"array", &openapi3.Schema{Type: typ("array"), Items: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typ("integer")}}}, "[1]"},
		{"pattern", &openapi3.Schema{Type: typ("string"), Pattern: "^[0-9]+$"}, "-"},
	} {
		v, ok := Generated(c.s)
		got := "-"
		if ok {
			got = fmt.Sprint(v)
		}
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	// only with Generate and only for required parameters
	p := &openapi3.Parameter{Name: "moonId", In: "query", Required: true, Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{Type: typ("integer")}}}
	if _, ok := Resolve(p, Inputs{}); ok {
		t.Error("generated without Generate")
	}
	if v, ok := Resolve(p, Inputs{Generate: true}); !ok || v.Rank != RankGenerated || v.Rank.String() != "generated" {
		t.Errorf("Generate: %+v %v", v, ok)
	}
	p.Required = false
	if _, ok := Resolve(p, Inputs{Generate: true}); ok {
		t.Error("generated for an optional parameter")
	}
}
