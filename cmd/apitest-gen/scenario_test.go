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

// contractProject writes a settings file and a copy of the spec, the way
// a project keeps them: apitest/contract/contract-settings.yaml.
func contractProject(t *testing.T, extra string) (settings, specPath string) {
	t.Helper()
	dir := t.TempDir()
	b, err := os.ReadFile(bookstoreSpec)
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
	settings = filepath.Join(dir, "apitest", "contract", "contract-settings.yaml")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	text := "spec: ../../api/openapi.yaml\nhealth: /health\ntokens:\n  default: env:CLI_STORE_TOKEN\nreport: out/contract-report_gen.md\n" + extra
	if err := os.WriteFile(settings, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLI_STORE_TOKEN", storeToken)
	return settings, specPath
}

func TestContractInitAndRun(t *testing.T) {
	settings, specPath := contractProject(t, "records: 2\n")
	_, base := startBookstore(t)
	dir := filepath.Dir(settings)
	// overrides: a fixed value and a step that is left out
	if err := os.WriteFile(filepath.Join(dir, "contract-overrides.yaml"), []byte("values:\n  Shelf.name: Garden Tools\nskip: [shelf1.create.forbidden]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := cli("contract", "init", "-settings", settings, "-base-url", base, "-forbidden-token", storeReader, "-junit", filepath.Join(dir, "out", "junit.xml"))
	if code != 0 {
		t.Fatalf("init: code %d\n%s\n%s", code, out, errOut)
	}
	for _, f := range []string{"contract-scenario_gen.yaml", "contract-golden_gen.yaml", "contract-review_gen.md", "out/contract-report_gen.md", "out/contract-report_gen.html", "out/junit.xml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	sc, err := scenario.Load(filepath.Join(dir, "contract-scenario_gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if sc.Step("shelf1.create.forbidden") != nil {
		t.Error("the skipped step is still in the scenario")
	}
	if st := sc.Step("shelf1.create"); st == nil || scenario.Text(st.Body.(map[string]any)["name"]) != "Garden Tools" {
		t.Errorf("the override value is missing: %+v", st)
	}
	review, _ := os.ReadFile(filepath.Join(dir, "contract-review_gen.md"))
	if !strings.Contains(string(review), "# Review: contract scenario") || !strings.Contains(string(review), "every step answered as expected") {
		t.Errorf("review:\n%s", review)
	}
	spec, _ := os.ReadFile(specPath)
	if !strings.Contains(string(spec), "Garden Tools") {
		t.Error("the examples were not written into the spec")
	}
	var ju junitSuites
	b, _ := os.ReadFile(filepath.Join(dir, "out", "junit.xml"))
	if err := xml.Unmarshal(b, &ju); err != nil || len(ju.Suites) != 1 || ju.Suites[0].Tests < 40 || ju.Suites[0].Failures != 0 {
		t.Errorf("junit: %v %+v", err, ju)
	}

	// run against a fresh instance: green
	_, base = startBookstore(t)
	code, out, errOut = cli("contract", "run", "-settings", settings, "-base-url", base, "-forbidden-token-env", "CLI_STORE_READER")
	if code == 0 || !strings.Contains(errOut, "CLI_STORE_READER") {
		t.Errorf("an empty token variable must fail: %d\n%s%s", code, out, errOut)
	}
	t.Setenv("CLI_STORE_READER", storeReader)
	code, out, errOut = cli("contract", "run", "-settings", settings, "-base-url", base, "-forbidden-token-env", "CLI_STORE_READER")
	if code != 0 || !strings.Contains(out, "PASSED") {
		t.Fatalf("run: code %d\n%s\n%s", code, out, errOut)
	}
	// only the 401 steps of Author
	_, base = startBookstore(t)
	code, out, _ = cli("contract", "run", "-settings", settings, "-base-url", base, "-errors", "401", "-run", "Author/")
	if code != 0 || strings.Contains(out, ".forbidden") || !strings.Contains(out, "author1.read.unauthorized") || strings.Contains(out, "Book/") {
		t.Errorf("filtered run: %d\n%s", code, out)
	}
	// a broken instance fails
	srv, base := startBookstore(t)
	srv.Faults.ExampleMismatch = true
	code, out, _ = cli("contract", "run", "-settings", settings, "-base-url", base, "-junit", filepath.Join(dir, "out", "junit-run.xml"))
	if code != 1 || !strings.Contains(out, "FAIL") {
		t.Errorf("a broken instance must fail: %d\n%s", code, out)
	}
}

func TestContractInitFailsAndReviews(t *testing.T) {
	settings, _ := contractProject(t, "")
	srv, base := startBookstore(t)
	srv.Faults.WrongStatus = true
	code, out, _ := cli("contract", "init", "-settings", settings, "-base-url", base)
	if code != 1 {
		t.Fatalf("init against a broken instance: code %d\n%s", code, out)
	}
	dir := filepath.Dir(settings)
	if _, err := os.Stat(filepath.Join(dir, "contract-golden_gen.yaml")); err == nil {
		t.Error("a failed init must not write the golden snapshot")
	}
	review, _ := os.ReadFile(filepath.Join(dir, "contract-review_gen.md"))
	if !strings.Contains(string(review), "author1.create") || !strings.Contains(string(review), "expect: 200") {
		t.Errorf("the review must name the step and the override:\n%s", review)
	}
	code, out, _ = cli("contract", "init", "-settings", settings, "-dry-run")
	if code != 0 || !strings.Contains(out, "steps:") {
		t.Errorf("dry run: %d\n%s", code, out)
	}
	for _, args := range [][]string{{"contract"}, {"contract", "nope"}, {"contract", "init"}, {"contract", "run", "-settings", "missing.yaml"}} {
		code, out, errOut := cli(args...)
		if args[len(args)-1] == "contract" {
			if code != 0 || !strings.Contains(out, "contract init") {
				t.Errorf("help: %d %s", code, out)
			}
			continue
		}
		if code != 2 {
			t.Errorf("%v: code %d %s %s", args, code, out, errOut)
		}
	}
}

func TestRegressionPrePost(t *testing.T) {
	dir := t.TempDir()
	srv, base := startBookstore(t)
	// data on the stage: the contract scenario leaves none, so create some
	settings, _ := contractProject(t, "")
	sdir := filepath.Dir(settings)
	if err := os.WriteFile(filepath.Join(sdir, "contract-overrides.yaml"), []byte("skip: [author2.delete, author1.delete, author1.read.afterDelete]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := cli("contract", "init", "-settings", settings, "-base-url", base, "-no-examples"); code != 0 {
		t.Fatalf("seeding the stage: %d\n%s%s", code, out, errOut)
	}
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
