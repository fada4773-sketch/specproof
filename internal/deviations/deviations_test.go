package deviations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dev.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const sample = `
- case: Planet/getPlanets/default
  expected: 404
  actual: 200
  reason: "empty list returns [] instead of 404"
  ticket: API-123
  expires: 2026-12-31
- case: Planet/*/unauthorized
  expected: 401
  actual: 403
  reason: "gateway answers 403"
  expires: 2026-10-05
- case: Book/createBook/valid
  status: EXAMPLE_MISMATCH
  pointer: /title
  reason: "server trims the title"
  expires: 2026-09-01
`

func TestTPL1_17_ExactMatchOnly(t *testing.T) {
	s, err := Load(write(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		o    Outcome
		want int // entry index, 0 = no match
	}{
		{"exact", Outcome{Case: "Planet/getPlanets/default", Status: "FAILED", Expected: "404", Actual: "200"}, 1},
		{"only case matches", Outcome{Case: "Planet/getPlanets/default", Status: "FAILED", Expected: "404", Actual: "500"}, 0},
		{"only status matches", Outcome{Case: "Planet/listAll/default", Status: "FAILED", Expected: "404", Actual: "200"}, 0},
		{"wildcard within one element", Outcome{Case: "Planet/deletePlanet/unauthorized", Status: "FAILED", Expected: "401", Actual: "403"}, 2},
		{"wildcard does not cross elements", Outcome{Case: "Planet/a/b/unauthorized", Status: "FAILED", Expected: "401", Actual: "403"}, 0},
		{"status entry", Outcome{Case: "Book/createBook/valid", Status: "EXAMPLE_MISMATCH", Pointers: []string{"/title"}}, 3},
		{"status entry, other pointer too", Outcome{Case: "Book/createBook/valid", Status: "EXAMPLE_MISMATCH", Pointers: []string{"/title", "/price"}}, 0},
		{"status entry, other status", Outcome{Case: "Book/createBook/valid", Status: "SCHEMA_VIOLATION", Pointers: []string{"/title"}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := s.Match(tc.o)
			got := 0
			if e != nil {
				got = e.Index
			}
			if got != tc.want {
				t.Errorf("got entry %d, want %d", got, tc.want)
			}
		})
	}
	if s.Uses(s.Entries[0]) != 1 {
		t.Error("uses of entry 1 must be counted")
	}
}

func TestTPL1_18_InvalidEntries(t *testing.T) {
	tests := map[string]string{
		"missing reason":     "- { case: a, expected: 404, actual: 200, expires: 2026-12-31 }",
		"missing expires":    "- { case: a, expected: 404, actual: 200, reason: r }",
		"no kind":            "- { case: a, reason: r, expires: 2026-12-31 }",
		"both kinds":         "- { case: a, expected: 404, actual: 200, status: ERROR, reason: r, expires: 2026-12-31 }",
		"half status code":   "- { case: a, expected: 404, reason: r, expires: 2026-12-31 }",
		"bad date":           "- { case: a, status: ERROR, reason: r, expires: 31.12.2026 }",
		"pointer w/o status": "- { case: a, expected: 1, actual: 2, pointer: /x, reason: r, expires: 2026-12-31 }",
		"not a list":         "case: a",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, content))
			if err == nil {
				t.Fatal("want load error")
			}
			if name != "not a list" && !strings.Contains(err.Error(), "entry 1") {
				t.Errorf("error must name the entry: %v", err)
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Errorf("missing file: %v", err)
	}
}

func TestTPL1_19_ExpiryAndUnused(t *testing.T) {
	s, err := Load(write(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	e1, e2, e3 := s.Entries[0], s.Entries[1], s.Entries[2]
	if e1.Expired(now) || e1.ExpiresSoon(now) {
		t.Error("entry 1 is valid for months")
	}
	if !e3.Expired(now) {
		t.Error("entry 3 expired on 2026-09-01")
	}
	if e2.Expired(time.Date(2026, 10, 5, 23, 0, 0, 0, time.UTC)) {
		t.Error("an entry is valid through its expiry day")
	}
	if s.Uses(e2) != 0 {
		t.Error("unused entry must have 0 uses")
	}
}

func TestTPL1_28_ExpiresSoon(t *testing.T) {
	s, err := Load(write(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC) // entry 2 expires in 10 days
	if !s.Entries[1].ExpiresSoon(now) {
		t.Error("entry 2 must be reported as expiring soon")
	}
	if !strings.Contains(s.Entries[0].Describe(), "API-123") {
		t.Errorf("describe: %s", s.Entries[0].Describe())
	}
}

func TestEmptyFile(t *testing.T) {
	s, err := Load(write(t, ""))
	if err != nil || len(s.Entries) != 0 {
		t.Errorf("empty file: %v %v", s, err)
	}
	var nilSet *Set
	if nilSet.Match(Outcome{}) != nil {
		t.Error("nil set matches nothing")
	}
}

func TestNormalizeCase(t *testing.T) {
	for in, want := range map[string]string{
		"Author/createAuthor/valid":             "Author/createAuthor/valid",
		"  Author/createAuthor/valid ":          "Author/createAuthor/valid",
		"007_Author/createAuthor/valid":         "Author/createAuthor/valid",
		"TestAPI/007_Author/createAuthor/valid": "Author/createAuthor/valid",
		"TestAPI/Author/createAuthor/valid":     "Author/createAuthor/valid",
		"Space Station/getPilot/default":        "Space_Station/getPilot/default",
		"Author/*/unauthorized":                 "Author/*/unauthorized",
		"2024_Archive/listArchive/default":      "Archive/listArchive/default",
	} {
		if got := NormalizeCase(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestTagStartingWithDigits(t *testing.T) {
	s := &Set{used: map[*Entry]int{}}
	e, err := convert(1, rawEntry{Case: "2024_Archive/listArchive/default", Status: "SCHEMA_VIOLATION", Reason: "r", Expires: "2099-12-31"})
	if err != nil {
		t.Fatal(err)
	}
	s.Entries = []*Entry{e}
	if s.Match(Outcome{Case: "2024_Archive/listArchive/default", Status: "SCHEMA_VIOLATION"}) == nil {
		t.Error("a tag starting with digits must still match literally")
	}
}
