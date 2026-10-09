package report

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "update golden files")

func sample() *Report {
	return &Report{
		SpecTitle:   "Bookstore",
		SpecVersion: "1.0",
		OpenAPI:     "3.0.3",
		SpecFile:    "/specs/bookstore.yaml",
		BaseURL:     "http://127.0.0.1:1234",
		Started:     time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC),
		Duration:    1234 * time.Millisecond,
		GoVersion:   "go1.27.1",
		Version:     "(devel)",
		Strict:      true,
		Warnings:    []string{"The spec declares a base path."},
		Findings:    []Finding{{Where: "paths./x.get", Message: "example does | not match"}},
		Coverage: Coverage{
			Operations: 4, OperationsCovered: 3, Examples: 2, ExamplesCovered: 1,
			Uncovered: []Uncovered{{Operation: "upload", Reason: "media type not supported"}},
		},
		NotSelected:    2,
		DeviationsFile: "/specs/deviations.yaml",
		Deviations: []DeviationEntry{
			{Index: 1, Case: "Author/getAuthor/default", Rule: "200 → 404", Reason: "seed data missing", Ticket: "API-1", Expires: "2026-12-31", Uses: 1},
			{Index: 2, Case: "Book/*/default", Rule: "EXAMPLE_MISMATCH at /title", Reason: "trimmed", Expires: "2026-10-05", Uses: 1, Soon: true},
			{Index: 3, Case: "Shelf/*/default", Rule: "404 → 500", Reason: "old", Expires: "2026-01-01", Expired: true},
			{Index: 4, Case: "X/y/z", Rule: "400 → 200", Reason: "unused", Expires: "2027-01-01"},
		},
		Tolerate: true,
		Cases: []Case{
			{Name: "Author/createAuthor/valid", Group: "Author", Status: Passed, Method: "POST", Target: "/authors", Expected: "201", Actual: "201", Precondition: true, Code: 201, Duration: 42 * time.Millisecond},
			{Name: "Author/createAuthor/wrong", Group: "Author", Status: Failed, Code: 200, Duration: 8 * time.Millisecond, Method: "POST", Target: "/authors", Expected: "201", Actual: "200", Message: "expected 201, got 200",
				Request:  &Body{Title: "Request", Lang: "json", Text: "{\n  \"name\": \"Ada\"\n}"},
				Response: &Body{Title: "Response (200)", Lang: "json", Text: "{}"},
				Curl:     `curl -X POST "$BASE_URL/authors" -H "Authorization: Bearer $TOKEN" -d '{"name":"Ada"}'`},
			{Name: "Book/getBook/default", Group: "Book", Status: SchemaViolation, Code: 200, Duration: 130 * time.Millisecond, Method: "GET", Target: "/books/1", Expected: "200, Schema", Actual: "200", Message: "response violates the schema",
				Problems: []string{"/price: value must be a number"}},
			{Name: "Author/createAuthor/mismatch", Group: "Author", Status: ExampleMismatch, Code: 201, Duration: 12 * time.Millisecond, Method: "POST", Target: "/authors", Expected: "201, body ⊇ example", Actual: "201, body differs", Message: "response differs from the example",
				Diffs: []Diff{{Pointer: "/name", Expected: `"Ada"`, Actual: `"Bob"`}, {Pointer: "/x", Expected: `1`, Actual: "(missing)"}}},
			{Name: "Book/updateBook/default", Group: "Book", Status: DataMismatch, Code: 200, Duration: 1300 * time.Millisecond, Method: "PUT", Target: "/books/1", Message: "GET returns different values",
				Verify:   &Body{Title: "Response (GET /books/1, 200)", Lang: "json", Text: "{\n  \"title\": \"Go\"\n}"},
				Response: &Body{Title: "Response", Lang: "text", Text: strings.Repeat("a", 10), Original: 70000}},
			{Name: "Author/listAuthors/default", Group: "Author", Status: Error, Method: "GET", Target: "/authors", Message: "timeout after 1s"},
			{Name: "Author/getAuthor/default", Group: "Author", Status: Deviation, Code: 404, Duration: 3 * time.Millisecond, Method: "GET", Target: "/authors/1", Message: "allowed by API-1"},
			{Name: "Book/upload/default", Group: "Book", Status: NotBuildable, Message: "no value for /file"},
			{Name: "Book/deleteBook/default", Group: "Book", Status: Skipped, Message: "x-apitest-skip: later"},
			{Name: "Book/getBook/not-found", Group: "Book", Status: Passed, Kind: "not-found", ErrorCase: true, Method: "GET", Target: "/books/999999999", Expected: "404", Actual: "404", Code: 404, Duration: 5 * time.Millisecond},
			{Name: "Book/deleteBook/not-found", Group: "Book", Status: Tolerated, Tolerated: Failed, Kind: "not-found", ErrorCase: true, Method: "DELETE", Target: "/books/999999999", Expected: "404", Actual: "204",
				Code: 204, Duration: 6 * time.Millisecond, Message: "tolerated (FAILED): status code 204, expected 404"},
			{Name: "Book/createBook/conflict", Group: "Book", Status: Tolerated, Tolerated: Failed, Kind: "conflict", ErrorCase: true, Method: "POST", Target: "/books", Expected: "409", Actual: "500",
				Code: 500, Duration: 21 * time.Millisecond, Message: "tolerated (FAILED): status code 500, expected 409",
				Response: &Body{Title: "Response (500)", Lang: "json", Text: "{\n  \"message\": \"duplicate key <script>\"\n}"}},
		},
	}
}

func TestTPL1_23_Golden(t *testing.T) {
	got := Render(sample())
	path := filepath.Join("..", "..", "testdata", "golden", "report_all_statuses.md")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file missing, run with -update: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("report differs from %s; run `make update-golden` and review the diff.\n--- got ---\n%s", path, got)
	}
}

func TestRenderDeterministic(t *testing.T) {
	if !bytes.Equal(Render(sample()), Render(sample())) {
		t.Error("rendering the same report twice gave different output")
	}
}

func TestEmptySectionsOmitted(t *testing.T) {
	r := sample()
	r.Cases = r.Cases[:1]
	r.Findings = nil
	r.Deviations = nil
	out := string(Render(r))
	for _, section := range []string{"## ❌ Errors", "## 🟡 Deviations", "## ⏭ Not run", "## Spec findings"} {
		if strings.Contains(out, section) {
			t.Errorf("empty section %q must be omitted", section)
		}
	}
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "r.md")
	if err := Write(path, sample()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestCodeSpan(t *testing.T) {
	for in, want := range map[string]string{
		"a":    "`a`",
		"a`b":  "``a`b``",
		"`a":   "`` `a ``",
		"a|b":  "`a\\|b`",
		"":     "",
		"x\ny": "`x y`",
		"a``b": "```a``b```",
	} {
		if got := code(in); got != want {
			t.Errorf("code(%q): got %q, want %q", in, got, want)
		}
	}
}

// The HTML report is one self-contained page: deterministic, with the
// dashboard, the error case analysis and every case, and escaped text.
func TestRenderHTML(t *testing.T) {
	a, err := RenderHTML(sample())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RenderHTML(sample())
	if !bytes.Equal(a, b) {
		t.Error("HTML report is not deterministic")
	}
	page := string(a)
	for _, want := range []string{
		"<title>apitest · Bookstore 1.0</title>", `<div class="badge bad">✕ Failed</div>`,
		`id="dashboard"`, `aria-label="cases by status"`, "Slowest requests", "1.30 s", "Response times", "Status codes",
		`id="errorcases"`, `<b class="num">3</b>error cases`, "Book/deleteBook/not-found", "would be FAILED",
		`data-status="TOLERATED"`, "duplicate key &lt;script&gt;", "Reproduce", "API-1", "media type not supported",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("HTML misses %q", want)
		}
	}
	if strings.Contains(page, "duplicate key <script>") {
		t.Error("response text is not escaped")
	}
	for _, ext := range []string{"https://", "http://cdn", "<link "} {
		if strings.Contains(page, ext) {
			t.Errorf("HTML loads something external: %q", ext)
		}
	}
}

func TestStats(t *testing.T) {
	st := Compute(sample())
	if st.Total != 12 || st.Executed != 9 || st.Bad != 5 || st.Good != 5 {
		t.Errorf("counts: %+v", st)
	}
	if st.Max != 1300*time.Millisecond || st.Slowest[0].Name != "Book/updateBook/default" || st.P50 != 12*time.Millisecond {
		t.Errorf("times: max %v p50 %v slowest %s", st.Max, st.P50, st.Slowest[0].Name)
	}
	e := st.Errors
	if len(e.Cases) != 3 || e.AsDocumented != 1 || e.Other != 2 || strings.Join(e.Expected, ",") != "404,409" || strings.Join(e.Actual, ",") != "204,404,500" || e.Matrix["404"]["204"] != 1 {
		t.Errorf("errors: %+v", e)
	}
	if len(st.Groups) != 2 || st.Groups[1].Name != "Book" || st.Groups[1].Total != 7 {
		t.Errorf("groups: %+v", st.Groups)
	}
	if ms(0) != "–" || ms(1500*time.Microsecond) != "1 ms" || ms(2*time.Second) != "2.00 s" || bar(1, 4, 8) != "██░░░░░░" {
		t.Error("formatting")
	}
}
