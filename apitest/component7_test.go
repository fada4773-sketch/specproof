package apitest

// Component tests for the error cases: generated not-found and conflict
// cases, named 5xx examples and Config.TolerateErrorCases.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const errorSpec = `
openapi: 3.0.3
info: { title: Starport, version: "1" }
paths:
  /docks:
    post:
      operationId: createDock
      tags: [Dock]
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/DockWrite" }
            example: { name: Nord }
      responses:
        "201": { $ref: "#/components/responses/Dock" }
        "409": { $ref: "#/components/responses/Problem" }
  /docks/{dockId}:
    parameters:
      - { name: dockId, in: path, required: true, schema: { type: integer, minimum: 1 } }
    get:
      operationId: getDock
      tags: [Dock]
      responses:
        "200": { $ref: "#/components/responses/Dock" }
        "404": { $ref: "#/components/responses/Problem" }
    delete:
      operationId: deleteDock
      tags: [Dock]
      responses:
        "204": { description: deleted }
        "404": { $ref: "#/components/responses/Problem" }
  /docks/{dockId}/undock:
    parameters:
      - { name: dockId, in: path, required: true, schema: { type: integer } }
    post:
      operationId: undockDock
      tags: [Dock]
      requestBody:
        required: true
        content:
          application/json:
            schema: { type: object, properties: { force: { type: integer } } }
            examples:
              overload: { value: { force: 999 } }
      responses:
        "202": { description: undocked }
        "500":
          description: the clamps fail
          content:
            application/json:
              schema: { $ref: "#/components/schemas/Problem" }
              examples:
                overload: { value: { message: clamps failed } }
components:
  responses:
    Dock:
      description: the dock
      content:
        application/json:
          schema: { $ref: "#/components/schemas/Dock" }
    Problem:
      description: a problem
      content:
        application/json:
          schema: { $ref: "#/components/schemas/Problem" }
  schemas:
    DockWrite:
      type: object
      required: [name]
      properties: { name: { type: string } }
    Dock:
      allOf:
        - $ref: "#/components/schemas/DockWrite"
        - type: object
          required: [id]
          properties: { id: { type: integer, readOnly: true } }
    Problem:
      type: object
      required: [message]
      properties: { message: { type: string } }
`

// starportErrors answers 409 for a dock name twice and 404 for an unknown
// dock, but deletes an unknown dock with 204 and answers 400 instead of 500
// to an overload.
func starportErrors() http.Handler {
	var mu sync.Mutex
	docks := map[string]string{}
	next := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		answer := func(status int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodPost && len(segs) == 1:
			var body struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, n := range docks {
				if n == body.Name {
					answer(409, map[string]any{"message": "name taken"})
					return
				}
			}
			next++
			docks[fmt.Sprint(next)] = body.Name
			answer(201, map[string]any{"id": next, "name": body.Name})
		case r.Method == http.MethodGet && len(segs) == 2:
			if n, ok := docks[segs[1]]; ok {
				answer(200, map[string]any{"id": json.Number(segs[1]), "name": n})
				return
			}
			answer(404, map[string]any{"message": "no such dock"})
		case r.Method == http.MethodDelete && len(segs) == 2:
			delete(docks, segs[1])
			w.WriteHeader(204)
		case r.Method == http.MethodPost && len(segs) == 3:
			answer(400, map[string]any{"message": "force too high"})
		default:
			answer(404, map[string]any{"message": "no such path"})
		}
	})
}

func TestErrorCases(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "starport.yaml")
	if err := os.WriteFile(specPath, []byte(errorSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{SpecPath: specPath, Handler: starportErrors(), ReportPath: filepath.Join(t.TempDir(), "report.md")}

	// without ErrorCases only the named 5xx example is an error case
	out := runFake(t, cfg)
	if got := statuses(out.res); got["Dock/undockDock/overload"] != StatusFailed || len(got) != 4 {
		t.Fatalf("without ErrorCases: %v", got)
	}

	cfg.ErrorCases = true
	out = runFake(t, cfg)
	want := map[string]Status{
		"Dock/createDock/default":   StatusPassed,
		"Dock/createDock/conflict":  StatusPassed,
		"Dock/getDock/default":      StatusPassed,
		"Dock/getDock/not-found":    StatusPassed,
		"Dock/deleteDock/default":   StatusPassed,
		"Dock/deleteDock/not-found": StatusFailed,
		"Dock/undockDock/overload":  StatusFailed,
	}
	if got := statuses(out.res); fmt.Sprint(got) != fmt.Sprint(want) || !out.res.Failed {
		t.Fatalf("ErrorCases:\n got  %v\n want %v\n%s", got, want, out.ft.output())
	}
	names := order(out.res)
	if indexOf(names, "Dock/createDock/conflict") < indexOf(names, "Dock/createDock/default") ||
		indexOf(names, "Dock/deleteDock/not-found") > indexOf(names, "Dock/deleteDock/default") {
		t.Errorf("order:\n%s", strings.Join(names, "\n"))
	}

	cfg.TolerateErrorCases = true
	out = runFake(t, cfg)
	if out.res.Failed {
		t.Fatalf("tolerated run failed:\n%s", out.ft.output())
	}
	for _, c := range out.res.Cases {
		switch c.Name {
		case "Dock/deleteDock/not-found", "Dock/undockDock/overload":
			if c.Status != StatusTolerated || c.Tolerated != StatusFailed || !strings.HasPrefix(c.Message, "tolerated (FAILED): status code") {
				t.Errorf("%s: %+v", c.Name, c)
			}
		}
	}
	for _, want := range []string{"Error cases", "Dock/deleteDock/not-found", "404", "204"} {
		if !strings.Contains(out.report, want) {
			t.Errorf("report misses %q", want)
		}
	}
	page, err := os.ReadFile(strings.TrimSuffix(out.res.Report, ".md") + ".html")
	if err != nil || !strings.Contains(string(page), `id="errorcases"`) || !strings.Contains(string(page), "Dock/undockDock/overload") {
		t.Errorf("HTML report: %v", err)
	}

	cfg.DisableHTMLReport = true
	cfg.ReportPath = filepath.Join(t.TempDir(), "report.md")
	out = runFake(t, cfg)
	if _, err := os.Stat(strings.TrimSuffix(out.res.Report, ".md") + ".html"); err == nil {
		t.Error("DisableHTMLReport wrote an HTML report")
	}
}

// x-apitest-not-found sets the unknown key.
func TestNotFoundValue(t *testing.T) {
	spec := strings.Replace(errorSpec, "schema: { type: integer, minimum: 1 } }", "schema: { type: integer, minimum: 1 }, x-apitest-not-found: 4242 }", 1)
	specPath := filepath.Join(t.TempDir(), "starport.yaml")
	if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	h := starportErrors()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path)
		mu.Unlock()
		h.ServeHTTP(w, r)
	})
	out := runFake(t, Config{SpecPath: specPath, Handler: handler, ErrorCases: true, TolerateErrorCases: true, DisableReports: true})
	if out.res.Failed {
		t.Fatalf("run failed:\n%s", out.ft.output())
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(got, "\n"), "GET /docks/4242") {
		t.Errorf("requests:\n%s", strings.Join(got, "\n"))
	}
}
