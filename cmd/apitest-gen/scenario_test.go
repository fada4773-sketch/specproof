package main

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/testserver"
)

const (
	bookstoreSpec = "../../testdata/bookstore.yaml"
	storeToken    = "cli-token"
	storeReader   = "cli-reader"
)

// startBookstore serves a fresh Bookstore over HTTP and returns its base URL.
func startBookstore(t *testing.T) (*testserver.Server, string) {
	t.Helper()
	srv := testserver.New(storeToken, storeToken, testserver.Faults{})
	srv.Readers = []string{storeReader}
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	return srv, hs.URL + "/v1"
}

const (
	starportSpec  = "../../testdata/gen/tree-starport.yaml"
	starportToken = "cli-tree-token"
)

// starport serves a Starport over HTTP: a stage with records (first id
// 100), or an empty instance (first id 1).
func starport(t *testing.T, stage bool) (*testserver.Starport, string) {
	t.Helper()
	first := 1
	if stage {
		first = 100
	}
	srv := testserver.NewStarport(starportToken, first)
	if stage {
		srv.Fill()
	}
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	return srv, hs.URL + "/api"
}

// treeProject copies the Starport spec into a project folder.
func treeProject(t *testing.T) (specPath, file string) {
	t.Helper()
	dir := t.TempDir()
	b, err := os.ReadFile(starportSpec)
	if err != nil {
		t.Fatal(err)
	}
	specPath = filepath.Join(dir, "api", "openapi.yaml")
	if err := os.MkdirAll(filepath.Dir(specPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return specPath, filepath.Join(dir, "apitest", "tree", "tree.yaml")
}

func TestTreeProposeFetchRun(t *testing.T) {
	specPath, file := treeProject(t)
	dir := filepath.Dir(file)
	// 1. propose
	code, out, errOut := cli("tree", "-spec", specPath, "-file", file)
	if code != 0 || !strings.Contains(out, "proposed from the spec") || !strings.Contains(out, "no records yet") {
		t.Fatalf("propose: %d\n%s%s", code, out, errOut)
	}
	reviewPath := filepath.Join(dir, "tree-review_gen.md")
	review := func(parts ...string) {
		t.Helper()
		b, _ := os.ReadFile(reviewPath)
		for _, p := range parts {
			if !strings.Contains(string(b), p) {
				t.Errorf("tree-review_gen.md lacks %q:\n%s", p, b)
			}
		}
	}
	review("| 1. Tree (tree.yaml) | done | proposed from the spec", "| 2. Fetch (tree-data_gen.yaml) | open |", "### Nodes", "-base-url <stage>")
	text, _ := os.ReadFile(file)
	if !strings.Contains(string(text), "docks:") || !strings.Contains(string(text), "# listDocks") {
		t.Fatalf("tree.yaml:\n%s", text)
	}
	// the user picks two planets
	changed := strings.Replace(string(text), "call: GET /planets # listPlanets\n    pick: 3", "call: GET /planets # listPlanets\n    pick: [mars, venus]", 1)
	if changed == string(text) {
		t.Fatalf("pick of planets not found:\n%s", text)
	}
	if err := os.WriteFile(file, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	// 2. a dry run sends nothing, also with -base-url, and shows the plan
	srv, stageURL := starport(t, true)
	t.Setenv("CLI_TREE_TOKEN", starportToken)
	code, out, errOut = cli("tree", "-spec", specPath, "-file", file, "-base-url", stageURL, "-token-env", "CLI_TREE_TOKEN", "-dry-run")
	if code != 0 || !strings.Contains(out, "order  ") || !strings.Contains(out, "the fetch would send") || len(srv.Requests) != 0 {
		t.Fatalf("dry run: %d %v\n%s%s", code, srv.Requests, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "tree-data_gen.yaml")); err == nil {
		t.Error("a dry run writes no data")
	}
	// then fetch from the stage
	code, out, errOut = cli("tree", "-spec", specPath, "-file", file, "-base-url", stageURL, "-token-env", "CLI_TREE_TOKEN")
	if code != 0 || !strings.Contains(out, "up to date") || !strings.Contains(out, "records created") {
		t.Fatalf("fetch: %d\n%s%s", code, out, errOut)
	}
	// the one POST is the search (read: true)
	if n := srv.Requests["PUT"] + srv.Requests["PATCH"] + srv.Requests["DELETE"]; n != 0 || srv.Requests["POST"] > 1 {
		t.Errorf("the fetch wrote to the stage: %v", srv.Requests)
	}
	for _, f := range []string{"tree-data_gen.yaml", "tree-test_gen.yaml", "tree-review_gen.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tree.yaml")); !strings.Contains(string(b), "pick: [mars, venus]") {
		t.Error("the change of the user must stay")
	}
	review("### Seed", "mars", "| 1. Tree (tree.yaml) | done | up to date with the spec", "| 2. Fetch (tree-data_gen.yaml) | done |",
		"| 3. Test (tree-test_gen.yaml) | done |", "### Records per node", "tree run -spec",
		"## Plan", "order: ", "<summary>Requests of the fetch (", "#### 1. GET /")
	// 3. build again without fetching
	code, out, _ = cli("tree", "-spec", specPath, "-file", file)
	if code != 0 || !strings.Contains(out, "fetched before") {
		t.Errorf("rebuild: %d\n%s", code, out)
	}
	// 4. run against an empty instance: write the answers into the test and the examples
	_, empty := starport(t, false)
	junit := filepath.Join(dir, "out", "junit.xml")
	code, out, errOut = cli("tree", "run", "-spec", specPath, "-file", file, "-base-url", empty, "-token", starportToken, "-update", "-junit", junit)
	if code != 0 || !strings.Contains(out, "PASSED") || !strings.Contains(out, "examples") {
		t.Fatalf("run -update: %d\n%s%s", code, out, errOut)
	}
	if sc, err := scenario.Load(filepath.Join(dir, "tree-test_gen.yaml")); err != nil || sc.Golden == nil || len(sc.Golden.Steps) == 0 {
		t.Errorf("answers not written into the test: %v", err)
	}
	review("| 4. Run (tree run) | done | PASSED", "| 5. Update (tree run -update) | done | answers written", "examples", "### Seed", "**Next:** done",
		"<summary>Requests of the run (", "request\n\n```json")
	if b, _ := os.ReadFile(filepath.Join(dir, "tree-review_gen.md")); strings.Contains(string(b), starportToken) {
		t.Error("the review must not show the token")
	}
	if b, _ := os.ReadFile(specPath); !strings.Contains(string(b), "example") {
		t.Error("the examples were not written into the spec")
	}
	var ju junitSuites
	b, _ := os.ReadFile(junit)
	if err := xml.Unmarshal(b, &ju); err != nil || len(ju.Suites) != 1 || ju.Suites[0].Tests < 20 || ju.Suites[0].Failures != 0 {
		t.Errorf("junit: %v %+v", err, ju)
	}
	// 5. run again on a fresh empty instance: compares with the answers;
	// building the test again keeps them
	if code, out, _ := cli("tree", "-spec", specPath, "-file", file); code != 0 || !hasAnswers(filepath.Join(dir, "tree-test_gen.yaml")) {
		t.Errorf("a rebuild must keep the answers: %d\n%s", code, out)
	}
	_, empty = starport(t, false)
	code, out, _ = cli("tree", "run", "-spec", specPath, "-file", file, "-base-url", empty, "-token", starportToken, "-run", "Dock/")
	if code != 0 || !strings.Contains(out, "PASSED") || strings.Contains(out, "Ship/ships1.read") {
		t.Errorf("run: %d\n%s", code, out)
	}
	// 6. an instance that is not empty fails
	_, notEmpty := starport(t, true)
	code, out, _ = cli("tree", "run", "-spec", specPath, "-file", file, "-base-url", notEmpty, "-token", starportToken)
	if code != 1 || !strings.Contains(out, "FAIL") {
		t.Errorf("a stage is no empty instance: %d\n%s", code, out)
	}
	review("| 4. Run (tree run) | failed | FAILED", "### Steps that did not pass", "| 5. Update (tree run -update) | done |", "**Next:** fix the failed steps")
}

func TestTreeUsage(t *testing.T) {
	specPath, file := treeProject(t)
	for _, args := range [][]string{
		{"tree"}, {"tree", "nope"}, {"tree", "run"}, {"tree", "-spec", specPath, "-file", file, "-nope"},
		{"tree", "run", "-spec", specPath, "-file", file},
	} {
		if code, out, errOut := cli(args...); code != 2 {
			t.Errorf("%v: code %d %s %s", args, code, out, errOut)
		}
	}
	if code, out, _ := cli("tree", "help"); code != 0 || !strings.Contains(out, "tree run") || !strings.Contains(out, "-update") {
		t.Errorf("help: %d %s", code, out)
	}
	// -help, also after run and other flags, explains the whole file
	for _, args := range [][]string{{"tree", "-help"}, {"tree", "--help"}, {"tree", "-h"}, {"tree", "run", "-help"}, {"tree", "-spec", "x.yaml", "-help"}} {
		code, out, _ := cli(args...)
		if code != 0 {
			t.Errorf("%v: code %d", args, code)
		}
		for _, want := range []string{"THE TREE FILE", "KEYS OF A NODE", "KEYS OF write", "ORDER", "VALUES", "present", "Flags of run:"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: no %q", args, want)
			}
		}
	}
	// a broken tree file
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("tree:\n  a: {call: GET /planets, pick: zero}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := cli("tree", "-spec", specPath, "-file", file); code != 1 || !strings.Contains(errOut, "pick") {
		t.Errorf("broken file: %d %s", code, errOut)
	}
	if err := os.WriteFile(file, []byte("tree:\n  a: GET /planets/{code}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := cli("tree", "-spec", specPath, "-file", file); code != 1 || !strings.Contains(errOut, "no value for the path parameter") {
		t.Errorf("a file that does not fit the spec: %d %s", code, errOut)
	}
	if code, _, errOut := cli("tree", "-spec", "missing.yaml", "-file", file); code != 1 || errOut == "" {
		t.Errorf("missing spec: %d %s", code, errOut)
	}
	_, empty := starport(t, false)
	if code, _, errOut := cli("tree", "run", "-spec", specPath, "-file", file, "-base-url", empty, "-token-env", "CLI_TREE_UNSET"); code != 2 || !strings.Contains(errOut, "CLI_TREE_UNSET") {
		t.Errorf("an unset token variable: %d %s", code, errOut)
	}
}

// seedBookstore creates authors and books on a Bookstore through its API.
func seedBookstore(t *testing.T, base string) {
	t.Helper()
	post := func(path, body string) {
		req, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+storeToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST %s: %v %v", path, err, resp)
		}
		resp.Body.Close()
	}
	post("/authors", `{"name":"Ada Lovelace","born":1815}`)
	post("/authors", `{"name":"Lin Starling","born":1950}`)
	post("/authors/2/books", `{"title":"Notes","price":12}`)
}

func TestRegressionPrePost(t *testing.T) {
	dir := t.TempDir()
	srv, base := startBookstore(t)
	// data on the stage
	seedBookstore(t, base)
	sample := filepath.Join(dir, "regression-sample.yaml")
	code, out, errOut := cli("regression", "init", "-spec", bookstoreSpec, "-out", sample)
	if code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errOut)
	}
	text, _ := os.ReadFile(sample)
	if !strings.Contains(string(text), "from: GET /authors") || !strings.Contains(string(text), "authorId: /id") {
		t.Errorf("proposed sample:\n%s", text)
	}
	// keep what the user changed
	changed := strings.Replace(string(text), "pick: 5", "pick: 2", 1)
	if err := os.WriteFile(sample, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := cli("regression", "init", "-spec", bookstoreSpec, "-out", sample); code != 0 {
		t.Fatal("second init failed")
	}
	if text, _ := os.ReadFile(sample); !strings.Contains(string(text), "pick: 2") {
		t.Errorf("init must keep the sets of the file:\n%s", text)
	}
	outDir := filepath.Join(dir, "out")
	code, out, errOut = cli("regression", "pre", "-spec", bookstoreSpec, "-sample", sample, "-base-url", base, "-token", storeToken, "-out", outDir, "-probe", "10ms", "-seed", "7")
	if code != 0 {
		t.Fatalf("pre: %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "sample  seed 7") || !strings.Contains(out, "2 of 3 candidates") {
		t.Errorf("pre output:\n%s", out)
	}
	pre, err := scenario.LoadSnapshot(filepath.Join(outDir, "pre-snapshot_gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pre.Steps) < 3 || pre.Seed != "7" || len(pre.Sample) != 1 {
		t.Errorf("pre snapshot: %+v", pre)
	}
	// nothing changed: post passes
	code, out, errOut = cli("regression", "post", "-spec", bookstoreSpec, "-base-url", base, "-token", storeToken, "-out", outDir, "-probe", "0", "-sample", sample)
	if code != 0 || !strings.Contains(out, "PASSED") {
		t.Fatalf("post without change: %d\n%s\n%s", code, out, errOut)
	}
	// the "deployment" changes the name of a sampled author
	id := scenario.Text(pre.Sample[0].Picked[0]["authorId"])
	req, _ := http.NewRequest(http.MethodPut, base+"/authors/"+id, strings.NewReader(`{"name":"Someone Else"}`))
	req.Header.Set("Authorization", "Bearer "+storeToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("update: %v %v", err, resp)
	}
	resp.Body.Close()
	_ = srv
	code, out, _ = cli("regression", "post", "-spec", bookstoreSpec, "-base-url", base, "-token", storeToken, "-out", outDir, "-probe", "0", "-junit", filepath.Join(outDir, "junit.xml"))
	if code != 1 || !strings.Contains(out, "FIELD_CHANGED") || !strings.Contains(out, "Someone Else") {
		t.Errorf("post after a change: %d\n%s", code, out)
	}
	md, _ := os.ReadFile(filepath.Join(outDir, "diff-report_gen.md"))
	html, _ := os.ReadFile(filepath.Join(outDir, "diff-report_gen.html"))
	if !strings.Contains(string(md), "**FAILED**") || !strings.Contains(string(html), "FIELD_CHANGED") {
		t.Errorf("diff report:\n%s", md)
	}
	code, out, _ = cli("regression", "diff", "-pre", filepath.Join(outDir, "pre-snapshot_gen.yaml"), "-post", filepath.Join(outDir, "post-snapshot_gen.yaml"), "-out", outDir, "-junit", filepath.Join(outDir, "junit-diff.xml"))
	if code != 1 || !strings.Contains(out, "FIELD_CHANGED") {
		t.Errorf("diff: %d\n%s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(outDir, "junit-diff.xml")); !strings.Contains(string(b), "REGRESSION") {
		t.Errorf("junit diff:\n%s", b)
	}
	for _, args := range [][]string{
		{"regression"}, {"regression", "nope"}, {"regression", "pre"}, {"regression", "post"}, {"regression", "diff"},
		{"regression", "init"}, {"regression", "diff", "-pre", "a", "-post", "b", "-fail-on", "x"},
	} {
		code, out, errOut := cli(args...)
		if len(args) == 1 {
			if code != 0 || !strings.Contains(out, "regression pre") {
				t.Errorf("help: %d %s", code, out)
			}
			continue
		}
		if code == 0 {
			t.Errorf("%v must fail: %s %s", args, out, errOut)
		}
	}
}

func TestTreeTagsStepByStep(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tree.yaml")
	spec := "../../testdata/gen/tree-colony.yaml"
	// 1. two tags, an operation left out
	code, out, errOut := cli("tree", "-spec", spec, "-file", file, "-tags", "Garden, Bed", "-exclude-ops", "getOpen*")
	if code != 0 || !strings.Contains(out, "proposed from the spec") {
		t.Fatalf("propose: %d\n%s%s", code, out, errOut)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), "tags: [Garden, Bed]") || !strings.Contains(string(b), "ops: [getOpen*]") || strings.Contains(string(b), "getOpenGardens") ||
		strings.Contains(string(b), "listPersons") {
		t.Fatalf("first step:\n%s", b)
	}
	// 2. one more tag: the file keeps what it has, the scope grows
	code, out, errOut = cli("tree", "-spec", spec, "-file", file, "-tags", "Moon", "-exclude-tags", "Admin")
	if code != 0 || !strings.Contains(out, "scope: tag Moon") || !strings.Contains(out, "scope: excluded tag Admin") {
		t.Fatalf("second step: %d\n%s%s", code, out, errOut)
	}
	b, _ = os.ReadFile(file)
	if !strings.Contains(string(b), "tags: [Garden, Bed, Moon]") || !strings.Contains(string(b), "listMoons") {
		t.Errorf("second step:\n%s", b)
	}
	if !strings.Contains(out, "params without value") {
		t.Errorf("the open values are printed:\n%s", out)
	}
	// 3. a node fetched again needs a fetch before
	srv := testserver.NewColony("colony-token", 500)
	srv.Fill()
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	t.Setenv("CLI_COLONY_TOKEN", "colony-token")
	code, out, errOut = cli("tree", "-spec", spec, "-file", file, "-base-url", hs.URL+"/api", "-token-env", "CLI_COLONY_TOKEN")
	if code != 0 {
		t.Fatalf("fetch: %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = cli("tree", "-spec", spec, "-file", file, "-base-url", hs.URL+"/api", "-token-env", "CLI_COLONY_TOKEN", "-node", "beds")
	if code != 0 || !strings.Contains(out, "only beds was fetched again") {
		t.Errorf("fetch one node: %d\n%s%s", code, out, errOut)
	}
}
