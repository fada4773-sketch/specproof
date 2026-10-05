// Package deviations loads and applies the file of accepted deviations
// between spec and API (FR-DEV).
package deviations

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oasdiff/yaml"
)

// WarnBefore is how long before expiry an entry is reported as expiring
// soon (FR-DEV-05).
const WarnBefore = 14 * 24 * time.Hour

// Entry is one accepted deviation.
type Entry struct {
	Index int // position in the file, from 1

	// Case is the case name; "*" matches within one name element.
	Case string
	// literal is Case with a leading number kept, for tags that really
	// start with digits and "_".
	literal string
	// Expected and Actual are status codes (e.g. 404 and 200).
	Expected, Actual string
	// Status is a result status such as EXAMPLE_MISMATCH, for deviations
	// that are not about the status code.
	Status string
	// Pointer limits Status to differences at exactly this JSON pointer.
	Pointer string

	Reason  string
	Ticket  string
	Expires time.Time
}

// numbered matches the position prefix of Config.NumberCases, e.g. "007_".
var numbered = regexp.MustCompile(`^\d+_`)

// NormalizeCase turns a case name copied from go test output or an IDE into
// the name apitest matches: without the test function ("TestAPI/"), without
// the position prefix of Config.NumberCases ("007_") and with spaces as "_",
// like Go names subtests.
func NormalizeCase(name string) string { return normalize(name, true) }

func normalize(name string, stripNumber bool) string {
	name = strings.Join(strings.Fields(strings.TrimSpace(name)), "_")
	elems := strings.Split(name, "/")
	if len(elems) == 4 && strings.HasPrefix(elems[0], "Test") {
		elems = elems[1:]
	}
	if stripNumber && len(elems) > 0 {
		elems[0] = numbered.ReplaceAllString(elems[0], "")
	}
	return strings.Join(elems, "/")
}

// names reports whether the entry's case pattern matches name.
func (e *Entry) names(name string) bool {
	if ok, _ := path.Match(e.Case, name); ok {
		return true
	}
	ok, _ := path.Match(e.literal, name)
	return ok
}

// Rule describes what the entry accepts, e.g. "404 → 200" or
// "EXAMPLE_MISMATCH at /price".
func (e *Entry) Rule() string {
	if e.Status == "" {
		return e.Expected + " → " + e.Actual
	}
	if e.Pointer != "" {
		return e.Status + " at " + e.Pointer
	}
	return e.Status
}

// Near returns the entries that name the case but did not match its result,
// to explain why a deviation did not apply.
func (s *Set) Near(name string) []*Entry {
	if s == nil {
		return nil
	}
	var out []*Entry
	for _, e := range s.Entries {
		if e.names(name) {
			out = append(out, e)
		}
	}
	return out
}

// Expired reports whether the entry has expired at now.
func (e *Entry) Expired(now time.Time) bool {
	return !now.Before(e.Expires.AddDate(0, 0, 1)) // valid through the whole day
}

// ExpiresSoon reports whether the entry expires within WarnBefore.
func (e *Entry) ExpiresSoon(now time.Time) bool {
	return !e.Expired(now) && now.Add(WarnBefore).After(e.Expires)
}

// Describe is a short description for messages.
func (e *Entry) Describe() string {
	s := e.Reason
	if e.Ticket != "" {
		s += " (" + e.Ticket + ")"
	}
	return fmt.Sprintf("%s, valid until %s", s, e.Expires.Format(time.DateOnly))
}

// Set is a loaded deviations file.
type Set struct {
	Path    string
	Entries []*Entry

	mu   sync.Mutex
	used map[*Entry]int
}

type rawEntry struct {
	Case     string `json:"case"`
	Expected any    `json:"expected"`
	Actual   any    `json:"actual"`
	Status   string `json:"status"`
	Pointer  string `json:"pointer"`
	Reason   string `json:"reason"`
	Ticket   any    `json:"ticket"`
	Expires  any    `json:"expires"`
}

// Load reads a deviations file (YAML or JSON). Invalid entries are rejected
// with their position (FR-DEV-02, NFR-08).
func Load(p string) (*Set, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("deviations file %s is not readable: %w", abs, err)
	}
	js, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("deviations file %s: %w", abs, err)
	}
	var list []rawEntry
	if string(js) != "null" {
		if err := json.Unmarshal(js, &list); err != nil {
			return nil, fmt.Errorf("deviations file %s: expected a list of entries: %w", abs, err)
		}
	}
	s := &Set{Path: abs, used: map[*Entry]int{}}
	var problems []string
	for i, r := range list {
		e, err := convert(i+1, r)
		if err != nil {
			problems = append(problems, fmt.Sprintf("entry %d: %v", i+1, err))
			continue
		}
		s.Entries = append(s.Entries, e)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("deviations file %s:\n  %s", abs, strings.Join(problems, "\n  "))
	}
	return s, nil
}

func convert(index int, r rawEntry) (*Entry, error) {
	e := &Entry{
		Index:    index,
		Case:     NormalizeCase(r.Case),
		literal:  normalize(r.Case, false),
		Expected: scalar(r.Expected),
		Actual:   scalar(r.Actual),
		Status:   strings.TrimSpace(r.Status),
		Pointer:  r.Pointer,
		Reason:   strings.TrimSpace(r.Reason),
		Ticket:   scalar(r.Ticket),
	}
	var missing []string
	if e.Case == "" {
		missing = append(missing, "case")
	}
	if e.Reason == "" {
		missing = append(missing, "reason")
	}
	exp := scalar(r.Expires)
	if exp == "" {
		missing = append(missing, "expires")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	// YAML turns an unquoted date into a timestamp, so both forms are fine.
	t, err := time.Parse(time.DateOnly, exp)
	if err != nil {
		ts, err2 := time.Parse(time.RFC3339, exp)
		if err2 != nil {
			return nil, fmt.Errorf("expires %q is not a date like 2026-12-31", exp)
		}
		t = time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
	}
	e.Expires = t
	byCode := e.Expected != "" || e.Actual != ""
	switch {
	case byCode && e.Status != "":
		return nil, fmt.Errorf("set either expected+actual or status, not both")
	case byCode && (e.Expected == "" || e.Actual == ""):
		return nil, fmt.Errorf("expected and actual must both be set")
	case !byCode && e.Status == "":
		return nil, fmt.Errorf("set expected+actual (status codes) or status (e.g. EXAMPLE_MISMATCH)")
	case e.Pointer != "" && e.Status == "":
		return nil, fmt.Errorf("pointer is only allowed together with status")
	}
	if _, err := path.Match(e.Case, ""); err != nil {
		return nil, fmt.Errorf("case %q is not a valid pattern: %w", e.Case, err)
	}
	return e, nil
}

func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// Outcome describes a case result for matching.
type Outcome struct {
	Case     string
	Status   string   // e.g. FAILED
	Expected string   // expected status, e.g. "404"
	Actual   string   // actual status, e.g. "200"
	Pointers []string // locations of the differences, for status entries
}

// Match returns the first entry that matches o exactly (FR-DEV-01), or nil.
func (s *Set) Match(o Outcome) *Entry {
	if s == nil {
		return nil
	}
	for _, e := range s.Entries {
		if !e.names(o.Case) {
			continue
		}
		if !e.matches(o) {
			continue
		}
		s.mu.Lock()
		s.used[e]++
		s.mu.Unlock()
		return e
	}
	return nil
}

func (e *Entry) matches(o Outcome) bool {
	if e.Status == "" {
		return o.Status == "FAILED" && o.Expected == e.Expected && o.Actual == e.Actual
	}
	if o.Status != e.Status {
		return false
	}
	if e.Pointer == "" {
		return true
	}
	if len(o.Pointers) == 0 {
		return false
	}
	for _, p := range o.Pointers {
		if p != e.Pointer {
			return false
		}
	}
	return true
}

// Uses returns how often e matched.
func (s *Set) Uses(e *Entry) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used[e]
}
