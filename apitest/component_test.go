package apitest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fada4773-sketch/specproof/internal/testserver"
	tokenpkg "github.com/fada4773-sketch/specproof/internal/token"
)

// Component tests against the Bookstore test API (internal/testserver).

const (
	bookstoreSpec = "../testdata/bookstore.yaml"
	testToken     = "test-token-123"
)

// executionOrder is the expected order of a run without faults: groups
// follow their bindings (Author -> Book -> Review), DELETEs of groups that
// others depend on run last, in reverse order (FR-ORDER-01, TP-L2-07).
var executionOrder = []string{
	"Author/createAuthor/valid-author",
	"Author/getAuthor/default",
	"Author/listAuthors/default",
	"Author/updateAuthor/default",
	"Author/createAuthor/missing-name",
	"Book/createBook/default",
	"Book/getBook/default",
	"Book/updateBook/default",
	"Review/createReview/default",
	"Review/getReview/default",
	"Review/deleteReview/default",
	"Shelf/createShelf/default",
	"Shelf/getShelf/default",
	"Shelf/updateShelf/rename",
	"Shelf/deleteShelf/default",
	"System/getHealth/default",
	"System/getStats/default",
	"Book/deleteBook/default",
	"Author/deleteAuthor/default",
}

// securedOps are the operations that document 401; each gets an
// unauthorized and an invalid-token case (FR-CASE-08, FR-CASE-10).
var securedOps = map[string][]string{
	"Author": {"createAuthor", "listAuthors", "getAuthor", "updateAuthor", "deleteAuthor"},
	"Book":   {"createBook", "getBook", "updateBook", "deleteBook"},
	"Review": {"createReview", "getReview", "deleteReview"},
	"Shelf":  {"createShelf", "getShelf", "updateShelf", "deleteShelf"},
	"System": {"getStats"},
}

// forbiddenOps are marked with x-apitest-forbidden and get a forbidden case
// (FR-CASE-11).
var forbiddenOps = []string{"Author/updateAuthor", "Review/deleteReview", "Shelf/createShelf"}

// authCaseNames lists all authentication cases of the Bookstore.
func authCaseNames() []string {
	var out []string
	for g, ops := range securedOps {
		for _, op := range ops {
			out = append(out, g+"/"+op+"/unauthorized", g+"/"+op+"/invalid-token")
		}
	}
	for _, op := range forbiddenOps {
		out = append(out, op+"/forbidden")
	}
	return out
}

// baseline is the expected result of a run without faults: all cases pass.
var baseline = func() map[string]Status {
	m := map[string]Status{}
	for _, n := range executionOrder {
		m[n] = StatusPassed
	}
	for _, n := range authCaseNames() {
		m[n] = StatusPassed
	}
	return m
}()

// readerToken is accepted by the Bookstore for reads only.
const readerToken = "reader-token-456"

// bookstore returns the test API with the admin token testToken and the
// reader token readerToken.
func bookstore(f testserver.Faults) *testserver.Server {
	srv := testserver.New(testToken, testToken, f)
	srv.Readers = []string{readerToken}
	return srv
}

// isAuthCase reports whether name is an authentication case.
func isAuthCase(name string) bool {
	return strings.HasSuffix(name, "/unauthorized") || strings.HasSuffix(name, "/invalid-token") || strings.HasSuffix(name, "/forbidden")
}

type runOut struct {
	res    *Result
	ft     *fakeT
	report string
}

func bookstoreConfig(t *testing.T, f testserver.Faults) Config {
	t.Helper()
	return Config{
		SpecPath:       bookstoreSpec,
		Handler:        bookstore(f),
		Token:          StaticToken(testToken),
		ForbiddenToken: StaticToken(readerToken),
		ReportPath:     filepath.Join(t.TempDir(), "report.md"),
	}
}

func runFake(t *testing.T, cfg Config) runOut {
	t.Helper()
	ft := newFakeT(t, "TestBookstore")
	res := run(ft, cfg)
	ft.finish()
	var report string
	if res.Report != "" {
		b, err := os.ReadFile(res.Report)
		if err != nil {
			t.Fatalf("report not written: %v", err)
		}
		report = string(b)
	}
	return runOut{res, ft, report}
}

func statuses(res *Result) map[string]Status {
	m := map[string]Status{}
	for _, c := range res.Cases {
		m[c.Name] = c.Status
	}
	return m
}

func order(res *Result) []string {
	var out []string
	for _, c := range res.Cases {
		out = append(out, c.Name)
	}
	return out
}

// assertOnlyChanged checks that a run differs from the baseline in exactly
// the given cases (AK-02: "all other cases remain unchanged").
func assertOnlyChanged(t *testing.T, res *Result, changed map[string]Status) {
	t.Helper()
	got := statuses(res)
	want := map[string]Status{}
	for k, v := range baseline {
		want[k] = v
	}
	for k, v := range changed {
		want[k] = v
	}
	if !reflect.DeepEqual(got, want) {
		for k := range want {
			if got[k] != want[k] {
				t.Errorf("%s: got %s, want %s (%s)", k, got[k], want[k], message(res, k))
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("unexpected case %s: %s", k, got[k])
			}
		}
	}
}

func message(res *Result, name string) string {
	for _, c := range res.Cases {
		if c.Name == name {
			return c.Message
		}
	}
	return ""
}

func TestTPL2_01_BaselineAllPassed(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	res := Run(t, cfg) // real *testing.T: a failure here is a real failure
	assertOnlyChanged(t, res, nil)
	b, err := os.ReadFile(res.Report)
	if err != nil {
		t.Fatal(err)
	}
	report := string(b)
	if strings.Contains(report, "## ❌ Errors") || !strings.Contains(report, "| Result | ✅ passed |") {
		t.Errorf("report of a clean run must not contain an error section:\n%s", report)
	}
	if res.Summary.Operations != 18 || res.Summary.OperationsCovered != 18 {
		t.Errorf("coverage: %+v", res.Summary)
	}
	if !strings.Contains(report, `parameter "authorId" is resolved heuristically from createAuthor`) {
		t.Errorf("heuristic bindings must be reported as spec findings (FR-PARAM-06):\n%s", report)
	}
}

func TestTPL2_02_HandlerAndBaseURL(t *testing.T) {
	viaHandler := runFake(t, bookstoreConfig(t, testserver.Faults{}))

	srv := httptest.NewServer(bookstore(testserver.Faults{}))
	defer srv.Close()
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.Handler = nil
	cfg.BaseURL = srv.URL + "/v1"
	cfg.HealthPath = "/health"
	viaBaseURL := runFake(t, cfg)

	if !reflect.DeepEqual(statuses(viaHandler.res), statuses(viaBaseURL.res)) {
		t.Errorf("results differ:\n handler %v\n baseURL %v", statuses(viaHandler.res), statuses(viaBaseURL.res))
	}
	assertOnlyChanged(t, viaBaseURL.res, nil)
}

func TestTPL2_03_TokenPlacement(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	var mu sync.Mutex
	seen := map[string]*http.Request{}
	cfg.Hooks.BeforeRequest = func(_ context.Context, c *Case, req *http.Request) error {
		mu.Lock()
		defer mu.Unlock()
		seen[c.Name] = req.Clone(context.Background())
		return nil
	}
	out := runFake(t, cfg)
	assertOnlyChanged(t, out.res, nil)

	if h := seen["System/getHealth/default"]; h == nil || h.Header.Get("Authorization") != "" {
		t.Errorf("security: [] must be sent without token: %v", h.Header)
	}
	if s := seen["System/getStats/default"]; s == nil || s.URL.Query().Get("api_key") != testToken || s.Header.Get("Authorization") != "" {
		t.Errorf("apiKey in query expected: %v %v", s.URL, s.Header)
	}
	if a := seen["Author/getAuthor/default"]; a == nil || a.Header.Get("Authorization") != "Bearer "+testToken {
		t.Errorf("bearer token expected: %v", a.Header)
	}
}

func TestTPL2_06_TokenSourceError(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	calls := 0
	cfg.Token = TokenFunc(func(context.Context) (string, error) {
		calls++
		return "", errors.New("IdP not reachable")
	})
	out := runFake(t, cfg)
	if n := out.ft.errorCount(); n != 1 {
		t.Errorf("got %d errors, want exactly one:\n%s", n, out.ft.output())
	}
	if calls != 1 {
		t.Errorf("token source asked %d times, want 1", calls)
	}
	counts := out.res.Summary.Counts
	if counts[StatusError] != 1 || counts[StatusSkipped] != len(baseline)-1 {
		t.Errorf("want one ERROR and the rest SKIPPED, got %v", counts)
	}
	if !strings.Contains(out.report, "run aborted") {
		t.Error("report must mention the abort")
	}
}

func TestTPL2_10_WrongStatus(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{WrongStatus: true}))
	// The author was created (2xx), so its dependents still get their value.
	assertOnlyChanged(t, out.res, map[string]Status{"Author/createAuthor/valid-author": StatusFailed})
	if !out.res.Failed || !out.ft.failed {
		t.Error("run must fail the test")
	}
}

func TestTPL2_11_UndocumentedStatus(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{UndocumentedStatus: true}))
	assertOnlyChanged(t, out.res, map[string]Status{"Book/getBook/default": StatusFailed})
	if msg := message(out.res, "Book/getBook/default"); !strings.Contains(msg, "not documented") {
		t.Errorf("message: %q", msg)
	}
}

func TestTPL2_12_SchemaViolation(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{SchemaViolation: true}))
	assertOnlyChanged(t, out.res, map[string]Status{"Book/getBook/default": StatusSchemaViolation})
	if !strings.Contains(out.report, "- /authorId: ") {
		t.Errorf("report must name the pointer /authorId:\n%s", out.report)
	}
}

func TestTPL2_13_ExampleMismatch(t *testing.T) {
	out := runFake(t, bookstoreConfig(t, testserver.Faults{ExampleMismatch: true}))
	assertOnlyChanged(t, out.res, map[string]Status{"Author/createAuthor/valid-author": StatusExampleMismatch})
	if !strings.Contains(out.report, "| `/name` | `\"Ada Lovelace\"` | `\"Someone Else\"` |") {
		t.Errorf("report must contain the diff:\n%s", out.report)
	}
}

func TestTPL2_16_SlowEndpointTimesOut(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{Slow: true})
	cfg.RequestTimeout = time.Second
	start := time.Now()
	out := runFake(t, cfg)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("run took %s, the timeout must end the slow request", d)
	}
	assertOnlyChanged(t, out.res, map[string]Status{"Author/listAuthors/default": StatusError})
	if msg := message(out.res, "Author/listAuthors/default"); !strings.Contains(msg, "timeout") && !strings.Contains(msg, "Timeout") {
		t.Errorf("message: %q", msg)
	}
}

func TestTPL2_19_PanicInHook(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.Hooks.AfterResponse = func(_ context.Context, c *Case, _ *Response) error {
		if c.Name == "Book/getBook/default" {
			panic("broken")
		}
		return nil
	}
	out := runFake(t, cfg)
	assertOnlyChanged(t, out.res, map[string]Status{"Book/getBook/default": StatusError})
	if msg := message(out.res, "Book/getBook/default"); !strings.Contains(msg, "panic in AfterResponse: broken") {
		t.Errorf("message: %q", msg)
	}
}

func randomSecret(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b) // 64 characters
}

func TestTPL2_20_NoTokenInOutput(t *testing.T) {
	token, reader := randomSecret(t), randomSecret(t)
	// The server expects a different API key, so getStats fails and its URL
	// with the api_key query parameter ends up in the report. IgnoreSignature
	// makes the invalid-token cases fail, so the manipulated token is shown
	// in the report as well.
	srv := testserver.New(token, "other-key", testserver.Faults{WrongStatus: true, IgnoreSignature: true, IgnoreRoles: true})
	srv.Readers = []string{reader}
	cfg := Config{
		SpecPath:       bookstoreSpec,
		Handler:        srv,
		Token:          StaticToken(token),
		ForbiddenToken: StaticToken(reader),
		ReportPath:     filepath.Join(t.TempDir(), "report.md"),
		ReportJSON:     true,
	}
	out := runFake(t, cfg)
	if s := statuses(out.res); s["System/getStats/invalid-token"] != StatusFailed || s["Author/getAuthor/invalid-token"] != StatusFailed || s["Author/updateAuthor/forbidden"] != StatusFailed {
		t.Fatalf("the failing cases with tokens in the report are missing: %v", s)
	}
	tampered := tokenpkg.Tamper(token)
	jsonReport, err := os.ReadFile(strings.TrimSuffix(out.res.Report, ".md") + ".json")
	if err != nil {
		t.Fatal(err)
	}
	for where, text := range map[string]string{"report": out.report, "JSON report": string(jsonReport), "go test output": out.ft.output()} {
		for name, secret := range map[string]string{"token": token, "manipulated token": tampered, "forbidden token": reader} {
			if strings.Contains(text, secret) {
				t.Errorf("%s found in %s", name, where)
			}
		}
	}
	for _, want := range []string{`-H "Authorization: Bearer $TOKEN"`, `/stats?api_key=$INVALID_TOKEN`, "/stats?api_key=***", `-H "Authorization: Bearer $INVALID_TOKEN"`, `-H "Authorization: Bearer $FORBIDDEN_TOKEN"`} {
		if !strings.Contains(out.report, want) {
			t.Errorf("report should contain %q", want)
		}
	}
}

func TestTPL2_30_ParallelRunsSeparateReports(t *testing.T) {
	spec, err := filepath.Abs(bookstoreSpec)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	cfgFor := func() Config {
		return Config{SpecPath: spec, Handler: bookstore(testserver.Faults{}), Token: StaticToken(testToken), ForbiddenToken: StaticToken(readerToken)}
	}
	var wg sync.WaitGroup
	results := make([]*Result, 2)
	for i, name := range []string{"TestA", "TestB"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ft := newFakeT(t, name)
			results[i] = run(ft, cfgFor())
			ft.finish()
		}()
	}
	wg.Wait()
	if results[0].Report == results[1].Report {
		t.Fatalf("both runs wrote %s", results[0].Report)
	}
	for _, r := range results {
		if filepath.Base(filepath.Dir(r.Report)) != "apitest-report" {
			t.Errorf("default report path: %s", r.Report)
		}
		if _, err := os.Stat(r.Report); err != nil {
			t.Error(err)
		}
	}

	// Same explicit path while the first run is still active: the second
	// run must refuse to start.
	started, release := make(chan struct{}), make(chan struct{})
	first := cfgFor()
	first.ReportPath = "shared.md"
	first.Hooks.BeforeGroup = func(context.Context, string) error {
		select {
		case <-started:
		default:
			close(started)
			<-release
		}
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ft := newFakeT(t, "TestFirst")
		run(ft, first)
		ft.finish()
	}()
	<-started
	second := cfgFor()
	second.ReportPath = "shared.md"
	ft := newFakeT(t, "TestSecond")
	res := run(ft, second)
	close(release)
	<-done
	if !res.Failed || !strings.Contains(ft.output(), "already in use by another active run") {
		t.Errorf("second run with the same report path must fail: %s", ft.output())
	}
}

// TestTPL2_31_DeadlineLeavesReport runs a slow suite in a subprocess with a
// short -test.timeout. The run must stop starting cases before the deadline
// and still write the report (FR-GO-06, FR-REP-01).
func TestTPL2_31_DeadlineLeavesReport(t *testing.T) {
	if os.Getenv("APITEST_HELPER") == "deadline" {
		cfg := Config{
			SpecPath:       bookstoreSpec,
			Handler:        bookstore(testserver.Faults{Slow: true}),
			Token:          StaticToken(testToken),
			RequestTimeout: 3 * time.Second,
			ReportPath:     os.Getenv("APITEST_HELPER_REPORT"),
		}
		Run(t, cfg)
		return
	}
	if testing.Short() {
		t.Skip("subprocess test")
	}
	report := filepath.Join(t.TempDir(), "report.md")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestTPL2_31_DeadlineLeavesReport$", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "APITEST_HELPER=deadline", "APITEST_HELPER_REPORT="+report)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("subprocess should fail because of the slow case, got %v\n%s", err, out)
	}
	if strings.Contains(string(out), "test timed out") {
		t.Fatalf("go test killed the run; deadline handling did not work:\n%s", out)
	}
	if d := time.Since(start); d > 9*time.Second {
		t.Errorf("subprocess took %s", d)
	}
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("no report after deadline: %v\n%s", err, out)
	}
	if !strings.Contains(string(b), "go test deadline reached") {
		t.Errorf("report must list cases skipped because of the deadline:\n%s", b)
	}
}

func TestRunFilterRunsPreconditions(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	ft := newFakeT(t, "TestBookstore")
	ft.filter = func(name string) bool { return strings.Contains(name, "/Book/") }
	res := run(ft, cfg)
	ft.finish()
	pre := map[string]bool{}
	for _, c := range res.Cases {
		pre[c.Name] = c.Precondition
		if c.Status != StatusPassed {
			t.Errorf("%s: %s %s", c.Name, c.Status, c.Message)
		}
	}
	want := map[string]bool{"Author/createAuthor/valid-author": true}
	for n := range baseline {
		if strings.HasPrefix(n, "Book/") {
			want[n] = false
		}
	}
	if !reflect.DeepEqual(pre, want) {
		t.Errorf("cases and preconditions: got %v, want %v", pre, want)
	}
	if res.Summary.NotSelected != len(baseline)-len(want) {
		t.Errorf("not selected: %d", res.Summary.NotSelected)
	}
	if len(ft.subs) != len(want)-1 {
		t.Errorf("preconditions must not run as subtests: %d subtests", len(ft.subs))
	}
}

func TestConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no spec", Config{BaseURL: "http://127.0.0.1:1"}, "SpecPath missing"},
		{"no target", Config{SpecPath: bookstoreSpec}, "neither Config.BaseURL nor Config.Handler"},
		{"both targets", Config{SpecPath: bookstoreSpec, BaseURL: "http://x", Handler: http.NotFoundHandler()}, "both set"},
		{"bad mode", Config{SpecPath: bookstoreSpec, BaseURL: "http://x", CompareMode: "fuzzy"}, "fuzzy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFakeT(t, "T")
			res := run(ft, tc.cfg)
			if !res.Failed || !strings.Contains(ft.output(), tc.want) {
				t.Errorf("got %q, want %q", ft.output(), tc.want)
			}
		})
	}
}

func TestMissingTokenIsConfigError(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.Token = nil
	out := runFake(t, cfg)
	if !out.res.Failed || !strings.Contains(out.ft.output(), "Config.Token missing") {
		t.Errorf("got %s", out.ft.output())
	}
	if len(out.res.Cases) != 0 {
		t.Error("no request may be sent without token")
	}
}

func TestSpecErrorStillWritesReport(t *testing.T) {
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.SpecPath = "../testdata/specs/broken_ref.yaml"
	out := runFake(t, cfg)
	if !out.res.Failed || !strings.Contains(out.report, "Run aborted") || !strings.Contains(out.report, "Missing") {
		t.Errorf("minimal report expected, got:\n%s", out.report)
	}
}

func TestWriteProtection(t *testing.T) {
	srv := httptest.NewServer(bookstore(testserver.Faults{}))
	defer srv.Close()
	// Route every host to the test server, so a non-local BaseURL can be used
	// without network access.
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	cfg := bookstoreConfig(t, testserver.Faults{})
	cfg.Handler = nil
	cfg.BaseURL = "http://api.example.test/v1"
	cfg.HTTPClient = &http.Client{Transport: transport}

	out := runFake(t, cfg)
	for _, c := range out.res.Cases {
		method := strings.SplitN(c.Operation, " ", 2)[0]
		switch c.Status {
		case StatusError:
			if !strings.Contains(c.Message, "api.example.test refused") {
				t.Errorf("%s: %s", c.Name, c.Message)
			}
		case StatusPassed, StatusSkipped:
		default:
			t.Errorf("%s: unexpected %s %s (%s)", c.Name, c.Status, c.Message, method)
		}
	}
	if s := statuses(out.res); s["Author/createAuthor/valid-author"] != StatusError || s["System/getHealth/default"] != StatusPassed {
		t.Errorf("writes must be refused, reads allowed: %v", s)
	}

	cfg.AllowedHosts = []string{"api.example.test"}
	cfg.ReportPath = filepath.Join(t.TempDir(), "r2.md")
	assertOnlyChanged(t, runFake(t, cfg).res, nil)

	cfg.AllowedHosts = nil
	cfg.AllowRemoteWrites = true
	cfg.ReportPath = filepath.Join(t.TempDir(), "r3.md")
	assertOnlyChanged(t, runFake(t, cfg).res, nil)
}
