package cases

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

func load(t *testing.T, file string) *spec.Spec {
	t.Helper()
	s, err := spec.Load(t.Context(), "../../testdata/specs/"+file)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func build(t *testing.T, file string, opt Options) map[string]*Case {
	t.Helper()
	list, err := Build(load(t, file), opt)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*Case{}
	for _, c := range list {
		m[c.Name] = c
	}
	return m
}

func names(list []*Case) []string {
	var out []string
	for _, c := range list {
		out = append(out, c.Name)
	}
	return out
}

func TestTPL1_05_NamedRequestExamples(t *testing.T) {
	m := build(t, "cases.yaml", Options{IncludeOps: []string{"createPlanet"}})
	want := []string{
		"Planet/createPlanet/missing-name",
		"Planet/createPlanet/second",
		"Planet/createPlanet/valid-planet",
	}
	var got []string
	for n := range m {
		got = append(got, n)
	}
	if len(got) != 3 {
		t.Fatalf("got cases %v, want %v", got, want)
	}
	for _, n := range want {
		c := m[n]
		if c == nil {
			t.Fatalf("case %s missing", n)
		}
		if c.MediaType != "application/json" {
			t.Errorf("%s: media type %q, want application/json (FR-HTTP-05)", n, c.MediaType)
		}
	}
	if b := m["Planet/createPlanet/second"].Body; !reflect.DeepEqual(b, map[string]any{"code": "s", "name": "Second"}) {
		t.Errorf("body: got %v", b)
	}
}

func TestTPL1_06_DefaultBodyFromPropertyExamples(t *testing.T) {
	m := build(t, "cases.yaml", Options{IncludeOps: []string{"createCountry", "updatePlanet"}})
	c := m["Moon_Code/createCountry/default"]
	if c == nil {
		t.Fatalf("default case missing, got %v", m)
	}
	want := map[string]any{"iso": "DE", "name": "Deutschland", "region": "eu", "tags": []any{"x"}, "aliases": []any{}}
	if !c.HasBody || !reflect.DeepEqual(c.Body, want) {
		t.Errorf("body: got %v, want %v (readOnly id and optional note left out, required array without item example empty)", c.Body, want)
	}
	u := m["Planet/updatePlanet/default"]
	if !reflect.DeepEqual(u.Body, map[string]any{"code": "p", "name": "Personal"}) {
		t.Errorf("body from $ref schema property examples: got %v", u.Body)
	}
	if u.Expect.Code != "2XX" || !u.Expect.Matches(204) || u.Expect.Matches(404) {
		t.Errorf("2XX range expectation: %+v", u.Expect)
	}
}

func TestTPL1_07_NamedResponseExample(t *testing.T) {
	c := build(t, "cases.yaml", Options{})["Planet/createPlanet/valid-planet"]
	if c.Expect.Status != 201 || c.Kind != Positive {
		t.Fatalf("expectation: got %+v", c.Expect)
	}
	want := map[string]any{"id": json.Number("1"), "code": "l", "name": "Logistik"}
	if !c.Expect.HasExample || !reflect.DeepEqual(c.Expect.Example, want) {
		t.Errorf("expected body: got %v, want %v", c.Expect.Example, want)
	}
	if !reflect.DeepEqual(c.Ignore, []string{"/id"}) {
		t.Errorf("x-apitest-ignore: got %v", c.Ignore)
	}
	// Without a named match: lowest 2xx with its media example (FR-CASE-04 (2)).
	s := build(t, "cases.yaml", Options{})["Planet/createPlanet/second"]
	if s.Expect.Status != 201 || s.Expect.HasExample {
		t.Errorf("second: got %+v, want 201 schema-only", s.Expect)
	}
	l := build(t, "cases.yaml", Options{})["Planet/listPlanets/small"]
	if l.Expect.Status != 200 || !l.Expect.HasExample {
		t.Errorf("list: got %+v, want 200 with media example", l.Expect)
	}
}

func TestTPL1_08_NamedNegativeExample(t *testing.T) {
	c := build(t, "cases.yaml", Options{})["Planet/createPlanet/missing-name"]
	if c.Kind != Negative || c.Expect.Status != 400 {
		t.Fatalf("got kind %v expect %+v, want negative 400", c.Kind, c.Expect)
	}
	if !reflect.DeepEqual(c.Expect.Example, map[string]any{"error": "name is required"}) {
		t.Errorf("expected error body: got %v", c.Expect.Example)
	}
}

func TestTPL1_09_NotBuildableBody(t *testing.T) {
	m := build(t, "cases.yaml", Options{})
	c := m["Moon_Code/needsBody/default"]
	if c == nil || !strings.Contains(c.NotBuildable, "/many/0") { // minItems: 1 without item example
		t.Fatalf("got %+v, want NOT_BUILDABLE naming /many/0", c)
	}
	if c.Expect.Code != "default" || !c.Expect.Matches(200) {
		t.Errorf("only default response: expect any 2xx, got %+v", c.Expect)
	}
	u := m["untagged/upload/default"]
	if u == nil || !strings.Contains(u.NotBuildable, "application/octet-stream") {
		t.Errorf("unsupported media type: got %+v", u)
	}
}

func TestParamExampleNamesAndSkip(t *testing.T) {
	m := build(t, "cases.yaml", Options{Tags: []string{"Planet"}})
	if m["Planet/listPlanets/small"] == nil || m["Planet/listPlanets/large"] == nil || m["Planet/listPlanets/default"] != nil {
		t.Errorf("GET with named parameter examples: got %v", keys(m))
	}
	if d := m["Planet/deletePlanet/default"]; d == nil || d.Skip != "deletion not until phase 2" {
		t.Errorf("x-apitest-skip: got %+v", d)
	}
	for n := range m {
		if !strings.HasPrefix(n, "Planet/") {
			t.Errorf("Tags filter: unexpected case %s", n)
		}
	}
}

func TestOrderWithinGroup(t *testing.T) {
	list, err := Build(load(t, "cases.yaml"), Options{Tags: []string{"Planet"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Planet/createPlanet/second",
		"Planet/createPlanet/valid-planet",
		"Planet/getPlanet/default",
		"Planet/listPlanets/large",
		"Planet/listPlanets/small",
		"Planet/updatePlanet/default",
		"Planet/createPlanet/missing-name",
		"Planet/deletePlanet/default",
	}
	if got := names(list); !reflect.DeepEqual(got, want) {
		t.Errorf("order:\n got  %v\n want %v", got, want)
	}
}

func TestGroupOrder(t *testing.T) {
	list, err := Build(load(t, "cases.yaml"), Options{Tags: []string{"Moon Code", "Planet"}})
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Group != "Moon Code" || list[len(list)-1].Group != "Planet" {
		t.Errorf("Config.Tags order not respected: first %s, last %s", list[0].Group, list[len(list)-1].Group)
	}
}

func TestTPL1_27_NameNormalization(t *testing.T) {
	if got := Name("Client Scopes", "POST /planets/{id}", "a b"); got != "Client_Scopes/POST__planets_{id}/a_b" {
		t.Errorf("got %q", got)
	}
	if got := NameElem("x\ty\u0001"); got != `x_y\x01` {
		t.Errorf("got %q", got)
	}
	_, err := Build(load(t, "dup_names.yaml"), Options{})
	if err == nil || !strings.Contains(err.Error(), "A_B/op_x/default") {
		t.Errorf("duplicate names after normalization: got %v", err)
	}
}

func TestSelectionErrors(t *testing.T) {
	s := load(t, "cases.yaml")
	if _, err := Build(s, Options{IncludeOps: []string{"nope"}}); err == nil {
		t.Error("unknown IncludeOps entry must be an error")
	}
	if _, err := Build(s, Options{Tags: []string{"Nope"}}); err == nil {
		t.Error("unknown tag must be an error")
	}
	m := build(t, "cases.yaml", Options{ExcludeOps: []string{"createPlanet", "POST /uploads"}})
	for n := range m {
		if strings.Contains(n, "createPlanet") || strings.Contains(n, "upload") {
			t.Errorf("excluded operation still planned: %s", n)
		}
	}
}

func keys(m map[string]*Case) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestAuthCases(t *testing.T) {
	s := load(t, "auth.yaml")
	list, err := Build(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range list {
		got[c.Name] = fmt.Sprintf("%d %s rank=%d", c.Kind, c.Expect, c.Rank)
	}
	want := map[string]string{
		"Item/createItem/default":       "0 201 rank=0",
		"Item/createItem/unauthorized":  "2 401 rank=6",
		"Item/createItem/invalid-token": "3 401 rank=6",
		"Item/createItem/forbidden":     "4 403 rank=6",
		"Item/deleteItem/default":       "0 204 rank=7",
		"Item/deleteItem/invalid-token": "3 403 rank=7",
		"Item/deleteItem/forbidden":     "4 403 rank=7",
		"Item/openItem/default":         "0 200 rank=1",
		"Item/noAuthDocs/default":       "0 200 rank=2",
		"Item/optionalAuth/default":     "0 200 rank=2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cases:\n got  %v\n want %v", got, want)
	}
	// The DELETE's auth case runs right before the DELETE.
	names := names(list)
	if i, j := indexOf(names, "Item/deleteItem/invalid-token"), indexOf(names, "Item/deleteItem/default"); i >= j || indexOf(names, "Item/deleteItem/forbidden") >= j {
		t.Errorf("order: %v", names)
	}
	for _, c := range list {
		if c.Name == "Item/createItem/forbidden" && (!c.HasBody || c.ParamSource() != "default" || c.Compare != "schema") {
			t.Errorf("auth case must reuse body and params of the regular case: %+v", c)
		}
	}
	var findings []string
	for _, f := range AuthFindings(s) {
		findings = append(findings, f.Where)
	}
	wantF := []string{"paths./items/no-docs.get.responses", "paths./items/no-docs.get.x-apitest-forbidden"}
	if !reflect.DeepEqual(findings, wantF) {
		t.Errorf("findings: %v, want %v", findings, wantF)
	}
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}
