package scenario

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseScenario(t *testing.T) {
	sc, err := Parse([]byte(`
order: [Dock, Ship]
records:
  dock1: {resource: Dock}
  ship1: {resource: Ship, refs: {Dock: dock1}}
steps:
  - id: dock1.create
    call: POST /docks
    body: {name: North, opened: 2026-01-15, capacity: 4}
    save: {dock1.dockId: /id}
    expect: 201
    unique: [/name]
  - id: ship1.create
    call: createShip
    path: {dockId: '{{dock1.dockId}}'}
    header: {If-Match: '"1"'}
    query: {dry: false}
    body: {name: 'Comet of {{dock1.dockId}}'}
    save: {ship1.loc: header Location, ship1.name: request /name}
    token: default
    expect: 201
  - id: ship1.create.again
    call: createShip
    path: {dockId: '{{dock1.dockId}}'}
    bodyFrom: ship1.create
    token: none
    expect: 401
  - id: ship.list
    call: GET /ships
    expect: 200
    check: {equals: ship1.create, contains: {key: /id, values: ['{{ship1.loc}}']}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Steps) != 4 || sc.Order[1] != "Ship" || len(sc.Records) != 2 || sc.Records[1].Refs["Dock"] != "dock1" {
		t.Fatalf("parsed: %+v", sc)
	}
	d := sc.Step("dock1.create")
	body := d.Body.(map[string]any)
	if body["opened"] != "2026-01-15" {
		t.Errorf("a date stays a string: %#v", body["opened"])
	}
	if _, ok := body["capacity"].(json.Number); !ok {
		t.Errorf("numbers are json.Number: %#v", body["capacity"])
	}
	if d.Line == 0 || sc.Step("ship1.create").Token != "" || sc.Step("nope") != nil {
		t.Errorf("line %d, token %q", d.Line, sc.Step("ship1.create").Token)
	}
	if got := sc.Step("ship1.create").Uses(); len(got) != 1 || got[0] != "dock1.dockId" {
		t.Errorf("uses: %v", got)
	}
	// encode and read back
	b, err := sc.Encode(" head")
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(b)
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	if len(again.Steps) != 4 || again.Step("ship.list").Contains.Key != "/id" || again.Step("ship1.create.again").BodyFrom != "ship1.create" {
		t.Errorf("round trip:\n%s", b)
	}
	// Map gives what ParseStep reads
	m := sc.Step("ship.list").Map()
	st, err := ParseStep(m)
	if err != nil || st.Equals != "ship1.create" {
		t.Errorf("Map/ParseStep: %v %+v", err, st)
	}
	path := filepath.Join(t.TempDir(), "s.yaml")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Error(err)
	}
	if _, err := Load(path + ".missing"); err == nil {
		t.Error("a missing file must fail")
	}
}

func TestParseScenarioErrors(t *testing.T) {
	tests := []struct{ text, want string }{
		{"- a", "mapping with the key"},
		{"other: 1", `unknown key "other"`},
		{"steps: 1", `"steps" must be a list`},
		{"steps: [1]", "a step is a mapping"},
		{"steps: [{call: x, expect: 200}]", `"id" is missing`},
		{"steps: [{id: 'a/b', call: x, expect: 200}]", "letters, digits"},
		{"steps: [{id: a, expect: 200}]", `"call" is missing`},
		{"steps: [{id: a, call: x}]", `"expect" must be an HTTP status`},
		{"steps: [{id: a, call: x, expect: 200, nope: 1}]", `unknown key "nope"`},
		{"steps: [{id: a, call: x, expect: 200, path: 1}]", `"path" must be a mapping`},
		{"steps: [{id: a, call: x, expect: 200, body: {}, bodyFrom: b}]", "exclude each other"},
		{"steps: [{id: a, call: x, expect: 200, save: 1}]", `"save" must be a mapping`},
		{"steps: [{id: a, call: x, expect: 200, save: {v: id}}]", "is no source"},
		{"steps: [{id: a, call: x, expect: 200, token: admin}]", "token \"admin\""},
		{"steps: [{id: a, call: x, expect: 200, check: 1}]", `"check" must be a mapping`},
		{"steps: [{id: a, call: x, expect: 200, check: {nope: 1}}]", `check: unknown key "nope"`},
		{"steps: [{id: a, call: x, expect: 200, check: {contains: 1}}]", "check.contains must be"},
		{"steps: [{id: a, call: x, expect: 200, check: {contains: {key: id, values: [1]}}}]", "check.contains needs key"},
		{"steps: [{id: a, call: x, expect: 200, unique: 1}]", `"unique" must be a list`},
		{"steps: [{id: a, call: x, expect: 200, unique: [name]}]", "is no JSON pointer"},
		{"steps: [{id: a, call: x, expect: 200}, {id: a, call: x, expect: 200}]", "the id is used twice"},
		{"steps: [{id: a, call: x, expect: 200, bodyFrom: b}]", "names no step above"},
		{"steps: [{id: b, call: x, expect: 200}, {id: a, call: x, expect: 200, bodyFrom: b}]", "that step sends no body"},
		{"steps: [{id: a, call: x, expect: 200, path: {p: '{{v}}'}}]", "{{v}} is saved by no step above"},
		{"steps: [{id: a, call: x, expect: 200}]\n bad: [", "did not find"},
	}
	for _, tt := range tests {
		_, err := Parse([]byte(tt.text))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: got %v, want %q", tt.text, err, tt.want)
		}
	}
}

func TestFillAndNames(t *testing.T) {
	vars := map[string]any{"a.id": json.Number("7"), "b.code": "X1"}
	v := map[string]any{"id": "{{a.id}}", "text": "dock {{a.id}} / {{b.code}}", "list": []any{"{{b.code}}", 3}, "miss": "{{c}}", "part": "x{{d}}"}
	out, missing := Fill(v, vars)
	m := out.(map[string]any)
	if m["id"] != json.Number("7") || m["text"] != "dock 7 / X1" || m["list"].([]any)[0] != "X1" {
		t.Errorf("fill: %#v", m)
	}
	if len(missing) != 2 {
		t.Errorf("missing: %v", missing)
	}
	if names := Names(v); len(names) != 4 {
		t.Errorf("names: %v", names)
	}
	for _, x := range []struct {
		v    any
		want string
	}{{true, "true"}, {nil, ""}, {3, "3"}, {1.5, "1.5"}, {[]any{1}, "[1]"}, {json.Number("2"), "2"}} {
		if got := Text(x.v); got != x.want {
			t.Errorf("Text(%v) = %q", x.v, got)
		}
	}
	c := Clone(map[string]any{"a": []any{map[string]any{"b": 1}}}).(map[string]any)
	c["a"].([]any)[0].(map[string]any)["b"] = 2
}

func TestTemplated(t *testing.T) {
	vars := map[string]any{"dock1.dockId": json.Number("1"), "ship1.code": "SHIP-0001", "x.year": json.Number("1965")}
	body := map[string]any{
		"id": json.Number("1"), "capacity": json.Number("1"), "dockId": json.Number("1"),
		"code": "SHIP-0001", "note": "SHIP-0001", "year": json.Number("1965"),
		"list": []any{map[string]any{"id": json.Number("1")}},
	}
	got := Templated(body, vars).(map[string]any)
	want := map[string]any{"id": "{{dock1.dockId}}", "capacity": json.Number("1"), "dockId": "{{dock1.dockId}}", "code": "{{ship1.code}}", "note": "{{ship1.code}}", "year": "{{x.year}}"}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %#v, want %#v", k, got[k], w)
		}
	}
	if got["list"].([]any)[0].(map[string]any)["id"] != "{{dock1.dockId}}" {
		t.Errorf("list: %#v", got["list"])
	}
	if h := TemplatedHeader("Location", "/v1/docks/1", vars); h != "/v1/docks/{{dock1.dockId}}" {
		t.Errorf("location: %s", h)
	}
	if h := TemplatedHeader("X-Code", "SHIP-0001", vars); h != "{{ship1.code}}" {
		t.Errorf("header: %s", h)
	}
	if h := TemplatedHeader("X", "other", vars); h != "other" {
		t.Errorf("header: %s", h)
	}
	if tg := TemplatedTarget("/docks/1/ships?code=SHIP-0001&x=2", vars); tg != "/docks/{{dock1.dockId}}/ships?code={{ship1.code}}&x=2" {
		t.Errorf("target: %s", tg)
	}
	if g := Generalize("/items/3/ships/0/name"); g != "/items/*/ships/*/name" {
		t.Errorf("generalize: %s", g)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	s := &Snapshot{
		Kind: KindRegressionPre, Spec: "api.yaml", SpecVersion: "1.0", BaseURL: "http://x", Created: "2026-10-13T10:00:00Z",
		Scenario: "s.yaml", Pre: "p.yaml", Seed: "7", Reread: true,
		Sample: []SampleSet{{Name: "dock", Candidates: 9, LeftOut: map[string]int{"without name": 2}, Picked: []map[string]any{{"dockId": json.Number("3")}}}},
		Steps: []*Shot{
			{ID: "a", Op: "getDock", Request: "GET /docks/3", Status: 200, Headers: map[string]string{"Content-Type": "application/json"},
				Body: map[string]any{"opened": "2026-01-15", "n": json.Number("1.5")}, HasBody: true, Volatile: []string{"/updatedAt"},
				Lists: map[string]string{"/items": "id"}, DurationMs: 12, Problems: []string{"bad"}},
			{ID: "b", Request: "GET /x", Error: "timeout"},
			{ID: "c", Request: "GET /y", Status: 204, HasBody: true},
		},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "snap.yaml")
	if err := s.Save(path, " head"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != s.Kind || !got.Reread || got.Seed != "7" || len(got.Sample) != 1 || got.Sample[0].LeftOut["without name"] != 2 || len(got.Steps) != 3 {
		t.Fatalf("round trip: %+v", got)
	}
	a := got.Shot("a")
	if a.Body.(map[string]any)["opened"] != "2026-01-15" || a.DurationMs != 12 || a.Lists["/items"] != "id" || a.Problems[0] != "bad" {
		t.Errorf("shot: %+v", a)
	}
	if got.Shot("b").Error != "timeout" || got.Shot("zz") != nil || !got.Shot("c").HasBody || got.Shot("c").Body != nil {
		t.Errorf("shots: %+v %+v", got.Shot("b"), got.Shot("c"))
	}
	for _, bad := range []string{"- 1", "steps: [1]", "steps: [{status: 200}]"} {
		if _, err := ParseSnapshot([]byte(bad)); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
	if _, err := LoadSnapshot(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("a missing snapshot must fail")
	}
	if err := WriteFile(filepath.Join(path, "x"), nil); err == nil {
		t.Error("writing below a file must fail")
	}
}

func TestSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contract-settings.yaml")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("spec: ../api.yaml\nbaseURL: env:SET_URL\ntokens: {default: env:SET_TOKEN, forbidden: literal}\nrecords: 3\nunique: true\nlearnFrom: {baseURL: http://dev}\n")
	s, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Rel(s.Spec) != filepath.Join(dir, "../api.yaml") || s.Rel("/abs") != "/abs" || s.Records != 3 || !s.Unique || s.LearnFrom.BaseURL != "http://dev" {
		t.Errorf("settings: %+v", s)
	}
	if !Literal(s.Tokens.Forbidden) || Literal(s.Tokens.Default) || Literal("") {
		t.Error("Literal")
	}
	t.Setenv("SET_URL", "http://x")
	if v, err := Expand(s.BaseURL); err != nil || v != "http://x" {
		t.Errorf("expand: %v %q", err, v)
	}
	if _, err := Expand("env:SET_MISSING_VAR"); err == nil {
		t.Error("an unset variable must fail")
	}
	if _, err := Expand("env: "); err == nil {
		t.Error("env: without name must fail")
	}
	if v, _ := Expand("plain"); v != "plain" {
		t.Error("a plain value stays")
	}
	for text, want := range map[string]string{"nope: 1": "unknown key", "- 1": "must be a mapping", "records: -1": "must not be negative", "records: x": "cannot unmarshal", "a: [": "did not find"} {
		write(text)
		if _, err := LoadSettings(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
	if _, err := LoadSettings(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing settings must fail")
	}
	var nilSettings *Settings
	if nilSettings.Rel("x") != "x" {
		t.Error("Rel on nil")
	}
	if v, err := ReadYAML(filepath.Join(dir, "missing")); err == nil || v != nil {
		t.Error("ReadYAML of a missing file")
	}
	b, err := EncodeValue(" h", map[string]any{"a": []any{1, "x"}, "b": "multi\nline", "c": map[string]string{"k": "v"}, "d": []string{"s"}, "e": 1.5, "f": int64(2), "g": struct{ X int }{3}})
	if err != nil || !strings.Contains(string(b), "multi") {
		t.Errorf("EncodeValue: %v\n%s", err, b)
	}
}

func TestDiff(t *testing.T) {
	pre := &Snapshot{Created: "2026-10-13T10:00:00Z", Steps: []*Shot{
		{ID: "a", Request: "GET /docks/1", Status: 200, HasBody: true, DurationMs: 50,
			Headers: map[string]string{"Content-Type": "application/json", "X-Gone": "1", "X-V": "1"},
			Body: map[string]any{"id": json.Number("1"), "name": "North", "removed": true, "updatedAt": "t1", "tags": []any{"a", "b"},
				"nested": map[string]any{"x": json.Number("1")}, "kind": map[string]any{"a": 1}}},
		{ID: "b", Request: "GET /docks", Status: 200, HasBody: true, DurationMs: 50,
			Body: map[string]any{"items": []any{
				map[string]any{"id": json.Number("1"), "name": "North"},
				map[string]any{"id": json.Number("2"), "name": "South"},
			}}},
		{ID: "c", Request: "GET /x", Status: 200},
		{ID: "d", Request: "GET /gone", Status: 200},
		{ID: "e", Request: "GET /err", Status: 200},
		{ID: "f", Request: "GET /prefail", Error: "timeout"},
		{ID: "g", Request: "GET /schema", Status: 200, DurationMs: 10},
	}}
	post := &Snapshot{Steps: []*Shot{
		{ID: "a", Status: 200, HasBody: true, DurationMs: 900,
			Headers: map[string]string{"content-type": "application/json", "X-V": "2"},
			Body: map[string]any{"id": json.Number("1"), "name": "north", "added": 1, "updatedAt": "t2", "tags": []any{"a"},
				"nested": map[string]any{"x": "1"}, "kind": []any{1}}},
		{ID: "b", Status: 200, HasBody: true,
			Body: map[string]any{"items": []any{
				map[string]any{"id": json.Number("2"), "name": "South!"},
				map[string]any{"id": json.Number("3"), "name": "East"},
			}}},
		{ID: "c", Status: 404},
		{ID: "e", Error: "connection refused"},
		{ID: "f", Status: 200},
		{ID: "g", Status: 200, Problems: []string{"/x: wrong type"}, DurationMs: 10},
	}}
	fs := Diff(pre, post, DiffOptions{Ignore: []string{"updatedAt"}, FailOn: []string{FailSlower}})
	codes := map[string]int{}
	for _, f := range fs {
		codes[f.Code+" "+f.Pointer]++
	}
	for _, want := range []string{
		FieldChanged + " /name", FieldRemoved + " /removed", FieldAdded + " /added", FieldChanged + " /tags",
		FieldChanged + " /nested/x", FieldChanged + " /kind", HeaderChanged + " header X-Gone", HeaderChanged + " header X-V",
		ElementMissing + " /items/[id=1]", ElementOther + " /items/[id=3]", FieldChanged + " /items/[id=2]/name",
		StatusChanged + " ", StepMissing + " ", RequestError + " ", SchemaViolation + " ", Slower + " ",
	} {
		if codes[want] == 0 {
			t.Errorf("missing finding %q in %v", want, codes)
		}
	}
	if codes[FieldChanged+" /updatedAt"] > 0 {
		t.Error("ignored field compared")
	}
	if !Failed(fs) {
		t.Error("findings must fail")
	}
	for _, f := range fs {
		if (f.Code == FieldAdded || f.Code == ElementOther) && f.Fails {
			t.Errorf("%s must not fail without FailOn", f.Code)
		}
		if f.Code == Slower && !f.Fails {
			t.Error("SLOWER fails with FailOn slower")
		}
	}
	// IgnoreCase, pointers with * and list keys of the snapshot
	pre2 := &Snapshot{Steps: []*Shot{{ID: "a", Status: 200, HasBody: true, Lists: map[string]string{"/l": "code"},
		Body: map[string]any{"l": []any{map[string]any{"code": "A", "v": "x", "t": 1}}, "s": "Abc", "m": []any{1, 2}}}}}
	post2 := &Snapshot{Steps: []*Shot{{ID: "a", Status: 200, HasBody: true,
		Body: map[string]any{"l": []any{map[string]any{"code": "A", "v": "X", "t": 2}}, "s": "abc", "m": []any{1, 3}}}}}
	fs = Diff(pre2, post2, DiffOptions{IgnoreCase: true, Ignore: []string{"/l/*/t", "/m/1"}})
	if len(fs) != 0 {
		t.Errorf("want no findings, got %+v", fs)
	}
	if fs := Diff(pre2, &Snapshot{Steps: []*Shot{{ID: "a", Status: 200, HasBody: true, Body: "text"}}}, DiffOptions{}); len(fs) != 1 || fs[0].Code != FieldChanged {
		t.Errorf("type change: %+v", fs)
	}
	if fs := Diff(&Snapshot{Steps: []*Shot{{ID: "a", Status: 200, HasBody: true, Body: "x"}}}, &Snapshot{Steps: []*Shot{{ID: "a", Status: 200, HasBody: true, Body: []any{1}}}}, DiffOptions{}); len(fs) != 1 {
		t.Errorf("scalar → list: %+v", fs)
	}
	if fs := Diff(&Snapshot{Steps: []*Shot{{ID: "a", Status: 200, HasBody: true, Body: []any{1}}}}, &Snapshot{Steps: []*Shot{{ID: "a", Status: 200, HasBody: true, Body: "x"}}}, DiffOptions{}); len(fs) != 1 {
		t.Errorf("list → scalar: %+v", fs)
	}
	if fs := Diff(&Snapshot{Steps: []*Shot{{ID: "a", Status: 200, HasBody: true, Body: "x"}}}, &Snapshot{Steps: []*Shot{{ID: "a", Status: 200, HasBody: true, Body: map[string]any{}}}}, DiffOptions{}); len(fs) != 1 || render(nil) != "null" {
		t.Errorf("scalar → object: %+v", fs)
	}
}
