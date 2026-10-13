package contract

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/gen/discover"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
	"github.com/fada4773-sketch/specproof/internal/testserver"
)

const (
	bookstoreSpec = "../../../testdata/bookstore.yaml"
	token         = "contract-token"
	reader        = "contract-reader"
)

func bookstore() *testserver.Server {
	srv := testserver.New(token, token, testserver.Faults{})
	srv.Readers = []string{reader}
	return srv
}

func generate(t *testing.T, in Input) *Output {
	t.Helper()
	s, err := spec.Load(context.Background(), bookstoreSpec)
	if err != nil {
		t.Fatal(err)
	}
	in.Spec = s
	out, err := Generate(in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// run writes the scenario and runs it against a fresh Bookstore.
func run(t *testing.T, sc *scenario.Scenario, update bool, dir string) (*apitest.Result, string) {
	t.Helper()
	path := filepath.Join(dir, "contract-scenario_gen.yaml")
	b, err := sc.Encode(" test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	res, err := apitest.RunScenarioStandalone(context.Background(), apitest.ScenarioConfig{
		Config: apitest.Config{
			SpecPath: bookstoreSpec, Handler: bookstore(),
			Token: apitest.StaticToken(token), ForbiddenToken: apitest.StaticToken(reader),
		},
		Scenario: path, Golden: filepath.Join(dir, "contract-golden_gen.yaml"), Update: update,
	}, apitest.StandaloneOptions{Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	return res, out.String()
}

func TestGenerateRunsGreen(t *testing.T) {
	out := generate(t, Input{})
	dir := t.TempDir()
	res, log := run(t, out.Scenario, true, dir)
	if res.Failed {
		t.Fatalf("update run failed:\n%s", log)
	}
	res, log = run(t, out.Scenario, false, dir)
	if res.Failed {
		t.Fatalf("compare run failed:\n%s", log)
	}
	if len(res.Cases) < 40 {
		t.Errorf("only %d steps ran", len(res.Cases))
	}
}

func writeFile(t *testing.T, name, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOverrides(t *testing.T) {
	o, err := LoadOverrides(writeFile(t, "o.yaml", `
values:
  Author.name: [Ursula, Stanislaw]
  Shelf.code: s1
links:
  Review.text: none
steps:
  author1.create: {expect: 201, token: null}
  shelf1.read: {check: null}
add:
  - after: author1.create
    id: author1.extra
    call: getAuthor
    path: {authorId: '{{author1.authorId}}'}
    expect: 200
skip: [author.list, author1.delete.unauthorized]
`))
	if err != nil {
		t.Fatal(err)
	}
	out := generate(t, Input{Overrides: o, Records: 2})
	sc := out.Scenario
	if sc.Step("author.list") != nil || sc.Step("author1.extra") == nil || sc.Step("shelf1.read").Equals != "" {
		t.Errorf("overrides not applied")
	}
	if name := sc.Step("author1.create").Body.(map[string]any)["name"]; name != "Ursula" {
		t.Errorf("value override: %v", name)
	}
	if sc.Step("author1.create").Why != "override" {
		t.Errorf("why: %q", sc.Step("author1.create").Why)
	}
	idx := func(id string) int {
		for i, st := range sc.Steps {
			if st.ID == id {
				return i
			}
		}
		return -1
	}
	if idx("author1.extra") != idx("author1.create")+1 {
		t.Error("add must follow its step")
	}

	for text, want := range map[string]string{
		"- 1":                 "are a mapping",
		"nope: 1":             "unknown key",
		"values: 1":           "values must map",
		"links: 1":            "links must map",
		"steps: 1":            "steps must map",
		"steps: {a: 1}":       "must be a mapping",
		"steps: {a: {id: b}}": "cannot be changed",
		"add: 1":              "add must be a list",
		"add: [1]":            "must be a step",
		"add: [{id: x}]":      "needs \"after\"",
		"skip: 1":             "skip must be a list",
	} {
		if _, err := LoadOverrides(writeFile(t, "o.yaml", text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
	if o, err := LoadOverrides(filepath.Join(t.TempDir(), "missing.yaml")); err != nil || o == nil {
		t.Errorf("a missing file is no overrides: %v", err)
	}
	if o, err := LoadOverrides(writeFile(t, "o.yaml", "# nothing\n")); err != nil || o == nil {
		t.Errorf("an empty file: %v", err)
	}
	if o, err := LoadOverrides(""); err != nil || o == nil {
		t.Error("no path")
	}
	s, _ := spec.Load(context.Background(), bookstoreSpec)
	for text, want := range map[string]string{
		"steps: {nope: {expect: 200}}":                       "no generated step has this id",
		"steps: {author1.create: {expect: x}}":               "expect",
		"add: [{after: nope, id: z, call: x, expect: 200}]":  "names no step",
		"add: [{after: author1.create, id: z, expect: 200}]": "call",
		"skip: [nope]":           "no step has the id",
		"skip: [author1.create]": "does not hold together",
	} {
		o, err := LoadOverrides(writeFile(t, "o.yaml", text))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Generate(Input{Spec: s, Overrides: o}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
}

func TestLearnAndLinks(t *testing.T) {
	srv := httptest.NewServer(bookstore())
	defer srv.Close()
	s, _ := spec.Load(context.Background(), bookstoreSpec)
	learned, notes := Learn(context.Background(), s, discover.Options{BaseURL: srv.URL + "/v1", Token: token})
	if len(learned["Author"]) != 1 || learned["Author"][0]["name"] != "Grace Hopper" {
		t.Errorf("learned: %v %v", learned, notes)
	}
	_, notes = Learn(context.Background(), s, discover.Options{BaseURL: srv.URL + "/v1", Token: "wrong"})
	if len(notes) == 0 {
		t.Error("a failing list must give a note")
	}
	out := generate(t, Input{Learned: learned, LearnedFrom: "dev"})
	st := out.Scenario.Step("author1.create")
	if st.Body.(map[string]any)["name"] != "Grace Hopper" || st.Notes["/body/name"] != "guess: learned from dev" {
		t.Errorf("learned value: %+v", st)
	}
	found := false
	for _, g := range out.Guesses {
		if strings.Contains(g.Why, "learned from dev") {
			found = true
		}
	}
	if !found {
		t.Errorf("guesses: %+v", out.Guesses)
	}
	// a link override makes a field a reference
	o := &Overrides{Links: map[string]string{"Shelf.name": "Author"}}
	out = generate(t, Input{Overrides: o})
	if v := out.Scenario.Step("shelf1.create").Body.(map[string]any)["name"]; v != "{{author1.authorId}}" {
		t.Errorf("link: %v", v)
	}
	if matches("originAuthorId", "authorId") != true || matches("coauthorId", "authorId") || !matches("origin_authorId", "authorId") {
		t.Error("matches")
	}
}

func TestChangeAndInvalid(t *testing.T) {
	s, _ := spec.Load(context.Background(), bookstoreSpec)
	gen := generate(t, Input{})
	_ = gen
	shelf := requestSchema(s.Op("updateShelf"))
	m := modelResource(t, s, "Shelf")
	body, field := change(map[string]any{"code": "c", "name": "Logistics"}, shelf, m, false)
	if field != "name" || body.(map[string]any)["name"] != "Logistics (revised)" {
		t.Errorf("change: %v %v", field, body)
	}
	body, field = change(map[string]any{"code": "c", "name": "Logistics"}, shelf, m, true)
	if field != "name" || len(body.(map[string]any)) != 1 {
		t.Errorf("patch: %v %v", field, body)
	}
	if _, field := change("text", shelf, m, false); field != "" {
		t.Error("a non-object stays")
	}
	b, f := withoutRequired(map[string]any{"code": "c", "name": "x"}, shelf)
	if f == "" || len(b.(map[string]any)) != 1 {
		t.Errorf("withoutRequired: %v %v", f, b)
	}
	if _, f := withoutRequired("x", shelf); f != "" {
		t.Error("a non-object has no field to drop")
	}
}

func TestReviewAndExamples(t *testing.T) {
	out := generate(t, Input{})
	dir := t.TempDir()
	specCopy := filepath.Join(dir, "openapi.yaml")
	b, _ := os.ReadFile(bookstoreSpec)
	if err := os.WriteFile(specCopy, b, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "contract-scenario_gen.yaml")
	text, _ := out.Scenario.Encode("")
	if err := os.WriteFile(path, text, 0o644); err != nil {
		t.Fatal(err)
	}
	var run []apitest.StepExchange
	res, err := apitest.RunScenarioStandalone(context.Background(), apitest.ScenarioConfig{
		Config:   apitest.Config{SpecPath: specCopy, Handler: bookstore(), Token: apitest.StaticToken(token), ForbiddenToken: apitest.StaticToken(reader)},
		Scenario: path, Golden: filepath.Join(dir, "golden.yaml"), Update: true,
		OnStep: func(ex apitest.StepExchange) { run = append(run, ex) },
	}, apitest.StandaloneOptions{})
	if err != nil || res.Failed {
		t.Fatalf("run: %v %+v", err, res)
	}
	s, _ := spec.Load(context.Background(), specCopy)
	doc, err := yamldoc.Load(specCopy)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := Examples(s, doc, out.Scenario, run)
	if err != nil || len(rr.Problems) > 0 || rr.Examples == 0 {
		t.Fatalf("examples: %v %+v", err, rr.Problems)
	}
	written, _ := doc.Bytes()
	for _, want := range []string{"default:", "unknown:"} {
		if !strings.Contains(string(written), want) {
			t.Errorf("spec misses %q", want)
		}
	}
	golden, _ := scenario.LoadSnapshot(filepath.Join(dir, "golden.yaml"))
	md := Review(ReviewInput{Spec: "openapi.yaml", SpecVersion: "3.0", BaseURL: "http://x", Out: out, Result: res, Golden: golden,
		Learn: []string{"Author: failed"}, Examples: "12 examples written"})
	for _, want := range []string{"# Review: contract scenario", "every step answered as expected", "## Guesses", "## Not learned", "Spec: 12 examples"} {
		if !strings.Contains(md, want) {
			t.Errorf("review misses %q:\n%s", want, md)
		}
	}
	res.Failed = true
	res.Cases = append(res.Cases, apitest.CaseResult{Name: "Author/author1.delete", Status: apitest.StatusFailed, StatusCode: 409, Message: "status code 409, expected 204"})
	out.NotGenerated = append(out.NotGenerated, Note{What: "GET /x", Why: "500"})
	out.Uncovered = append(out.Uncovered, Note{What: "POST /y", Why: "no 2xx"})
	out.Guesses = nil
	md = Review(ReviewInput{Spec: "openapi.yaml", Out: out, Result: res})
	for _, want := range []string{"did not answer as expected (1)", "steps: {author1.delete: {expect: 409}}", "## Not generated (1)", "## Operations without a step (1)", "None: every decision"} {
		if !strings.Contains(md, want) {
			t.Errorf("review misses %q:\n%s", want, md)
		}
	}
	if md := Review(ReviewInput{Out: out}); !strings.Contains(md, "not run") {
		t.Error("review without run")
	}
	if variant("a.b.c") != "c" || variant("a") != "a" {
		t.Error("variant")
	}
}

// TestProposals: every operation without a step gets an entry under
// "operations" of the overrides, by its reason, with values learned from
// the stage where possible; filled in, the next run gives it a step.
func TestProposals(t *testing.T) {
	s, err := spec.Load(context.Background(), "../../../testdata/gen/contract-uncovered.yaml")
	if err != nil {
		t.Fatal(err)
	}
	learned := map[string][]map[string]any{"Dock": {{"code": "D-7", "name": "North"}}}
	out, err := Generate(Input{Spec: s, Learned: learned, LearnedFrom: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	byCall := map[string]*Proposal{}
	for _, p := range out.Proposals {
		byCall[p.Call] = p
	}
	if len(out.Proposals) != 4 || len(out.Uncovered) != 4 {
		t.Fatalf("proposals: %+v", out.Proposals)
	}
	if p := byCall["GET /docks/{dockCode}"]; p == nil || p.Path["dockCode"] != "D-7" || p.Status || !strings.Contains(p.Learned["path/dockCode"], "learned from dev") {
		t.Errorf("learned path value: %+v", p)
	}
	if p := byCall["DELETE /planets/{planetId}"]; p == nil || !p.Status || p.Path["planetId"] != nil || len(p.Why) != 2 {
		t.Errorf("delete: %+v", p)
	}
	if p := byCall["POST /ships/import"]; p == nil || !p.Status || p.Documented != 201 {
		t.Errorf("second create: %+v", p)
	}
	if p := byCall["POST /ships/{shipId}/launch"]; p == nil || !p.Status || p.Documented != 0 || !strings.Contains(p.Why[0], "no 2xx") {
		t.Errorf("no 2xx: %+v", p)
	}

	// written into the user's file: what is there stays
	path := writeFile(t, "contract-overrides.yaml", "# mine\nvalues:\n  Ship.name: Moon   # kept\n")
	n, err := WriteProposals(path, out.Proposals)
	if err != nil || n != 4 {
		t.Fatalf("write: %d %v", n, err)
	}
	b, _ := os.ReadFile(path)
	text := string(b)
	for _, want := range []string{"# mine", "# kept", "operations:", "Fill in the null values", "GET /docks/{dockCode}:", "dockCode: D-7 # learned from dev (Dock.code)",
		"planetId: null", "status: null # documented: 201", "status: null # the status it answers with", "# no 2xx response documented"} {
		if !strings.Contains(text, want) {
			t.Errorf("overrides miss %q:\n%s", want, text)
		}
	}
	if n, err := WriteProposals(path, out.Proposals); err != nil || n != 0 {
		t.Errorf("a second write must add nothing: %d %v", n, err)
	}

	// null is open: loaded as it is, nothing changes
	o, err := LoadOverrides(path)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := Generate(Input{Spec: s, Overrides: o})
	if err != nil {
		t.Fatal(err)
	}
	if len(out2.Proposals) != 3 || out2.Scenario.Step("getDock") == nil {
		t.Errorf("only the learned dock is complete: %+v", out2.Proposals)
	}

	// filled in: every operation gets a step, a skipped one none
	for old, repl := range map[string]string{
		"planetId: null":                            "planetId: h1",
		"status: null # documented: 204":            "status: 204",
		"status: null # documented: 201":            "status: 201",
		"status: null # the status it answers with": "status: 202",
	} {
		text = strings.Replace(text, old, repl, 1)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if o, err = LoadOverrides(path); err != nil {
		t.Fatal(err)
	}
	out3, err := Generate(Input{Spec: s, Overrides: o})
	if err != nil {
		t.Fatal(err)
	}
	if len(out3.Proposals) != 0 {
		t.Errorf("left: %+v", out3.Proposals)
	}
	sc := out3.Scenario
	for call, expect := range map[string]int{"DELETE /planets/{planetId}": 204, "POST /ships/import": 201, "POST /ships/{shipId}/launch": 202, "GET /docks/{dockCode}": 200} {
		found := false
		for _, st := range sc.Steps {
			if st.Call == call && st.Expect == expect {
				found = true
			}
		}
		if !found {
			t.Errorf("no step %s %d", call, expect)
		}
	}
	del := slices.IndexFunc(sc.Steps, func(st *scenario.Step) bool { return st.Call == "DELETE /planets/{planetId}" })
	if first := slices.IndexFunc(sc.Steps, func(st *scenario.Step) bool { return st.Call == "DELETE /ships/{shipId}" }); del > first {
		t.Error("an extra DELETE runs before the records are deleted")
	}
	o.Operations["DELETE /planets/{planetId}"] = &OpOverride{Skip: true}
	o.Operations["POST /ships/import"] = &OpOverride{}
	out4, _ := Generate(Input{Spec: s, Overrides: o})
	if len(out4.Proposals) != 1 || out4.Proposals[0].Call != "POST /ships/import" {
		t.Errorf("skip proposes nothing: %+v", out4.Proposals)
	}

	// a missing file is created, a file of comments keeps them
	fresh := filepath.Join(t.TempDir(), "new.yaml")
	if n, err := WriteProposals(fresh, out.Proposals); err != nil || n != 4 {
		t.Errorf("new file: %d %v", n, err)
	}
	comments := writeFile(t, "c.yaml", "# only a comment\n")
	if _, err := WriteProposals(comments, out.Proposals); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(comments); !strings.HasPrefix(string(b), "# only a comment\n") || !strings.Contains(string(b), "operations:") {
		t.Errorf("comment file:\n%s", b)
	}
	if _, err := WriteProposals(writeFile(t, "l.yaml", "- 1\n"), out.Proposals); err == nil {
		t.Error("a list is no overrides file")
	}

	for text, want := range map[string]string{
		"operations: 1":                       "operations must map",
		"operations: {nope: {}}":              "is no operation",
		"operations: {GET /x: 1}":             "must be a mapping",
		"operations: {GET /x: {foo: 1}}":      "unknown key",
		"operations: {GET /x: {status: 42}}":  "HTTP status",
		"operations: {GET /x: {path: 1}}":     "must map parameter names",
		"operations: {GET /x: {skip: maybe}}": "true or false",
	} {
		if _, err := LoadOverrides(writeFile(t, "o.yaml", text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
	if o, err := LoadOverrides(writeFile(t, "o.yaml", "operations: {get  /x: ~, GET /y: {query: {q: 1}, header: {X-A: 2}, skip: false}}")); err != nil || o.Operations["GET /x"] == nil || o.Operations["GET /y"].Query["q"] == nil {
		t.Errorf("normalized keys: %v %+v", err, o)
	}
}

func modelResource(t *testing.T, s *spec.Spec, name string) *model.Resource {
	t.Helper()
	r := model.Detect(s, nil).Resource(name)
	if r == nil {
		t.Fatalf("no resource %s", name)
	}
	return r
}
