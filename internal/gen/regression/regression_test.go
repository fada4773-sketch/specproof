package regression

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fada4773-sketch/specproof/internal/gen/discover"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

func writeSample(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "regression-sample.yaml")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadSample(t *testing.T) {
	s, err := LoadSample(writeSample(t, `
seed: 734291
probe: 500ms
lists: {key: code}
ignore: [updatedAt]
reads:
  exclude: [get /health]
  get /ships: {query: {dockId: '{{dock.dockId}}'}}
sets:
  dock:
    from: get /docks
    query: {pageSize: 50}
    pages: 2
    where: {name: present}
    check: ['GET /docks/{dockId}/ships']
    pick: 3
    as: {dockId: /id}
`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Seed != "734291" || s.Probe != 500*time.Millisecond || s.ListKey != "code" || s.Exclude[0] != "GET /health" || s.Reads["GET /ships"]["dockId"] != "{{dock.dockId}}" {
		t.Errorf("sample: %+v", s)
	}
	set := s.Sets[0]
	if set.From != "GET /docks" || set.Pages != 2 || set.Pick != 3 || set.As["dockId"] != "/id" || set.Check[0] != "GET /docks/{dockId}/ships" {
		t.Errorf("set: %+v", set)
	}
	b, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadSample(writeSample(t, string(b)))
	if err != nil || again.Sets[0].Pick != 3 || again.Probe != s.Probe {
		t.Errorf("round trip: %v\n%s", err, b)
	}
	tests := map[string]string{
		"- 1":                                 "is a mapping",
		"nope: 1":                             "unknown key",
		"seed: abc":                           "seed must be",
		"probe: soon":                         "probe:",
		"reads: {POST /x: {}}":                "is no GET",
		"sets: {a: 1}":                        "must be a mapping",
		"sets: {a: {nope: 1}}":                "unknown key",
		"sets: {a: {from: POST /x}}":          "with a GET",
		"sets: {a: {from: GET /x, pick: 0}}":  "pick must be",
		"sets: {a: {from: GET /x, pages: x}}": "pages must be",
		"sets: {a: {from: GET /x, check: [DELETE /x]}}": "reads with a GET",
		"sets: {a: {from: GET /x, as: {v: id}}}":        "is no JSON pointer",
		"sets: {a: {from: GET /x}}":                     "needs \"as\"",
	}
	for text, want := range tests {
		if _, err := LoadSample(writeSample(t, text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", text, err, want)
		}
	}
	if _, err := LoadSample(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing file must fail")
	}
}

func TestWhere(t *testing.T) {
	c := map[string]any{"name": "North", "year": json.Number("1965"), "nested": map[string]any{"x": 1}}
	tests := []struct {
		cond map[string]any
		want string
	}{
		{map[string]any{"name": "present"}, ""},
		{map[string]any{"missing": "present"}, "without missing"},
		{map[string]any{"name": "North"}, ""},
		{map[string]any{"name": "South"}, "name not South"},
		{map[string]any{"year": ">=1950"}, ""},
		{map[string]any{"year": "<1950"}, "year not <1950"},
		{map[string]any{"year": ">1965"}, "year not >1965"},
		{map[string]any{"year": "<=1965"}, ""},
		{map[string]any{"name": ">5"}, "name not >5"},
		{map[string]any{"/nested/x": "1"}, ""},
	}
	for _, tt := range tests {
		if got := where(c, tt.cond); got != tt.want {
			t.Errorf("%v: got %q, want %q", tt.cond, got, tt.want)
		}
	}
	if p, ok := fillPath("/docks/{dockId}/x", map[string]any{"dockId": "a b"}); !ok || p != "/docks/a%20b/x" {
		t.Errorf("fillPath: %s", p)
	}
	if _, ok := fillPath("/docks/{other}", map[string]any{}); ok {
		t.Error("a missing value must fail")
	}
	if sanitize("GET /a b") != "GET_a_b" || sanitize("///") != "_" {
		t.Errorf("sanitize: %q", sanitize("GET /a b"))
	}
}

// stage is a small stage: docks with ships; dock 3 has no ships.
func stage(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	docks := []map[string]any{{"id": 1, "name": "North"}, {"id": 2, "name": "South"}, {"id": 3, "name": "East"}, {"id": 4}}
	mux.HandleFunc("GET /docks", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": docks, "total": 4})
	})
	mux.HandleFunc("GET /docks/{id}/ships", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "3" {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestDraw(t *testing.T) {
	base := stage(t)
	s := &Sample{Seed: "5", Sets: []*Set{{Name: "dock", From: "GET /docks", Pages: 2, Pick: 5, Query: map[string]any{"size": 10},
		Where: map[string]any{"name": "present"}, Check: []string{"GET /docks/{dockId}/ships"}, As: map[string]string{"dockId": "/id"}}}}
	p, err := Draw(context.Background(), s, discover.Options{BaseURL: base, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ss := p.Sets[0]
	if p.Seed != "5" || ss.Candidates != 4 || len(ss.Picked) != 2 || ss.LeftOut["without name"] != 1 || ss.LeftOut["GET /docks/{dockId}/ships failed"] != 1 {
		t.Errorf("drawn: %+v", ss)
	}
	s.Seed = "random"
	s.Sets[0].As = map[string]string{"dockId": "/id", "label": "/label"}
	s.Sets[0].Pages = 1
	p, err = Draw(context.Background(), s, discover.Options{BaseURL: base, Token: "t"})
	if err != nil || p.Seed == "random" || p.Sets[0].LeftOut["no label"] != 3 {
		t.Errorf("random seed and missing value: %v %+v", err, p)
	}
	if _, err := Draw(context.Background(), s, discover.Options{BaseURL: base, Token: "wrong"}); err == nil || !strings.Contains(err.Error(), "set dock") {
		t.Errorf("a failing list must fail: %v", err)
	}
	s.Seed = "x"
	if _, err := Draw(context.Background(), s, discover.Options{BaseURL: base}); err == nil {
		t.Error("an invalid seed must fail")
	}
}

func TestProposeAndPlan(t *testing.T) {
	sp, err := spec.Load(context.Background(), "../../../testdata/bookstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := Propose(sp, nil)
	if len(s.Sets) != 1 || s.Sets[0].Name != "author" || s.Sets[0].As["authorId"] != "/id" {
		t.Fatalf("proposed: %+v", s.Sets)
	}
	s.Exclude = []string{"GET /health"}
	s.Reads["GET /authors"] = map[string]any{"q": "{{author.authorId}}"}
	picked := &Picked{Sets: []scenario.SampleSet{{Name: "author", Picked: []map[string]any{{"authorId": json.Number("1")}, {"authorId": json.Number("2")}}}}}
	sc, notes := Plan(sp, s, picked)
	ids := map[string]bool{}
	for _, st := range sc.Steps {
		ids[st.ID] = true
	}
	if !ids["getAuthor:1"] || !ids["getAuthor:2"] || !ids["getStats"] || ids["getHealth"] {
		t.Errorf("steps: %v", ids)
	}
	if !ids["listAuthors:1"] {
		t.Errorf("reads with a set value run per record: %v", ids)
	}
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, "\n"), "no set gives {bookId}") {
		t.Errorf("notes: %v", notes)
	}
	// an empty set
	picked.Sets[0].Picked = nil
	_, notes = Plan(sp, s, picked)
	if !strings.Contains(strings.Join(notes, "\n"), "has no record") {
		t.Errorf("notes: %v", notes)
	}
	// keep sets of an existing file
	again := Propose(sp, s)
	if len(again.Sets) != 1 || again.Sets[0] != s.Sets[0] {
		t.Error("Propose must keep the existing sets")
	}
	if Exists(filepath.Join(t.TempDir(), "x")) {
		t.Error("Exists")
	}
}

func TestDiffReport(t *testing.T) {
	pre := &scenario.Snapshot{Created: "2026-10-13T10:00:00Z", SpecVersion: "1.4", Seed: "7", BaseURL: "http://dev",
		Sample: []scenario.SampleSet{{Name: "dock", Picked: []map[string]any{{"id": 1}}}},
		Steps:  []*scenario.Shot{{ID: "a", Request: "GET /a"}, {ID: "b", Request: "GET /b"}}}
	post := &scenario.Snapshot{Created: "2026-10-13T10:07:36Z", SpecVersion: "1.5", BaseURL: "http://dev"}
	r := &DiffReport{Pre: pre, Post: post, Findings: []scenario.Finding{
		{Code: scenario.FieldChanged, Step: "a", Request: "GET /a", Pointer: "/x", Old: `"a|b"`, New: strings.Repeat("y", 200), Fails: true},
		{Code: scenario.FieldAdded, Step: "a", Request: "GET /a", Pointer: "/y", Old: "", New: "1"},
	}, Ignored: []string{"updatedAt"}}
	md := r.Markdown()
	for _, want := range []string{"1.4 → 1.5", "7m36s apart", "**FAILED** – 1 findings fail, 1 for information", `\|`, "Not compared: updatedAt", "## Unchanged (1)", "GET /b"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown misses %q:\n%s", want, md)
		}
	}
	html, err := r.HTML()
	if err != nil || !strings.Contains(string(html), "FAILED") || !strings.Contains(string(html), "prefers-color-scheme") {
		t.Errorf("html: %v", err)
	}
	r.Findings = r.Findings[1:]
	if !strings.Contains(r.Markdown(), "**PASSED** – 1 findings for information") || r.Failed() {
		t.Error("info only passes")
	}
	r.Findings = nil
	post.Created, post.SpecVersion = "?", ""
	if md := r.Markdown(); !strings.Contains(md, "pre and post answer the same") || !strings.Contains(md, "→ ?") {
		t.Error(md)
	}
	if html, _ := r.HTML(); !strings.Contains(string(html), "pre and post answer the same") {
		t.Error("html without findings")
	}
}
