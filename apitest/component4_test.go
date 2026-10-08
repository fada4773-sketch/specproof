package apitest

// Component tests for specs without declared security, scoped parameters and
// the token of the invalid-token case.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/cases"
)

// A spec without security still gets Config.Token as bearer token, and
// whitespace, line breaks and a "Bearer " prefix are removed from it.
func TestTokenWithoutDeclaredSecurity(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "notes.yaml")
	err := os.WriteFile(specPath, []byte(`
openapi: 3.0.3
info: { title: Notes, version: "1" }
paths:
  /notes:
    get:
      operationId: listNotes
      tags: [Note]
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { type: array, items: { type: string } }
              example: [a]
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	const clean = "aaaa.bbbb.cccc"
	var mu sync.Mutex
	var got []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]string{"a"})
	})
	out := runFake(t, Config{
		SpecPath:       specPath,
		Handler:        handler,
		Token:          StaticToken("Bearer aaaa.\n  bbbb.cccc\n"),
		DisableReports: true,
	})
	if out.res.Failed {
		t.Fatalf("run failed:\n%s", out.ft.output())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("no request")
	}
	for _, h := range got {
		if h != "Bearer "+clean {
			t.Errorf("Authorization = %q, want %q", h, "Bearer "+clean)
		}
	}
	log := out.ft.output()
	for _, want := range []string{"the spec declares no security", "were removed before sending"} {
		if !strings.Contains(log, want) {
			t.Errorf("warning %q missing:\n%s", want, log)
		}
	}
	if strings.Contains(log, clean) {
		t.Error("token appears in the output")
	}
}

// Config.Params keys "<operationId>.<name>" apply to one operation only.
func TestScopedParams(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "regions.yaml")
	err := os.WriteFile(specPath, []byte(`
openapi: 3.0.3
info: { title: Regions, version: "1" }
paths:
  /regions/{id}:
    get:
      operationId: getRegion
      tags: [Region]
      parameters: [{ name: id, in: path, required: true, schema: { type: string } }]
      responses: { "200": { description: ok } }
  /zones/{id}:
    get:
      operationId: getZone
      tags: [Zone]
      parameters: [{ name: id, in: path, required: true, schema: { type: string } }]
      responses: { "200": { description: ok } }
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	paths := map[string]bool{}
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path] = true
		mu.Unlock()
	})
	out := runFake(t, Config{
		SpecPath:       specPath,
		Handler:        handler,
		Params:         map[string]string{"id": "z9", "getRegion.id": "r1", "getRegoin.id": "typo"},
		DisableReports: true,
	})
	if out.res.Failed {
		t.Fatalf("run failed:\n%s", out.ft.output())
	}
	mu.Lock()
	defer mu.Unlock()
	if !paths["/regions/r1"] || !paths["/zones/z9"] {
		t.Errorf("paths = %v", paths)
	}
	if log := out.ft.output(); !strings.Contains(log, `Config.Params key "getRegoin.id" matches no parameter`) {
		t.Errorf("warning about the typo missing:\n%s", log)
	}
}

// The invalid-token case sends the first 100 characters of the real token by
// default, and whatever Config.TamperToken returns otherwise. If the API
// accepts it, the message says so, since the report redacts both tokens.
func TestInvalidToken(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "items.yaml")
	err := os.WriteFile(specPath, []byte(`
openapi: 3.0.3
info: { title: Items, version: "1" }
security: [{ bearer: [] }]
paths:
  /items:
    get:
      operationId: listItems
      tags: [Item]
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { type: array, items: { type: string } }
              example: [a]
        "401": { description: unauthorized }
components:
  securitySchemes:
    bearer: { type: http, scheme: bearer, bearerFormat: JWT }
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	sig := make([]byte, 256) // RS256
	if _, err := rand.Read(sig); err != nil {
		t.Fatal(err)
	}
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	realTok := header + "." + enc.EncodeToString([]byte(`{"sub":"u1","exp":4102444800,"roles":["reader","writer"]}`)) + "." + enc.EncodeToString(sig)

	tests := []struct {
		name     string
		tamper   func(string) string
		want     func(tok string) bool // is tok the expected manipulated token?
		validate bool                  // does the API validate tokens?
		status   Status
		hint     string
	}{
		{"truncated, validating API", nil, func(tok string) bool { return tok == realTok[:100] }, true, StatusPassed, ""},
		{"truncated, API accepts anything", nil, func(tok string) bool { return tok == realTok[:100] }, false, StatusFailed, "truncated copy of the real token"},
		{"signature, validating API", TamperSignature, func(tok string) bool {
			parts := strings.Split(tok, ".")
			return len(parts) == 3 && parts[0] == header && tok != realTok
		}, true, StatusPassed, ""},
		{"signature, API accepts anything", TamperSignature, func(tok string) bool { return strings.HasPrefix(tok, header+".") && tok != realTok }, false, StatusFailed, "token from Config.TamperToken"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var sent []string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
				mu.Lock()
				sent = append(sent, tok)
				mu.Unlock()
				if tok == "" || (tc.validate && tok != realTok) {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([]string{"a"})
			})
			cfg := Config{SpecPath: specPath, Handler: handler, Token: StaticToken(realTok), TamperToken: tc.tamper, ReportPath: filepath.Join(t.TempDir(), "r.md")}
			out := runFake(t, cfg)

			mu.Lock()
			n := 0
			for _, tok := range sent {
				if tc.want(tok) {
					n++
				}
			}
			mu.Unlock()
			if n != 1 {
				t.Errorf("%d expected manipulated tokens among %d requests, want 1", n, len(sent))
			}
			st, msg := statuses(out.res)["Item/listItems/invalid-token"], message(out.res, "Item/listItems/invalid-token")
			if st != tc.status || !strings.Contains(msg, tc.hint) {
				t.Errorf("invalid-token: %s %q, want %s with %q", st, msg, tc.status, tc.hint)
			}
			if strings.Contains(out.report, realTok[:100]) {
				t.Error("report leaks the truncated token")
			}
		})
	}

	// A TamperToken that returns the real token is rejected per case.
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	out := runFake(t, Config{SpecPath: specPath, Handler: handler, Token: StaticToken(realTok), DisableReports: true,
		TamperToken: func(tok string) string { return tok }})
	if st, msg := statuses(out.res)["Item/listItems/invalid-token"], message(out.res, "Item/listItems/invalid-token"); st != StatusError || !strings.Contains(msg, "returned the real token") {
		t.Errorf("TamperToken returning the real token: %s %q", st, msg)
	}
}

// A heuristic binding whose producer returns no value falls back to
// Config.Params instead of skipping the dependents.
func TestHeuristicFallsBackToParams(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "widgets.yaml")
	err := os.WriteFile(specPath, []byte(`
openapi: 3.0.3
info: { title: Widgets, version: "1" }
paths:
  /widgets:
    post:
      operationId: createWidget
      tags: [Widget]
      requestBody:
        content:
          application/json:
            # WidgetCode exists in the schema, but not in the example.
            schema: { type: object, properties: { WidgetCode: { type: string }, Name: { type: string } } }
            example: { Name: w }
      responses:
        "201": { description: created, content: { application/json: { schema: { type: object, properties: { Message: { type: string } } } } } }
  /widgets/{widgetCode}:
    get:
      operationId: getWidget
      tags: [Widget]
      parameters: [{ name: widgetCode, in: path, required: true, schema: { type: string } }]
      responses: { "200": { description: ok } }
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	paths := map[string]bool{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.Method+" "+r.URL.Path] = true
		mu.Unlock()
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Message":"ok"}`))
		}
	})

	out := runFake(t, Config{SpecPath: specPath, Handler: handler, DisableReports: true})
	if st := statuses(out.res)["Widget/getWidget/default"]; st != StatusSkipped {
		t.Errorf("without Params: %s, want SKIPPED", st)
	}

	out = runFake(t, Config{SpecPath: specPath, Handler: handler, DisableReports: true, Params: map[string]string{"widgetCode": "w1"}})
	if st := statuses(out.res)["Widget/getWidget/default"]; st != StatusPassed {
		t.Errorf("with Params: %s %s", st, message(out.res, "Widget/getWidget/default"))
	}
	mu.Lock()
	defer mu.Unlock()
	if !paths["GET /widgets/w1"] {
		t.Errorf("paths = %v", paths)
	}
	if log := out.ft.output(); !strings.Contains(log, "Config.Params is used instead") {
		t.Errorf("warning missing:\n%s", log)
	}
}

// SkipAuthCases leaves the listed authentication cases out completely; a
// hook can still set headers such as Accept for the others.
func TestSkipAuthCasesAndAcceptHook(t *testing.T) {
	if AuthUnauthorized != cases.UnauthorizedExample || AuthInvalidToken != cases.InvalidTokenExample || AuthForbidden != cases.ForbiddenExample {
		t.Fatal("exported auth case names differ from the internal ones")
	}
	specPath := filepath.Join(t.TempDir(), "items.yaml")
	err := os.WriteFile(specPath, []byte(`
openapi: 3.0.3
info: { title: Items, version: "1" }
security: [{ bearer: [] }]
paths:
  /items:
    get:
      operationId: listItems
      tags: [Item]
      responses:
        "200": { description: ok, content: { application/json: { schema: { type: array, items: { type: string } }, example: [a] } } }
        "401": { description: unauthorized }
components:
  securitySchemes:
    bearer: { type: http, scheme: bearer }
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	accept := map[string]string{} // Authorization header -> Accept
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		accept[r.Header.Get("Authorization")] = r.Header.Get("Accept")
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`["a"]`))
	})
	cfg := Config{
		SpecPath:       specPath,
		Handler:        handler,
		Token:          StaticToken("good"),
		SkipAuthCases:  []string{AuthInvalidToken},
		DisableReports: true,
		Hooks: Hooks{BeforeRequest: func(_ context.Context, c *Case, req *http.Request) error {
			if c.Example == AuthUnauthorized || c.Example == AuthInvalidToken {
				req.Header.Set("Accept", "text/plain")
			}
			return nil
		}},
	}
	out := runFake(t, cfg)
	st := statuses(out.res)
	// Skipped auth cases do not exist at all: no result, no subtest.
	if _, ok := st["Item/listItems/invalid-token"]; ok {
		t.Errorf("invalid-token was planned: %v", st)
	}
	for _, sub := range out.ft.subs {
		if strings.HasSuffix(sub.name, "/invalid-token") {
			t.Errorf("subtest %s exists", sub.name)
		}
	}
	if st["Item/listItems/unauthorized"] != StatusPassed || st["Item/listItems/default"] != StatusPassed {
		t.Errorf("statuses: %v", st)
	}
	mu.Lock()
	if accept[""] != "text/plain" || accept["Bearer good"] != "application/json" || len(accept) != 2 {
		t.Errorf("Accept per request: %v", accept)
	}
	mu.Unlock()

	cfg.SkipAuthCases = []string{"invalid_token"}
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), `unknown case "invalid_token"`) {
		t.Errorf("validate: %v", err)
	}
}

// A failed case shows the response headers, also without body, and the curl
// command contains the headers as sent after BeforeRequest; secret-looking
// headers are masked.
func TestReportShowsHeadersAsSentAndReceived(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "health.yaml")
	err := os.WriteFile(specPath, []byte(`
openapi: 3.0.3
info: { title: Health, version: "1" }
security: [{ bearer: [] }]
paths:
  /health:
    get:
      operationId: getHealth
      tags: [System]
      responses:
        "200": { description: ok, content: { text/plain: { schema: { type: string } } } }
        "401": { description: no token, content: { text/plain: { schema: { type: string } } } }
components:
  securitySchemes:
    bearer: { type: http, scheme: bearer }
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			// like a gateway: 401 without body and without Content-Type
			w.Header().Set("WWW-Authenticate", `Bearer realm="gateway"`)
			w.Header().Set("Set-Cookie", "gw=secret-cookie")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("alive!"))
	})
	cfg := Config{
		SpecPath:   specPath,
		Handler:    handler,
		Token:      StaticToken("good"),
		ReportPath: filepath.Join(t.TempDir(), "report.md"),
		Hooks: Hooks{BeforeRequest: func(_ context.Context, _ *Case, req *http.Request) error {
			req.Header.Set("X-Trace", "t1")
			req.Header.Set("X-Api-Key", "hook-secret")
			return nil
		}},
	}
	out := runFake(t, cfg)
	if st := statuses(out.res)["System/getHealth/unauthorized"]; st != StatusSchemaViolation {
		t.Fatalf("unauthorized: %s", st)
	}
	for _, want := range []string{
		"Response headers (401, no body)",
		`Www-Authenticate: Bearer realm="gateway"`,
		"Set-Cookie: ***",
		`-H "X-Trace: t1"`,
		`-H "X-Api-Key: ***"`,
	} {
		if !strings.Contains(out.report, want) {
			t.Errorf("report misses %q", want)
		}
	}
	for _, secret := range []string{"hook-secret", "secret-cookie"} {
		if strings.Contains(out.report, secret) {
			t.Errorf("report leaks %q", secret)
		}
	}
}

// Config.CaseInsensitive accepts an answer that differs from the example in
// the case of its strings and field names only; without it the case fails.
func TestCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "moons.yaml")
	err := os.WriteFile(specPath, []byte(`
openapi: 3.0.3
info: { title: Moons, version: "1" }
paths:
  /moons/current:
    get:
      operationId: getMoon
      tags: [Moon]
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  name: { type: string }
                  planet: { type: string }
              example: { name: Luna, planet: Earth }
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"LUNA","Planet":"earth"}`))
	})
	cfg := Config{SpecPath: specPath, Handler: handler, DisableReports: true}
	if out := runFake(t, cfg); !out.res.Failed || out.res.Cases[0].Status != StatusExampleMismatch {
		t.Fatalf("case-sensitive run passed:\n%s", out.ft.output())
	}
	cfg.CaseInsensitive = true
	if out := runFake(t, cfg); out.res.Failed {
		t.Fatalf("case-insensitive run failed:\n%s", out.ft.output())
	}
}
