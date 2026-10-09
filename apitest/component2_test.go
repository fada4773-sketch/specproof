package apitest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fada4773-sketch/specproof/internal/testserver"
)

// Component tests of phase 2: bindings, verification, order, deviations.

func TestTPL2_04_TokenSourceAskedPerRequest(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	srv := testserver.New("tok-1", testToken, testserver.Faults{})
	srv.Tokens = append(srv.Tokens, "tok-2")
	srv.Readers = []string{readerToken}
	cfg.Handler = srv
	var mu sync.Mutex
	calls, current := 0, ""
	cfg.Token = TokenFunc(func(context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		current = "tok-1"
		if calls >= 3 {
			current = "tok-2"
		}
		return current, nil
	})
	used := map[string]bool{}
	cfg.Hooks.BeforeRequest = func(_ context.Context, c *Case, req *http.Request) error {
		mu.Lock()
		defer mu.Unlock()
		if isAuthCase(c.Name) {
			return nil
		}
		if got := req.Header.Get("Authorization"); got != "" {
			if got != "Bearer "+current {
				t.Errorf("%s sent %q, but the token source last returned %q", c.Name, got, current)
			}
			used[strings.TrimPrefix(got, "Bearer ")] = true
		}
		return nil
	}
	// getStats sends the token as API key, which this server does not know.
	cfg.ExcludeOps = []string{"getStats"}
	out := runFake(t, cfg)
	if out.res.Failed || len(out.res.Cases) != len(baseline)-3 {
		t.Errorf("all cases must pass with the changing token:\n%s", out.ft.output())
	}
	if !used["tok-1"] || !used["tok-2"] {
		t.Errorf("both tokens must be used: %v", used)
	}
}

// targets records the request path of every case.
func targets(cfg *Config) (map[string]string, *sync.Mutex) {
	var mu sync.Mutex
	m := map[string]string{}
	cfg.Hooks.BeforeRequest = func(_ context.Context, c *Case, req *http.Request) error {
		mu.Lock()
		defer mu.Unlock()
		m[c.Name] = req.URL.Path
		return nil
	}
	return m, &mu
}

func TestTPL2_05_PutChangesBoundKey(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	seen, _ := targets(&cfg)
	out := runFake(t, cfg)
	assertOnlyChanged(t, out.res, nil)
	want := map[string]string{
		"Shelf/getShelf/default":    "/v1/shelves/l",
		"Shelf/updateShelf/rename":  "/v1/shelves/l",
		"Shelf/deleteShelf/default": "/v1/shelves/c", // follows the new code
		"Book/getBook/default":      "/v1/books/1",   // from the Location header
		"Book/createBook/default":   "/v1/authors/2/books",
		"Review/getReview/default":  "/v1/reviews/1", // from a link
	}
	for name, path := range want {
		if seen[name] != path {
			t.Errorf("%s: requested %s, want %s", name, seen[name], path)
		}
	}
}

func TestTPL2_07_Order(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{}))
	all := order(out.res)
	var regular []string
	for _, n := range all {
		if !isAuthCase(n) {
			regular = append(regular, n)
		}
	}
	if !reflect.DeepEqual(regular, executionOrder) {
		t.Errorf("order of the regular cases:\n got  %v\n want %v", regular, executionOrder)
	}
	// Every regular case runs first, DELETEs included; then the 4xx
	// examples and the authentication cases, in the order of the groups.
	seenOther := ""
	for _, n := range all {
		switch {
		case !isRegular(n) && seenOther == "":
			seenOther = n
		case isRegular(n) && seenOther != "":
			t.Errorf("regular case %s runs after %s", n, seenOther)
		}
	}
	if seenOther != "Author/createAuthor/missing-name" {
		t.Errorf("first other case: %s", seenOther)
	}
}

func TestTPL2_08_TagsContradictGraph(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.Tags = []string{"Review", "Book", "Author"}
	out := runFake(t, cfg)
	if !out.res.Failed || len(out.res.Cases) != 0 || !strings.Contains(out.ft.output(), "Config.Tags lists") {
		t.Errorf("want an error before the first request, got %d cases: %s", len(out.res.Cases), out.ft.output())
	}
	cfg.Tags = []string{"Shelf", "Author", "Book"}
	cfg.ReportPath = filepath.Join(t.TempDir(), "r.md")
	out = runFake(t, cfg)
	got := order(out.res)
	if len(got) != 42 || !strings.HasPrefix(got[0], "Shelf/") {
		t.Errorf("Tags selects and orders groups: %v", got)
	}
	for _, n := range got {
		if strings.HasPrefix(n, "Review/") || strings.HasPrefix(n, "System/") {
			t.Errorf("group not in Config.Tags was run: %s", n)
		}
	}
}

func TestTPL2_09_GroupHooksOncePerGroup(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	before, after := map[string]int{}, map[string]int{}
	cfg.Hooks.BeforeGroup = func(_ context.Context, g string) error { before[g]++; return nil }
	cfg.Hooks.AfterGroup = func(_ context.Context, g string) error { after[g]++; return nil }
	out := runFake(t, cfg)
	assertOnlyChanged(t, out.res, nil)
	want := map[string]int{"Author": 1, "Book": 1, "Review": 1, "Shelf": 1, "System": 1}
	if !reflect.DeepEqual(before, want) || !reflect.DeepEqual(after, want) {
		t.Errorf("BeforeGroup %v, AfterGroup %v, want %v", before, after, want)
	}

	cfg = bookstoreConfig(t, testserver.Faults{})
	cfg.Hooks.BeforeGroup = func(_ context.Context, g string) error {
		if g == "Shelf" {
			return errors.New("reset failed")
		}
		return nil
	}
	out = runFake(t, cfg)
	for _, c := range out.res.Cases {
		if c.Group == "Shelf" && (c.Status != StatusSkipped || !strings.Contains(c.Message, "BeforeGroup failed: reset failed")) {
			t.Errorf("%s: %s %s", c.Name, c.Status, c.Message)
		}
	}
	if !out.res.Failed {
		t.Error("a failing hook fails the run")
	}
}

func TestTPL2_14_DropOnUpdate(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{DropOnUpdate: true}))
	assertOnlyChanged(t, out.res, map[string]Status{"Book/updateBook/default": StatusDataMismatch})
	for _, want := range []string{"| `/title` | `\"Go 2\"` | `\"Go\"` |", "Response (GET /books/1, 200)"} {
		if !strings.Contains(out.report, want) {
			t.Errorf("report should contain %q:\n%s", want, out.report)
		}
	}
}

func TestTPL2_15_DeleteNoop(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{DeleteNoop: true}))
	assertOnlyChanged(t, out.res, map[string]Status{"Review/deleteReview/default": StatusDataMismatch})
	if msg := message(out.res, "Review/deleteReview/default"); !strings.Contains(msg, "still exists after DELETE") {
		t.Errorf("message: %q", msg)
	}
}

func TestTPL2_18_FailedProducerSkipsDependents(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{FailCreateAuthor: true}))
	skippedOps := []string{
		"Author/getAuthor/", "Author/updateAuthor/", "Author/deleteAuthor/",
		"Book/", "Review/",
	}
	var skipped []string
	for n := range baseline {
		for _, prefix := range skippedOps {
			if strings.HasPrefix(n, prefix) {
				skipped = append(skipped, n)
			}
		}
	}
	changed := map[string]Status{"Author/createAuthor/valid-author": StatusFailed}
	for _, n := range skipped {
		changed[n] = StatusSkipped
	}
	assertOnlyChanged(t, out.res, changed)
	for _, n := range skipped {
		if msg := message(out.res, n); !strings.Contains(msg, "Author/createAuthor/valid-author is FAILED") {
			t.Errorf("%s must name the cause: %q", n, msg)
		}
	}
	if n := out.ft.errorCount(); n != 1 {
		t.Errorf("only the cause is an error, got %d errors:\n%s", n, out.ft.output())
	}
}

func TestTPL2_21_AbortMidRun(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	n := 0
	cfg.Hooks.BeforeRequest = func(context.Context, *Case, *http.Request) error {
		n++
		if n > 3 {
			return errors.New("stop")
		}
		return nil
	}
	out := runFake(t, cfg)
	if s := statuses(out.res); s["Author/createAuthor/valid-author"] != StatusPassed || s["Author/updateAuthor/default"] != StatusError {
		t.Errorf("results: %v", s)
	}
	if !strings.Contains(out.report, "`Author/createAuthor/valid-author`") || !strings.Contains(out.report, "stop") {
		t.Errorf("report must contain the results so far:\n%s", out.report)
	}

	// Canceling the context ends the run; the report still exists.
	cfg = bookstoreConfig(t, testserver.Faults{})
	ctx, cancel := context.WithCancel(t.Context())
	cfg.Hooks.AfterResponse = func(_ context.Context, c *Case, _ *Response) error {
		if c.OperationID == "getAuthor" {
			cancel()
		}
		return nil
	}
	ft := newFakeT(t, "TestCancel")
	ft.ctx = ctx
	res := run(ft, cfg)
	ft.finish()
	if s := statuses(res); s["Author/listAuthors/default"] != StatusSkipped || !strings.Contains(message(res, "Author/listAuthors/default"), "canceled") {
		t.Errorf("after cancel: %v %q", s["Author/listAuthors/default"], message(res, "Author/listAuthors/default"))
	}
	if _, err := os.Stat(res.Report); err != nil {
		t.Errorf("report after cancel: %v", err)
	}
}

// timing matches the response times and durations of a report.
var timing = regexp.MustCompile(`[0-9.]+ ?(µs|ms|s)\b|[█░]+`)

// normalize removes values that differ between runs of the same state:
// the times, the base URL, the dashboard (its figures are times) and the
// Date header.
func normalize(report string) string {
	var out []string
	dashboard := false
	for _, line := range strings.Split(report, "\n") {
		switch {
		case strings.HasPrefix(line, "## 📈 Dashboard"):
			dashboard = true
		case strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "<details"):
			dashboard = false
		}
		if dashboard || strings.HasPrefix(line, "| `/") || strings.Contains(line, "⏱") ||
			strings.HasPrefix(strings.TrimSpace(line), "Date: ") { // response header, differs when a second passes
			continue
		}
		out = append(out, timing.ReplaceAllString(line, "T"))
	}
	return strings.Join(out, "\n")
}

func TestTPL2_22_ReportDeterministic(t *testing.T) {
	a := runFake(t, bookstoreConfig(t, testserver.Faults{DropOnUpdate: true, ExampleMismatch: true}))
	b := runFake(t, bookstoreConfig(t, testserver.Faults{DropOnUpdate: true, ExampleMismatch: true}))
	if normalize(a.report) != normalize(b.report) {
		t.Errorf("reports differ:\n--- a ---\n%s\n--- b ---\n%s", a.report, b.report)
	}
}

// TestTPL2_23_RunSelectsPreconditions runs "go test -run" on a single case in
// a subprocess: only that case runs as a subtest, its producers run as
// preconditions (FR-GO-02, AK-12).
func TestTPL2_23_RunSelectsPreconditions(t *testing.T) {
	if os.Getenv("APITEST_HELPER") == "run" {
		cfg := Config{
			SpecPath:   bookstoreSpec,
			Handler:    testserver.New(testToken, testToken, testserver.Faults{}),
			Token:      StaticToken(testToken),
			ReportPath: os.Getenv("APITEST_HELPER_REPORT"),
			ReportJSON: true,
		}
		Run(t, cfg)
		return
	}
	if testing.Short() {
		t.Skip("subprocess test")
	}
	report := filepath.Join(t.TempDir(), "report.md")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.v", "-test.run=^TestTPL2_23_RunSelectsPreconditions$/Book/updateBook/default")
	cmd.Env = append(os.Environ(), "APITEST_HELPER=run", "APITEST_HELPER_REPORT="+report)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\n%s", err, out)
	}
	var ran []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if name, ok := strings.CutPrefix(sc.Text(), "=== RUN   "); ok {
			ran = append(ran, name)
		}
	}
	want := []string{"TestTPL2_23_RunSelectsPreconditions", "TestTPL2_23_RunSelectsPreconditions/Book/updateBook/default"}
	if !reflect.DeepEqual(ran, want) {
		t.Errorf("subtests run: %v, want %v", ran, want)
	}
	raw, err := os.ReadFile(strings.TrimSuffix(report, ".md") + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var jr jsonReport
	if err := json.Unmarshal(raw, &jr); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range jr.Cases {
		got[c.Name] = c.Precondition
		if c.Status != StatusPassed {
			t.Errorf("%s: %s %s", c.Name, c.Status, c.Message)
		}
	}
	wantCases := map[string]bool{
		"Author/createAuthor/valid-author": true,
		"Book/createBook/default":          true,
		"Book/updateBook/default":          false,
	}
	if !reflect.DeepEqual(got, wantCases) {
		t.Errorf("cases in the report: %v, want %v", got, wantCases)
	}
}

func TestTPL2_24_ParallelRunsDifferentSpecs(t *testing.T) {
	var wg sync.WaitGroup
	var a, b *Result
	wg.Add(2)
	go func() {
		defer wg.Done()
		ft := newFakeT(t, "TestBookstore")
		a = run(ft, bookstoreConfig(t, testserver.Faults{}))
		ft.finish()
	}()
	go func() {
		defer wg.Done()
		ft := newFakeT(t, "TestHealth")
		b = run(ft, Config{
			SpecPath:   "../testdata/health.yaml",
			Handler:    testserver.New(testToken, testToken, testserver.Faults{}),
			ReportPath: filepath.Join(t.TempDir(), "health.md"),
		})
		ft.finish()
	}()
	wg.Wait()
	assertOnlyChanged(t, a, nil)
	if b.Failed || len(b.Cases) != 1 || b.Cases[0].Status != StatusPassed {
		t.Errorf("health run: %+v", b.Cases)
	}
}

func TestTPL2_29_BindingFollowsStoredValue(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{DropShelfName: true})
	seen, _ := targets(&cfg)
	out := runFake(t, cfg)
	assertOnlyChanged(t, out.res, map[string]Status{"Shelf/updateShelf/rename": StatusDataMismatch})
	if seen["Shelf/deleteShelf/default"] != "/v1/shelves/c" {
		t.Errorf("the code was stored, so the binding must follow it: %s", seen["Shelf/deleteShelf/default"])
	}
	if !strings.Contains(message(out.res, "Shelf/updateShelf/rename"), "/name") {
		t.Errorf("message: %s", message(out.res, "Shelf/updateShelf/rename"))
	}
}

func writeDeviations(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "deviations.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const deviationsFile = `
- case: Author/createAuthor/valid-author
  expected: 201
  actual: 200
  reason: "server answers 200, spec will be fixed"
  ticket: BOOK-1
  expires: 2026-12-31
- case: Book/updateBook/default
  status: DATA_MISMATCH
  pointer: /title
  reason: "title is stored asynchronously"
  expires: 2026-10-05
- case: Shelf/*/default
  expected: 404
  actual: 500
  reason: "never used"
  expires: 2027-01-31
`

func TestDeviationsEndToEnd(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{WrongStatus: true, DropOnUpdate: true})
	cfg.DeviationsPath = writeDeviations(t, deviationsFile)
	cfg.Strict = true
	cfg.now = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
	out := runFake(t, cfg)
	assertOnlyChanged(t, out.res, map[string]Status{
		"Author/createAuthor/valid-author": StatusDeviation,
		"Book/updateBook/default":          StatusDeviation,
	})
	if out.res.Failed {
		t.Errorf("accepted deviations must not fail the run:\n%s", out.ft.output())
	}
	for _, want := range []string{
		"| 1 | `Author/createAuthor/valid-author` | 201 → 200 |",
		"| 2 | `Book/updateBook/default` | DATA_MISMATCH at /title |",
		"⚠️ expires soon",
		"| 3 | `Shelf/*/default` | 404 → 500 | never used |  | 2027-01-31 | 0 | unused, can be removed |",
		"deviation 2 (Book/updateBook/default) expires on 2026-10-05",
	} {
		if !strings.Contains(out.report, want) {
			t.Errorf("report should contain %q:\n%s", want, out.report)
		}
	}
	if !strings.Contains(message(out.res, "Author/createAuthor/valid-author"), "BOOK-1") {
		t.Errorf("message must name the ticket: %s", message(out.res, "Author/createAuthor/valid-author"))
	}

	// After the expiry date the entries fail a strict run (FR-DEV-03).
	cfg = bookstoreConfig(t, testserver.Faults{WrongStatus: true, DropOnUpdate: true})
	cfg.DeviationsPath = writeDeviations(t, deviationsFile)
	cfg.Strict = true
	cfg.now = func() time.Time { return time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC) }
	out = runFake(t, cfg)
	if !out.res.Failed || !strings.Contains(out.ft.output(), "deviation 2 (Book/updateBook/default) expired on 2026-10-05") {
		t.Errorf("expired entry must fail the strict run:\n%s", out.ft.output())
	}
	if !strings.Contains(out.report, "❗ expired") {
		t.Error("report must mark the expired entry")
	}

	// The same outcome without a matching entry is an error.
	cfg = bookstoreConfig(t, testserver.Faults{UndocumentedStatus: true})
	cfg.DeviationsPath = writeDeviations(t, deviationsFile)
	out = runFake(t, cfg)
	assertOnlyChanged(t, out.res, map[string]Status{"Book/getBook/default": StatusFailed})
}

func TestInvalidDeviationsFileAbortsBeforeRequests(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.DeviationsPath = writeDeviations(t, "- { case: x, expected: 200, actual: 404 }")
	out := runFake(t, cfg)
	if !out.res.Failed || len(out.res.Cases) != 0 || !strings.Contains(out.ft.output(), "missing reason, expires") {
		t.Errorf("got %d cases: %s", len(out.res.Cases), out.ft.output())
	}
}

func TestStrictNotBuildable(t *testing.T) {
	cfg := Config{
		SpecPath:   "../testdata/specs/cases.yaml",
		BaseURL:    "http://127.0.0.1:1",
		IncludeOps: []string{"needsBody"},
		ReportPath: filepath.Join(t.TempDir(), "r.md"),
	}
	// No server is needed: the only case cannot be built.
	cfg.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Body: http.NoBody}, nil
	})}
	out := runFake(t, cfg)
	if out.res.Failed {
		t.Errorf("NOT_BUILDABLE only fails in strict mode: %s", out.ft.output())
	}
	cfg.Strict = true
	cfg.ReportPath = filepath.Join(t.TempDir(), "r2.md")
	if out = runFake(t, cfg); !out.res.Failed {
		t.Error("NOT_BUILDABLE fails in strict mode")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
