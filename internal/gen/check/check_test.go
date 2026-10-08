package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

func load(t *testing.T, src string) *spec.Spec {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := spec.Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const crew = `
openapi: 3.0.3
info: { title: Crew, version: "1" }
paths:
  /crews:
    post:
      operationId: createCrew
      requestBody:
        required: true
        content:
          application/json:
            schema: { type: object, required: [name], properties: { name: { type: string } } }
      responses:
        "201":
          description: created
          content:
            application/json:
              schema: { type: object, properties: { id: { type: integer } } }
              example: { id: "not a number" }
  /crews/{id}:
    get:
      operationId: getCrew
      parameters: [{ name: id, in: path, required: true, schema: { type: integer } }]
      responses: { "200": { description: ok } }
  /reports/{year}:
    get:
      operationId: getReport
      parameters: [{ name: year, in: path, required: true, schema: { type: integer } }]
      responses: { "200": { description: ok } }
  /legacy:
    get:
      operationId: legacy
      x-apitest-skip: retired
      parameters: [{ name: q, in: query, required: true, schema: { type: string } }]
      responses: { "200": { description: ok } }
`

func TestRun(t *testing.T) {
	s := load(t, crew)
	r := Run(s, nil)
	if r.Cases != 4 {
		t.Errorf("cases: %d", r.Cases)
	}
	kinds := map[string][]string{}
	for _, p := range r.Problems {
		kinds[p.Kind] = append(kinds[p.Kind], p.Where+": "+p.Message)
	}
	// no body example for a required body, no value for {year}
	if nb := strings.Join(kinds[KindNotBuildable], "\n"); !strings.Contains(nb, "createCrew") || !strings.Contains(nb, `"year"`) {
		t.Errorf("not buildable: %v", kinds[KindNotBuildable])
	}
	// {id} is bound to the POST by the heuristic, so it counts as present
	if strings.Contains(strings.Join(kinds[KindNotBuildable], "\n"), "getCrew") {
		t.Errorf("a bound parameter was reported: %v", kinds[KindNotBuildable])
	}
	if len(kinds[KindExampleSchema]) != 1 {
		t.Errorf("example findings: %v", kinds[KindExampleSchema])
	}
	if r.Ready != 1 {
		t.Errorf("ready: %d", r.Ready)
	}

	// a fixed value, as from Config.Params or defaults.json, fills {year}
	r = Run(s, map[string]string{"getReport.year": "2026"})
	for _, p := range r.Problems {
		if strings.Contains(p.Where, "getReport") {
			t.Errorf("fixed value ignored: %v", p)
		}
	}
}

func TestRunReportsBrokenBindings(t *testing.T) {
	s := load(t, `
openapi: 3.0.3
info: { title: B, version: "1" }
paths:
  /a/{id}:
    get:
      operationId: getA
      parameters:
        - name: id
          in: path
          required: true
          schema: { type: string }
          x-apitest-bind: { from: nowhere, pointer: /id }
      responses: { "200": { description: ok } }
`)
	r := Run(s, nil)
	if len(r.Problems) != 1 || r.Problems[0].Kind != KindBinding || !strings.Contains(r.Problems[0].Message, "nowhere") {
		t.Errorf("problems: %+v", r.Problems)
	}
}

// Lint adds what apitest.Run warns about besides Run: the guessed binding
// of {id}, every finding with its fix.
func TestLint(t *testing.T) {
	r := Lint(load(t, crew), nil)
	kinds := map[string]int{}
	for _, p := range r.Problems {
		kinds[p.Kind]++
		if p.Fix == "" {
			t.Errorf("no fix: %+v", p)
		}
	}
	if kinds[KindHeuristic] != 1 || kinds[KindNotBuildable] != 2 || kinds[KindExampleSchema] != 1 {
		t.Errorf("kinds %v:\n%+v", kinds, r.Problems)
	}
	for _, p := range r.Problems {
		if p.Kind == KindHeuristic && (p.Where != "paths./crews/{id}.get.parameters[id]" || !strings.Contains(p.Message, `parameter "id" is resolved heuristically from createCrew`) || !strings.Contains(p.Fix, "x-apitest-bind")) {
			t.Errorf("heuristic: %+v", p)
		}
	}
	explicit := strings.Replace(crew, "schema: { type: integer } }]\n      responses: { \"200\": { description: ok } }\n  /reports",
		"schema: { type: integer }, x-apitest-bind: { from: createCrew, pointer: /id } }]\n      responses: { \"200\": { description: ok } }\n  /reports", 1)
	for _, p := range Lint(load(t, explicit), nil).Problems {
		if p.Kind == KindHeuristic {
			t.Errorf("explicit binding still guessed: %+v", p)
		}
	}
}
