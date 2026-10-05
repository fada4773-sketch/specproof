package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

type run struct {
	res  *Result
	doc  *yamldoc.Doc
	dict *dict.Dict
	out  *spec.Spec // the written file, loaded again
	text string
}

// applyTo runs Apply on a copy of testdata/gen/apply.yaml with the given
// defaults and loads the result again with apitest's loader.
func applyTo(t *testing.T, defaultsJSON string, opt Options) run {
	t.Helper()
	return applyToFile(t, "apply.yaml", defaultsJSON, opt)
}

func applyToFile(t *testing.T, file, defaultsJSON string, opt Options) run {
	t.Helper()
	src, err := os.ReadFile("../../../testdata/gen/" + file)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), file)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	d, _, _ := dict.Build(s, nil, dict.Options{Seed: 1})
	defs, err := defaults.Parse([]byte(defaultsJSON))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := yamldoc.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	res := Apply(doc, s, d, defs, opt)
	r := run{res: res, doc: doc, dict: d}
	if len(res.Fatal) > 0 {
		return r
	}
	b, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	r.text = string(b)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if r.out, err = spec.Load(context.Background(), path); err != nil {
		t.Fatalf("written spec does not load: %v\n%s", err, b)
	}
	return r
}

func example(t *testing.T, doc *yamldoc.Doc, keys ...string) any {
	t.Helper()
	n := yamldoc.Path(doc.Root, keys...)
	if n == nil {
		return nil
	}
	v, err := yamldoc.Decode(n)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func notes(r run, code string) []string {
	var out []string
	for _, n := range r.res.Notes {
		if n.Code == code {
			out = append(out, n.Where+": "+n.Message)
		}
	}
	return out
}

const baseDefaults = `{
  "PilotCode": "a1",
  "pilotId": 7,
  "getPilot.id": {"bind": "createPilot", "pointer": "/Id"},
  "deleteUser.key": {"bind": "createPilot", "pointer": "/PilotCode"},
  "updatePilot.x-apitest-verify": false,
  "updatePilot.pilotId": 3,
  "noSuchField": 1
}`

func TestApplyWritesExamples(t *testing.T) {
	r := applyTo(t, baseDefaults, Options{Seed: 1})
	if len(r.res.Fatal) > 0 {
		t.Fatalf("fatal: %v", r.res.Fatal)
	}

	// request body with named examples: no "example" next to "examples"
	if v := example(t, r.doc, "paths", "/pilots", "post", "requestBody", "content", "application/json", "example"); v != nil {
		t.Errorf("example written next to examples: %v", v)
	}
	// shared request body: generated, with the default, without readOnly Id
	body, _ := example(t, r.doc, "components", "requestBodies", "PilotBody", "content", "application/json", "example").(map[string]any)
	if body["PilotCode"] != "a1" || body["Id"] != nil || body["Name"] == nil || body["Secret"] == nil {
		t.Errorf("request example: %v", body)
	}
	// response: no writeOnly Secret, readOnly Id present
	resp, _ := example(t, r.doc, "paths", "/pilots", "post", "responses", "201", "content", "application/json", "example").(map[string]any)
	if resp["Secret"] != nil || resp["Id"] == nil {
		t.Errorf("response example: %v", resp)
	}

	// named examples get the default, except with x-example-defaults: false
	if v := example(t, r.doc, "paths", "/pilots", "post", "requestBody", "content", "application/json", "examples", "curated", "value", "PilotCode"); v != "a1" {
		t.Errorf("curated example PilotCode = %v", v)
	}
	if v := example(t, r.doc, "paths", "/pilots", "post", "requestBody", "content", "application/json", "examples", "wrongPilot", "value", "PilotCode"); v != "missing" {
		t.Errorf("negative example was changed: %v", v)
	}

	// a valid existing example is kept, but gets the default
	kept, _ := example(t, r.doc, "paths", "/pilots/{id}", "get", "responses", "200", "content", "application/json", "example").(map[string]any)
	if kept["Name"] != "Kept pilot" || kept["PilotCode"] != "a1" {
		t.Errorf("kept example: %v", kept)
	}
	// invalid existing examples are replaced: a 200, a 400 and a DTO example
	if len(notes(r, CodeReplaced)) != 3 {
		t.Errorf("replaced: %v", notes(r, CodeReplaced))
	}

	// the shared request body was written at its target, the $ref stays
	if yamldoc.Get(yamldoc.Path(r.doc.Root, "paths", "/pilots/{id}", "put"), "requestBody").Content[1].Value != "#/components/requestBodies/PilotBody" {
		t.Error("the $ref of the request body was changed")
	}

	// generic {id}: /pilots/{id} takes pilotId
	if v, _ := example(t, r.doc, "paths", "/pilots/{id}", "get", "parameters", "0", "example").(json.Number); v != "7" {
		t.Errorf("getPilot id = %v, want 7 from pilotId", v)
	}
	// generic {key} without default: a value kept per path
	if v := example(t, r.doc, "paths", "/users/{key}", "parameters", "0", "example"); v == nil || r.dict.Paths["/users/{key}"] == nil {
		t.Errorf("deleteUser key = %v, dictionary %v", v, r.dict.Paths)
	}

	// a shared parameter cannot take an operation-specific default
	if len(notes(r, CodeSharedParam)) == 0 {
		t.Error("SHARED_PARAM_CONFLICT missing")
	}

	// extension and bindings
	if v := example(t, r.doc, "paths", "/pilots/{id}", "put", "x-apitest-verify"); v != false {
		t.Errorf("x-apitest-verify = %v", v)
	}
	bind, _ := example(t, r.doc, "paths", "/pilots/{id}", "get", "parameters", "0", "x-apitest-bind").(map[string]any)
	if bind["from"] != "createPilot" || bind["pointer"] != "/Id" {
		t.Errorf("x-apitest-bind: %v", bind)
	}
	link, _ := example(t, r.doc, "paths", "/pilots", "post", "responses", "201", "links", "deleteUser_key").(map[string]any)
	if link["operationId"] != "deleteUser" || !reflect.DeepEqual(link["parameters"], map[string]any{"key": "$response.body#/PilotCode"}) {
		t.Errorf("link: %v", link)
	}

	// unused defaults are reported
	if u := notes(r, CodeDefaultUnused); len(u) != 1 || !strings.Contains(u[0], "noSuchField") {
		t.Errorf("unused: %v", u)
	}
	// comments survive
	if !strings.Contains(r.text, "# comment that must survive") {
		t.Error("comment lost")
	}
	// the written spec loads and its examples fit their schemas
	for _, f := range r.out.Findings {
		if f.Kind == spec.FindingExampleSchema {
			t.Errorf("finding in the written spec: %s %s", f.Where, f.Message)
		}
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	first := applyTo(t, baseDefaults, Options{Seed: 1})
	s := first.out
	doc, err := yamldoc.Parse([]byte(first.text))
	if err != nil {
		t.Fatal(err)
	}
	defs, _ := defaults.Parse([]byte(baseDefaults))
	res := Apply(doc, s, first.dict, defs, Options{Seed: 1})
	if res.Changed || res.Stats.Added+res.Stats.Replaced+res.Stats.DefaultsApplied+res.Stats.Extensions+res.Stats.Bindings != 0 {
		t.Errorf("second run changed something: %+v %v", res.Stats, res.Notes)
	}
}

func TestApplyFatal(t *testing.T) {
	for name, tc := range map[string]struct{ defaults, want string }{
		"default violates schema": {`{"PilotCode": "this is far too long for ten"}`, "DEFAULT_INVALID"},
		"extension type":          {`{"updatePilot.x-apitest-verify": "false"}`, "EXT_INVALID"},
		"extension on operation":  {`{"updatePilot.x-apitest-bind": true}`, "EXT_INVALID"},
		"unknown producer":        {`{"getPilot.id": {"bind": "nope", "pointer": "/Id"}}`, "BIND_INVALID"},
	} {
		t.Run(name, func(t *testing.T) {
			r := applyTo(t, tc.defaults, Options{Seed: 1})
			if len(r.res.Fatal) == 0 || !strings.Contains(strings.Join(r.res.Fatal, "\n"), tc.want) {
				t.Errorf("fatal: %v", r.res.Fatal)
			}
		})
	}
}

func TestApplyOverwrite(t *testing.T) {
	r := applyTo(t, `{}`, Options{Seed: 1, Overwrite: true})
	kept, _ := example(t, r.doc, "paths", "/pilots/{id}", "get", "responses", "200", "content", "application/json", "example").(map[string]any)
	if kept["Name"] == "Kept pilot" {
		t.Errorf("Overwrite kept the example: %v", kept)
	}
}

func TestResourceKeys(t *testing.T) {
	for path, want := range map[string][]string{
		"/pilots/{id}":             {"pilotId", "pilotsId"},
		"/categories/{id}":         {"categoryId", "categoriesId"},
		"/statuses/{id}":           {"statuseId", "statusId", "statusesId"},
		"/app-versions/{id}":       {"appVersionId", "appVersionsId"},
		"/pilots/{pilotId}/x/{id}": {"xId"},
		"/{id}":                    nil,
		"/a/{other}/{id}":          nil,
	} {
		if got := resourceKeys(path, "id"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}

func TestCoerce(t *testing.T) {
	str, _ := spec.Load(context.Background(), "../../../testdata/gen/apply.yaml")
	app := str.Doc.Components.Schemas["Pilot"].Value
	id, code := app.Properties["Id"].Value, app.Properties["PilotCode"].Value
	if v := coerce("42", id); v.(interface{ String() string }).String() != "42" {
		t.Errorf("string to number: %#v", v)
	}
	if v := coerce([]any{"x", "y"}, code); v != "x" {
		t.Errorf("array to scalar: %#v", v)
	}
	if v := coerce(true, code); v != "true" {
		t.Errorf("bool to string: %#v", v)
	}
}

// apitest validates every example, so apply replaces every invalid one: in
// components.schemas and in error responses too, but it adds none there.
func TestApplyReplacesEveryInvalidExample(t *testing.T) {
	r := applyTo(t, `{}`, Options{Seed: 1})
	if len(r.res.Fatal) > 0 {
		t.Fatalf("fatal: %v", r.res.Fatal)
	}
	dto, _ := example(t, r.doc, "components", "schemas", "Pilot", "example").(map[string]any)
	if _, isNumber := dto["Id"].(json.Number); !isNumber {
		t.Errorf("invalid DTO example not replaced: %v", dto)
	}
	problem, _ := example(t, r.doc, "paths", "/pilots/{id}", "get", "responses", "400", "content", "application/json", "example").(map[string]any)
	if _, ok := problem["Message"].(string); !ok {
		t.Errorf("invalid 400 example not replaced: %v", problem)
	}
	if v := example(t, r.doc, "paths", "/pilots/{id}", "get", "responses", "404", "content", "application/json", "example"); v != nil {
		t.Errorf("an example was added to an error response: %v", v)
	}
	// nothing else is left for apitest to complain about
	for _, f := range r.out.Findings {
		if f.Kind == spec.FindingExampleSchema {
			t.Errorf("finding left: %s %s", f.Where, f.Message)
		}
	}
}

// Curated named examples are never replaced; an invalid one is reported,
// except a request example paired with a 4xx response (a negative test).
func TestApplyReportsInvalidNamedExamples(t *testing.T) {
	r := applyToFile(t, "named.yaml", `{}`, Options{Seed: 1})
	if v := example(t, r.doc, "paths", "/moons", "post", "responses", "201", "content", "application/json", "examples", "broken", "value", "Name"); v != json.Number("7") {
		t.Errorf("named example changed: %v", v)
	}
	n := notes(r, CodeNamedInvalid)
	if len(n) != 1 || !strings.Contains(n[0], "responses.201") {
		t.Errorf("named invalid: %v", n)
	}
}

// A default for a place that holds a DTO, a list of DTOs or a free object
// is laid over the generated value; readOnly fields stay out of requests.
func TestApplyObjectDefaults(t *testing.T) {
	r := applyToFile(t, "nested.yaml", `{
	  "Ship.Pilot": {"Name": "Ada", "Id": 9},
	  "Ship.Crew": [{"Name": "Bo"}, {"Name": "Cy", "Rank": 5}],
	  "Ship.Tags": ["red", "fast"],
	  "Ship.Settings": {"mode": "eco", "lights": {"deck": true}}
	}`, Options{Seed: 1})
	if len(r.res.Fatal) > 0 {
		t.Fatalf("fatal: %v", r.res.Fatal)
	}
	body := func(keys ...string) map[string]any {
		v, _ := example(t, r.doc, keys...).(map[string]any)
		return v
	}
	req := body("paths", "/ships", "post", "requestBody", "content", "application/json", "example")
	pilot, _ := req["Pilot"].(map[string]any)
	if pilot["Name"] != "Ada" || pilot["Rank"] == nil || pilot["Id"] != nil {
		t.Errorf("request pilot: %v", pilot)
	}
	crew, _ := req["Crew"].([]any)
	if len(crew) != 2 {
		t.Fatalf("crew: %v", crew)
	}
	second, _ := crew[1].(map[string]any)
	if second["Name"] != "Cy" || second["Rank"] != json.Number("5") {
		t.Errorf("crew[1]: %v", second)
	}
	if first, _ := crew[0].(map[string]any); first["Rank"] == nil {
		t.Errorf("crew[0] lost its generated fields: %v", first)
	}
	if !reflect.DeepEqual(req["Tags"], []any{"red", "fast"}) {
		t.Errorf("tags: %v", req["Tags"])
	}
	settings, _ := req["Settings"].(map[string]any)
	if settings["mode"] != "eco" {
		t.Errorf("settings: %v", settings)
	}
	resp := body("paths", "/ships", "post", "responses", "201", "content", "application/json", "example")
	if p, _ := resp["Pilot"].(map[string]any); p["Id"] != json.Number("9") {
		t.Errorf("response pilot keeps readOnly Id: %v", p)
	}
	// an existing example gets the default merged in, its other fields stay
	upd := body("paths", "/ships/{id}", "put", "requestBody", "content", "application/json", "example")
	if p, _ := upd["Pilot"].(map[string]any); p["Name"] != "Ada" || p["Rank"] != json.Number("2") {
		t.Errorf("existing pilot: %v", p)
	}
}

func TestApplyObjectDefaultInvalid(t *testing.T) {
	r := applyToFile(t, "nested.yaml", `{"Ship.Pilot": {"Rank": 9}}`, Options{Seed: 1})
	if len(r.res.Fatal) == 0 || !strings.Contains(r.res.Fatal[0], "DEFAULT_INVALID") || !strings.Contains(r.res.Fatal[0], "/Rank") {
		t.Errorf("fatal: %v", r.res.Fatal)
	}
}

func TestOverlay(t *testing.T) {
	base := map[string]any{"a": 1, "n": map[string]any{"x": 1, "y": 2}, "l": []any{map[string]any{"k": 1, "j": 2}}}
	top := map[string]any{"n": map[string]any{"y": 3}, "l": []any{map[string]any{"k": 5}, map[string]any{"j": 7}}}
	want := map[string]any{"a": 1, "n": map[string]any{"x": 1, "y": 3}, "l": []any{map[string]any{"k": 5, "j": 2}, map[string]any{"k": 1, "j": 7}}}
	if got := overlay(base, top); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

// "#/components/schemas/<Dto>" sets the DTO itself: its schema example and
// every place that holds it; a field default is laid over it.
func TestApplyDTODefault(t *testing.T) {
	r := applyToFile(t, "nested.yaml", `{
	  "#/components/schemas/Person": {"name": "my", "age": 12},
	  "Ship.Guest": {"age": 30},
	  "person": {"name": "unused"}
	}`, Options{Seed: 1})
	if len(r.res.Fatal) > 0 {
		t.Fatalf("fatal: %v", r.res.Fatal)
	}
	want := map[string]any{"name": "my", "age": json.Number("12")}
	if got := example(t, r.doc, "components", "schemas", "Person", "example"); !reflect.DeepEqual(got, want) {
		t.Errorf("schema example: %v", got)
	}
	req, _ := example(t, r.doc, "paths", "/ships", "post", "requestBody", "content", "application/json", "example").(map[string]any)
	if !reflect.DeepEqual(req["Owner"], want) {
		t.Errorf("owner: %v", req["Owner"])
	}
	if g, _ := req["Guest"].(map[string]any); g["name"] != "my" || g["age"] != json.Number("30") {
		t.Errorf("guest: %v", req["Guest"])
	}
	// the existing example of updateShip has no Owner; none is added
	upd, _ := example(t, r.doc, "paths", "/ships/{id}", "put", "requestBody", "content", "application/json", "example").(map[string]any)
	if _, has := upd["Owner"]; has {
		t.Errorf("field added to an existing example: %v", upd)
	}
	n := notes(r, CodeDefaultUnused)
	if len(n) != 1 || !strings.Contains(n[0], `"#/components/schemas/Person"`) {
		t.Errorf("unused: %v", n)
	}
}

// Applied defaults are kept in the dictionary, except operation-scoped ones.
func TestApplyKeepsDefaultsInDictionary(t *testing.T) {
	r := applyToFile(t, "nested.yaml", `{
	  "#/components/schemas/Person": {"name": "my"},
	  "#/components/schemas/Pilot": {"Rank": 1},
	  "Ship.Tags": ["red"],
	  "updateShip.Name": "only here"
	}`, Options{Seed: 1})
	if len(r.res.Fatal) > 0 {
		t.Fatalf("fatal: %v", r.res.Fatal)
	}
	if v := r.dict.Schemas["Person"].Value; !reflect.DeepEqual(v, map[string]any{"name": "my"}) {
		t.Errorf("Person: %v", v)
	}
	if v := r.dict.Schemas["Pilot"].Properties["Rank"].Value; v != json.Number("1") {
		t.Errorf("Pilot.Rank: %v", v)
	}
	if v := r.dict.Schemas["Ship"].Properties["Tags"].Value; !reflect.DeepEqual(v, []any{"red"}) {
		t.Errorf("Ship.Tags: %v", v)
	}
	if v := r.dict.Schemas["Ship"].Properties["Name"].Value; v == "only here" {
		t.Error("an operation-scoped default was kept in the dictionary")
	}
	if r.res.Stats.Dict != 3 {
		t.Errorf("stats: %d, notes %v", r.res.Stats.Dict, notes(r, CodeDictDefault))
	}
}

func TestVerify(t *testing.T) {
	verify := func(defaultsJSON string) []string {
		t.Helper()
		r := applyToFile(t, "lists.yaml", defaultsJSON, Options{Seed: 1})
		if len(r.res.Fatal) > 0 {
			t.Fatalf("fatal: %v", r.res.Fatal)
		}
		defs, _ := defaults.Parse([]byte(defaultsJSON))
		return Verify(r.out, defs, r.res)
	}
	if p := verify(`{"getPlanetById.id": {"bind": "listPlanets", "pointer": "/0/Id"}, "getPlanet.x-apitest-verify": false}`); len(p) != 0 {
		t.Errorf("problems for correct defaults: %v", p)
	}
	for _, c := range []struct{ defaults, want string }{
		{`{"getPlanetById.id": {"bind": "listPlanets", "pointer": "/0/Nope"}}`, "nothing at /0/Nope"},
		{`{"noSuchField": 1}`, "DEFAULT_UNUSED"},
	} {
		p := verify(c.defaults)
		if len(p) != 1 || !strings.Contains(p[0], c.want) {
			t.Errorf("%s: %v", c.defaults, p)
		}
	}
}

// A path default of a parameter that holds a record key selects the record;
// a shared parameter object is then no conflict, the records give every
// path its value.
func TestApplyRecordKeySharedParam(t *testing.T) {
	defs := `{"/Dock/id/{id}": 7, "/Ship/id/{id}": 31}`
	without := applyToFile(t, "records.yaml", defs, Options{Seed: 1})
	if !hasNote(without.res, CodeSharedParam) {
		t.Fatalf("without RecordKey: %v", without.res.Notes)
	}
	keyed := applyToFile(t, "records.yaml", defs, Options{Seed: 1, RecordKey: func(*spec.Operation, string) bool { return true }})
	if hasNote(keyed.res, CodeSharedParam) || len(keyed.res.Fatal) > 0 {
		t.Errorf("with RecordKey: %v %v", keyed.res.Notes, keyed.res.Fatal)
	}
	if !strings.Contains(fmt.Sprint(keyed.res.Notes), "selects the record") {
		t.Errorf("notes: %v", keyed.res.Notes)
	}
}

func hasNote(r *Result, code string) bool {
	for _, n := range r.Notes {
		if n.Code == code {
			return true
		}
	}
	return false
}
