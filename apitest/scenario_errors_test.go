package apitest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const moonsSpec = "../testdata/specs/scenario_errors.yaml"

// moonsScenario creates, reads, lists and deletes one moon; the read after
// the delete is the only error step of the file.
const moonsScenario = `
steps:
  - id: moon1.create
    call: POST /moons
    body: {name: Luna, radius: 1737, kind: rock}
    save: {moon1.id: /id}
    expect: 201
  - id: moon1.read
    call: getMoon
    path: {moonId: '{{moon1.id}}'}
    expect: 200
  - id: moon.list
    call: GET /moons
    expect: 200
  - id: moon1.update
    call: updateMoon
    path: {moonId: '{{moon1.id}}'}
    body: {name: Luna, radius: 1738, kind: ice}
    expect: 200
  - id: moon1.delete
    call: DELETE /moons/{moonId}
    path: {moonId: '{{moon1.id}}'}
    expect: 204
  - id: moon1.read.afterDelete
    call: getMoon
    path: {moonId: '{{moon1.id}}'}
    expect: 404
`

// moonFaults inject one bug each into the Moon API.
type moonFaults struct {
	duplicates bool // a second moon of the same name is created (201 instead of 409)
	crash      bool // invalid input answers 500 instead of 400
	noAuth     bool // requests without token are accepted
	hangup     bool // invalid input closes the connection without an answer (the API crashed)
}

// moonAPI is the Moon API of scenario_errors.yaml: 400 for invalid input,
// 401 without the token "moon-token", 404 for unknown moons, 409 for a
// second moon of the same name.
func moonAPI(f moonFaults) http.Handler {
	var mu sync.Mutex
	moons := map[int]map[string]any{}
	next := 1
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	bad := func(w http.ResponseWriter, msg string) {
		if f.hangup {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
		}
		if f.crash {
			reply(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
			return
		}
		reply(w, http.StatusBadRequest, map[string]any{"error": msg})
	}
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") == "Bearer moon-token" || f.noAuth && r.Header.Get("Authorization") == "" {
			return true
		}
		reply(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return false
	}
	id := func(w http.ResponseWriter, r *http.Request) (int, bool) {
		n, err := strconv.Atoi(r.PathValue("moonId"))
		if err != nil {
			bad(w, "moonId is no number")
			return 0, false
		}
		if moons[n] == nil {
			reply(w, http.StatusNotFound, map[string]any{"error": "no such moon"})
			return 0, false
		}
		return n, true
	}
	valid := func(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
		var in map[string]any
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			bad(w, "invalid json")
			return nil, false
		}
		name, ok := in["name"].(string)
		radius, rok := in["radius"].(float64)
		kind, kok := in["kind"].(string)
		switch {
		case !ok || name == "" || len(name) > 20:
			bad(w, "name is required, at most 20 characters")
		case !rok || radius < 1:
			bad(w, "radius is required, at least 1")
		case in["kind"] != nil && (!kok || kind != "rock" && kind != "ice"):
			bad(w, "kind is rock or ice")
		default:
			return in, true
		}
		return nil, false
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /moons/{moonId}", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		n, ok := id(w, r)
		if !ok {
			return
		}
		in, ok := valid(w, r)
		if !ok {
			return
		}
		in["id"] = n
		moons[n] = in
		reply(w, http.StatusOK, in)
	})
	mux.HandleFunc("POST /moons", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		in, ok := valid(w, r)
		if !ok {
			return
		}
		name := in["name"]
		mu.Lock()
		defer mu.Unlock()
		for _, m := range moons {
			if m["name"] == name && !f.duplicates {
				reply(w, http.StatusConflict, map[string]any{"error": fmt.Sprintf("moon %v exists", name)})
				return
			}
		}
		in["id"] = next
		moons[next] = in
		next++
		reply(w, http.StatusCreated, in)
	})
	mux.HandleFunc("GET /moons", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		out := []any{}
		for _, m := range moons {
			out = append(out, m)
		}
		reply(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /moons/{moonId}", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if n, ok := id(w, r); ok {
			reply(w, http.StatusOK, moons[n])
		}
	})
	mux.HandleFunc("DELETE /moons/{moonId}", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if n, ok := id(w, r); ok {
			delete(moons, n)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	return mux
}

func moonsConfig(t *testing.T, f moonFaults, errs ...string) ScenarioConfig {
	t.Helper()
	sc, _ := scenarioFiles(t, moonsScenario)
	return ScenarioConfig{
		Config:   Config{SpecPath: moonsSpec, Handler: moonAPI(f), Token: StaticToken("moon-token"), DisableReports: true},
		Scenario: sc,
		Errors:   errs,
	}
}

func caseNames(res *Result) []string {
	var out []string
	for _, c := range res.Cases {
		out = append(out, c.Name)
	}
	return out
}

func TestScenarioGeneratedErrors(t *testing.T) {
	res, ft := runScenarioFake(t, moonsConfig(t, moonFaults{}, "409", "404", "401", "invalid"))
	if res.Failed {
		t.Fatalf("run failed:\n%s", ft.output())
	}
	want := []string{
		// the clean state: the steps before the first DELETE
		"Moon/moon1.create", "Moon/moon1.read", "Moon/moon.list", "Moon/moon1.update",
		// 409, 404 (getMoon has its 404 step in the file), 401, invalid
		"Moon/moon1.create.conflict",
		"Moon/moon1.update.notFound", "Moon/moon1.delete.notFound",
		"Moon/moon1.create.unauthorized", "Moon/moon1.create.invalidToken",
		"Moon/moon1.read.unauthorized", "Moon/moon1.read.invalidToken",
		"Moon/moon.list.unauthorized", "Moon/moon.list.invalidToken",
		"Moon/moon1.update.unauthorized", "Moon/moon1.update.invalidToken",
		"Moon/moon1.delete.unauthorized", "Moon/moon1.delete.invalidToken",
		// a POST: every field null; a PUT: one value per step
		"Moon/moon1.create.invalidNull",
		"Moon/moon1.read.invalidParam",
		"Moon/moon1.update.invalidMissing", "Moon/moon1.update.invalidType", "Moon/moon1.update.invalidValue", "Moon/moon1.update.invalidParam",
		"Moon/moon1.delete.invalidParam",
		// the DELETEs and the steps after them
		"Moon/moon1.delete", "Moon/moon1.read.afterDelete",
	}
	if got := caseNames(res); !slices.Equal(got, want) {
		t.Errorf("steps:\n got %v\nwant %v", got, want)
	}

	// without Errors the scenario runs as written
	res, _ = runScenarioFake(t, moonsConfig(t, moonFaults{}))
	if got := caseNames(res); len(got) != 6 {
		t.Errorf("without Errors want the 6 steps of the file, got %v", got)
	}
	// Errors selects the phases
	res, _ = runScenarioFake(t, moonsConfig(t, moonFaults{}, "409"))
	if got := caseNames(res); len(got) != 6 || got[4] != "Moon/moon1.create.conflict" {
		t.Errorf("Errors 409: want the clean steps, the conflict and the delete, got %v", got)
	}
}

// Each fault makes exactly the steps that should find it fail.
func TestScenarioGeneratedErrorsFaults(t *testing.T) {
	all := []string{"409", "404", "401", "invalid"}
	for _, tt := range []struct {
		name   string
		fault  moonFaults
		failed []string
	}{
		{"duplicates", moonFaults{duplicates: true}, []string{"Moon/moon1.create.conflict"}},
		{"crash", moonFaults{crash: true}, []string{
			"Moon/moon1.create.invalidNull", "Moon/moon1.read.invalidParam",
			"Moon/moon1.update.invalidMissing", "Moon/moon1.update.invalidType", "Moon/moon1.update.invalidValue", "Moon/moon1.update.invalidParam",
			"Moon/moon1.delete.invalidParam",
		}},
		{"noAuth", moonFaults{noAuth: true}, []string{
			"Moon/moon1.create.unauthorized", "Moon/moon1.read.unauthorized", "Moon/moon.list.unauthorized",
			"Moon/moon1.update.unauthorized", "Moon/moon1.delete.unauthorized",
			// the delete without token deleted the moon
			"Moon/moon1.delete",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, ft := runScenarioFake(t, moonsConfig(t, tt.fault, all...))
			var failed []string
			for _, c := range res.Cases {
				if c.Status == StatusFailed {
					failed = append(failed, c.Name)
				}
			}
			if !slices.Equal(failed, tt.failed) {
				t.Errorf("failed:\n got %v\nwant %v\n%s", failed, tt.failed, ft.output())
			}
		})
	}
}

func TestScenarioInvalidStatus(t *testing.T) {
	cfg := moonsConfig(t, moonFaults{crash: true}, "invalid")
	cfg.InvalidStatus = []string{"400", "422", "500"}
	res, ft := runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("InvalidStatus with 500 must accept the 500:\n%s", ft.output())
	}
	got := statusOf(res)
	if got["Moon/moon1.create.invalidNull"] != StatusPassed {
		t.Errorf("invalidNull: %v", got)
	}

	// TolerateErrorCases: a 500 the statuses do not accept is TOLERATED
	cfg = moonsConfig(t, moonFaults{crash: true}, "invalid")
	cfg.TolerateErrorCases = true
	res, ft = runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("TolerateErrorCases must tolerate the invalid steps:\n%s", ft.output())
	}
	for _, c := range res.Cases {
		if strings.Contains(c.Name, ".invalid") && (c.Status != StatusTolerated || c.Tolerated != StatusFailed) {
			t.Errorf("%s: %s (%s), want TOLERATED (FAILED)", c.Name, c.Status, c.Tolerated)
		}
	}

	cfg = moonsConfig(t, moonFaults{crash: true}, "invalid")
	cfg.InvalidStatus = []string{"4XX", "x"}
	res, ft = runScenarioFake(t, cfg)
	if !res.Failed || !strings.Contains(ft.output(), `InvalidStatus: "x"`) {
		t.Errorf("an invalid InvalidStatus entry must fail the configuration:\n%s", ft.output())
	}
}

func TestScenarioErrorsUnused(t *testing.T) {
	res, ft := runScenarioFake(t, moonsConfig(t, moonFaults{}, "404", "500"))
	if res.Failed {
		t.Fatalf("run failed:\n%s", ft.output())
	}
	if out := ft.output(); !strings.Contains(out, `no step for "500"`) || strings.Contains(out, `no step for "404"`) {
		t.Errorf("want a warning for 500 only:\n%s", out)
	}
}

func TestScenarioErrorsNotInUpdate(t *testing.T) {
	cfg := moonsConfig(t, moonFaults{}, "409", "404", "401", "invalid")
	cfg.Update = true
	res, ft := runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("update failed:\n%s", ft.output())
	}
	if got := caseNames(res); len(got) != 6 {
		t.Errorf("Update must not generate steps, got %v", got)
	}
}

func TestScenarioInvalidValues(t *testing.T) {
	maxLen, lo, hi := uint64(3), 1.0, 9.0
	for _, tt := range []struct {
		name  string
		sch   *openapi3.Schema
		empty bool
		rule  string // "" when no rule can be broken
	}{
		{"enum", &openapi3.Schema{Type: &openapi3.Types{"string"}, Enum: []any{"a"}}, true, "its enum"},
		{"uuid", &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "uuid"}, true, "format uuid"},
		{"date", &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "date"}, true, "format date"},
		{"email", &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "email"}, true, "format email"},
		{"maxLength", &openapi3.Schema{Type: &openapi3.Types{"string"}, MaxLength: &maxLen}, true, "maxLength 3"},
		{"pattern", &openapi3.Schema{Type: &openapi3.Types{"string"}, Pattern: `^[a-z]+$`}, true, "pattern ^[a-z]+$"},
		{"minLength", &openapi3.Schema{Type: &openapi3.Types{"string"}, MinLength: 2}, true, "minLength 2"},
		{"minLength in path", &openapi3.Schema{Type: &openapi3.Types{"string"}, MinLength: 2}, false, ""},
		{"plain string", &openapi3.Schema{Type: &openapi3.Types{"string"}}, true, ""},
		{"integer enum", &openapi3.Schema{Type: &openapi3.Types{"integer"}, Enum: []any{1}}, true, "its enum"},
		{"maximum", &openapi3.Schema{Type: &openapi3.Types{"integer"}, Max: &hi}, true, "maximum 9"},
		{"minimum", &openapi3.Schema{Type: &openapi3.Types{"number"}, Min: &lo}, true, "minimum 1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, rule, ok := breakRule(tt.sch, tt.empty)
			if ok != (tt.rule != "") || rule != tt.rule {
				t.Errorf("got %q %v, want %q", rule, ok, tt.rule)
			}
		})
	}
	for _, typ := range []string{"integer", "number", "boolean", "string", "array", "object"} {
		if _, ok := wrongType(&openapi3.Schema{Type: &openapi3.Types{typ}}); !ok {
			t.Errorf("wrongType %s: no value", typ)
		}
	}
	if _, ok := wrongType(&openapi3.Schema{}); ok {
		t.Error("wrongType without type must find none")
	}
}

// Every step sends the valid token, except the 401 steps: unauthorized
// none, invalidToken the first half of it.
func TestScenarioGeneratedErrorsTokens(t *testing.T) {
	cfg := moonsConfig(t, moonFaults{}, "409", "404", "401", "invalid")
	var mu sync.Mutex
	sent := map[string]string{}
	cfg.Hooks.BeforeRequest = func(_ context.Context, c *Case, req *http.Request) error {
		mu.Lock()
		defer mu.Unlock()
		sent[c.Name] = req.Header.Get("Authorization")
		return nil
	}
	res, ft := runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("run failed:\n%s", ft.output())
	}
	for _, c := range res.Cases {
		want := "Bearer moon-token"
		switch {
		case strings.HasSuffix(c.Name, ".unauthorized"):
			want = ""
		case strings.HasSuffix(c.Name, ".invalidToken"):
			want = "Bearer moon-"
		}
		if got := sent[c.Name]; got != want {
			t.Errorf("%s sent Authorization %q, want %q", c.Name, got, want)
		}
	}
}

// A spec without any security: Config.Token goes as a bearer token to
// every operation, and the 401 steps are generated for the operations
// that document 401.
func TestScenarioErrorsSpecWithoutSecurity(t *testing.T) {
	raw, err := os.ReadFile(moonsSpec)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "security:\n  - bearer: []\n", "", 1)
	text = strings.Replace(text, "  securitySchemes:\n    bearer: {type: http, scheme: bearer}\n", "", 1)
	if strings.Contains(text, "bearer") {
		t.Fatal("the spec still declares security")
	}
	specPath := filepath.Join(t.TempDir(), "moons.yaml")
	if err := os.WriteFile(specPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := moonsConfig(t, moonFaults{}, "401")
	cfg.SpecPath = specPath
	var mu sync.Mutex
	sent := map[string]string{}
	cfg.Hooks.BeforeRequest = func(_ context.Context, c *Case, req *http.Request) error {
		mu.Lock()
		defer mu.Unlock()
		sent[c.Name] = req.Header.Get("Authorization")
		return nil
	}
	res, ft := runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("run failed:\n%s", ft.output())
	}
	if !strings.Contains(ft.output(), "the spec declares no security") {
		t.Errorf("want the warning about the assumed bearer token:\n%s", ft.output())
	}
	got := statusOf(res)
	for _, name := range []string{"Moon/moon1.read.unauthorized", "Moon/moon1.read.invalidToken", "Moon/moon1.create.unauthorized"} {
		if got[name] != StatusPassed {
			t.Errorf("%s: %q, want PASSED (%v)", name, got[name], got)
		}
	}
	if sent["Moon/moon1.create"] != "Bearer moon-token" || sent["Moon/moon1.read.unauthorized"] != "" {
		t.Errorf("tokens sent: %v", sent)
	}
}

// An API that closes the connection on invalid input (it crashed): the
// step is ERROR with "no answer", the report marks it, and
// TolerateErrorCases tolerates it.
func TestScenarioInvalidNoAnswer(t *testing.T) {
	cfg := moonsConfig(t, moonFaults{hangup: true}, "invalid")
	res, ft := runScenarioFake(t, cfg)
	if !res.Failed {
		t.Fatalf("a crash must fail the run:\n%s", ft.output())
	}
	for _, c := range res.Cases {
		if !strings.Contains(c.Name, ".invalid") {
			continue
		}
		if c.Status != StatusError || !strings.Contains(c.Message, "no answer: the API closed the connection") {
			t.Errorf("%s: %s %q, want ERROR with no answer", c.Name, c.Status, c.Message)
		}
	}

	cfg = moonsConfig(t, moonFaults{hangup: true}, "invalid")
	cfg.TolerateErrorCases = true
	cfg.DisableReports = false
	cfg.ReportPath = filepath.Join(t.TempDir(), "report.md")
	res, ft = runScenarioFake(t, cfg)
	if res.Failed {
		t.Fatalf("TolerateErrorCases must tolerate the crash:\n%s", ft.output())
	}
	for _, c := range res.Cases {
		if strings.Contains(c.Name, ".invalid") && (c.Status != StatusTolerated || c.Tolerated != StatusError) {
			t.Errorf("%s: %s (%s), want TOLERATED (ERROR)", c.Name, c.Status, c.Tolerated)
		}
	}
	md, err := os.ReadFile(cfg.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| no answer |", "tolerated (ERROR, no answer)", "requests got no answer.**"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("report misses %q", want)
		}
	}
	page, err := os.ReadFile(strings.TrimSuffix(cfg.ReportPath, ".md") + ".html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `<span class="crash"`) {
		t.Error("HTML report does not mark the requests without answer")
	}
}
