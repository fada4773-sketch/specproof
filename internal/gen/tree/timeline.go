package tree

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/redact"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Entry is one line of a timeline: a request of the fetch, a step of the
// test as it will run, or a step of a run, in the order it ran.
type Entry struct {
	// Step says what it is: a step of the test (docks1.create), or the
	// node a request of the fetch is for (ships under docks1).
	Step string `json:"step"`
	// OperationID is the operation of the request or step.
	OperationID string `json:"op,omitempty"`
	Method      string `json:"method,omitempty"`
	Target      string `json:"target,omitempty"`
	// Status is the status of the answer, 0 for a step that has not run;
	// Expect the status the step expects.
	Status int   `json:"status,omitempty"`
	Expect int   `json:"expect,omitempty"`
	Ms     int64 `json:"ms,omitempty"`
	// Failed marks a request without answer or out of 2xx (fetch), a step
	// that did not pass (run).
	Failed bool `json:"failed,omitempty"`
	// NoAnswer marks a request that was sent but got no (whole) answer:
	// the connection failed or closed, a timeout, the API crashed. NotSent
	// marks a step of a run that was not sent: skipped (a step before it
	// failed) or not buildable. Result says why.
	NoAnswer bool `json:"noAnswer,omitempty"`
	NotSent  bool `json:"notSent,omitempty"`
	// Result says how it ended (the outcome and message of a step, the
	// error of a request) or, for the test, its decisions and gaps.
	Result     string `json:"result,omitempty"`
	Request    any    `json:"request,omitempty"`
	HasRequest bool   `json:"hasRequest,omitempty"`
	Answer     any    `json:"answer,omitempty"`
	// Outcome is the status of a step of a run that did not pass
	// (DATA_MISMATCH, SCHEMA_VIOLATION, …); Problems its schema violations,
	// Diffs the values that differ, Equals the step whose body the answer
	// must hold. The page lists them together (button "Mismatches").
	Outcome  string      `json:"outcome,omitempty"`
	Problems []string    `json:"problems,omitempty"`
	Diffs    []EntryDiff `json:"diffs,omitempty"`
	Equals   string      `json:"equals,omitempty"`
	// Notes are the decisions and gaps of a step of the test with their
	// level (error, warn, info); Result holds them too, as text.
	Notes []EntryNote `json:"notes,omitempty"`
}

// EntryNote is a note of a step with its level.
type EntryNote struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// noteText is a note as one line, its level in front: "error: x = 3 is a
// key of the stage: …".
func noteText(n Note) string {
	level := n.Level
	if level == "" {
		level = LevelInfo
	}
	return level + ": " + n.What + ": " + n.Why
}

// EntryDiff is a value of an answer that differs from the expected one.
type EntryDiff struct {
	Pointer  string `json:"pointer"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Note     string `json:"note,omitempty"`
}

// SetTimeline sets the timeline of a step of the review: its lines in the
// order they ran; nil removes it (a step that sent nothing).
func (d *ReviewDoc) SetTimeline(step string, list []Entry) {
	if list == nil {
		delete(d.timelines, step)
		return
	}
	d.timelines[step] = list
}

// SetTrace sets the fetch as a tree; the page shows it above the requests
// of the fetch.
func (d *ReviewDoc) SetTrace(list []*Visit) { d.trace = list }

// Trace is the fetch as a tree, nil without.
func (d *ReviewDoc) Trace() []*Visit { return d.trace }

// Timeline is the timeline of a step, nil without.
func (d *ReviewDoc) Timeline(step string) []Entry { return d.timelines[step] }

// FetchTimeline is the timeline of a fetch: every request as it was sent.
func FetchTimeline(log []Exchange, red *redact.Redactor) []Entry {
	if red == nil {
		red = redact.New(nil)
	}
	out := make([]Entry, 0, len(log))
	for _, ex := range log {
		e := Entry{Step: ex.For, OperationID: ex.OperationID, Method: ex.Method, Target: red.String(ex.Target), Status: ex.Status, Ms: ex.Ms,
			Failed: ex.Error != "" || ex.Status/100 != 2, NoAnswer: ex.Error != "", Result: red.String(ex.Error), HasRequest: ex.HasRequest}
		if ex.HasRequest {
			e.Request = red.Value(ex.Request)
		}
		if ex.Answer != nil {
			e.Answer = red.Value(ex.Answer)
		}
		out = append(out, e)
	}
	return out
}

// TestTimeline is the timeline of the test as built: its steps in the
// order they will run, with the decisions and gaps of each.
func TestTimeline(s *spec.Spec, built *Built) []Entry {
	if built == nil || built.Scenario == nil {
		return []Entry{}
	}
	notes := map[string][]Note{}
	for _, n := range built.Notes {
		notes[n.Step] = append(notes[n.Step], n)
	}
	out := make([]Entry, 0, len(built.Scenario.Steps))
	for _, st := range built.Scenario.Steps {
		method, target, ok := strings.Cut(st.Call, " ")
		if !ok {
			method, target = "", st.Call
		}
		var what []string
		if st.Equals != "" {
			what = append(what, "equals "+st.Equals)
		}
		if st.Token != "" {
			what = append(what, "token "+st.Token)
		}
		var marked []EntryNote
		for _, n := range sortedNotes(notes[st.ID]) {
			what = append(what, noteText(n))
			marked = append(marked, EntryNote{Level: n.Level, Text: n.What + ": " + n.Why})
		}
		e := Entry{Step: st.ID, Method: method, Target: target, Expect: st.Expect, Result: strings.Join(what, "; "), HasRequest: st.HasBody, Notes: marked}
		if s != nil {
			if op := lookupOp(s, st.Call); op != nil {
				e.OperationID = op.ID
			}
		}
		if st.HasBody {
			e.Request = st.Body
		}
		out = append(out, e)
	}
	return out
}

// timelineMD is the timeline of a step in the Markdown review: one line per
// request or step, no bodies (tree-review_gen.html has them). A fetch lists
// only the requests that failed.
func timelineMD(step string, list []Entry, page string) string {
	if list == nil {
		return ""
	}
	var b strings.Builder
	failed := 0
	for _, e := range list {
		if e.Failed {
			failed++
		}
	}
	switch step {
	case ReviewFetch:
		fmt.Fprintf(&b, "\n### Requests in the order sent (%d, %d failed)\n\nBodies of every request: [%s](%s#%s).\n", len(list), failed, page, page, step)
		if failed > 0 {
			b.WriteString("\n| # | For | Operation | Request | Status | Result |\n|---|---|---|---|---|---|\n")
			for i, e := range list {
				if e.Failed {
					fmt.Fprintf(&b, "| %d | %s | %s | %s %s | %s | %s |\n", i+1, cell(e.Step), opText(e), e.Method, cell(e.Target), statusText(e), cell(e.Result))
				}
			}
		}
	case ReviewScenario:
		fmt.Fprintf(&b, "\n<details><summary>Steps in the order they run (%d)</summary>\n\nBodies: [%s](%s#%s).\n\n", len(list), page, page, step)
		b.WriteString("| # | Step | Operation | Request | Expect | Checks, decisions and gaps |\n|---|---|---|---|---|---|\n")
		for i, e := range list {
			fmt.Fprintf(&b, "| %d | %s | %s | %s %s | %d | %s |\n", i+1, cell(e.Step), opText(e), e.Method, cell(e.Target), e.Expect, cell(e.Result))
		}
		b.WriteString("\n</details>\n")
	case ReviewUpdate:
		fmt.Fprintf(&b, "\n<details><summary>Examples written, in the order their steps ran (%d)</summary>\n\n", len(list))
		b.WriteString("| # | Step | Operation | Request | Status | Example |\n|---|---|---|---|---|---|\n")
		for i, e := range list {
			fmt.Fprintf(&b, "| %d | %s | %s | %s %s | %d | %s |\n", i+1, cell(e.Step), opText(e), e.Method, cell(e.Target), e.Status, cell(e.Result))
		}
		b.WriteString("\n</details>\n")
	default:
		open := ""
		if failed > 0 {
			open = " open"
		}
		fmt.Fprintf(&b, "\n<details%s><summary>Steps in the order they ran (%d, %d did not pass)</summary>\n\nBodies: [%s](%s#%s).\n\n", open, len(list), failed, page, page, step)
		b.WriteString("| # | Step | Operation | Request | Status | ms | Result |\n|---|---|---|---|---|---|---|\n")
		for i, e := range list {
			fmt.Fprintf(&b, "| %d | %s | %s | %s %s | %s | %d | %s |\n", i+1, cell(e.Step), opText(e), e.Method, cell(e.Target), statusText(e), e.Ms, cell(e.Result))
		}
		b.WriteString("\n</details>\n")
	}
	return b.String()
}

// opText is the operationId of a line, "–" without.
func opText(e Entry) string {
	if e.OperationID == "" {
		return "–"
	}
	return cell(e.OperationID)
}

// statusText is the status of a line: ✓ or ✗, with the expected status
// when it differs; "no answer ✗" for a request the API did not answer,
// "not sent" for a step that was skipped.
func statusText(e Entry) string {
	mark := "✓"
	if e.Failed {
		mark = "✗"
	}
	switch {
	case e.NoAnswer && e.Status != 0:
		return fmt.Sprintf("%d, answer broke off %s", e.Status, mark)
	case e.NoAnswer:
		return "no answer " + mark
	case e.NotSent:
		return "not sent"
	}
	if e.Status == 0 {
		return mark
	}
	s := fmt.Sprintf("%d %s", e.Status, mark)
	if e.Expect != 0 && e.Expect != e.Status {
		s += fmt.Sprintf(" (expected %d)", e.Expect)
	}
	return s
}

// reviewData is what tree-review_gen.html holds and reads back: the
// timelines of the steps, so a later run keeps those it does not write.
type reviewData struct {
	Spec      string             `json:"spec,omitempty"`
	File      string             `json:"file,omitempty"`
	Timelines map[string][]Entry `json:"timelines"`
	Trace     []*Visit           `json:"trace,omitempty"`
}

// encodeData is the data of the review as JSON for the page; json escapes
// <, > and &, so the text cannot end the script element.
func (d *ReviewDoc) encodeData() string {
	b, err := json.Marshal(reviewData{Spec: d.Spec, File: d.File, Timelines: d.timelines, Trace: d.trace})
	if err != nil {
		return `{"timelines":{}}`
	}
	return string(b)
}

// sortedNotes are notes with the errors first, then the warnings, then the
// others, each in the order they came.
func sortedNotes(list []Note) []Note {
	rank := func(n Note) int {
		switch n.Level {
		case LevelError:
			return 0
		case LevelWarn:
			return 1
		}
		return 2
	}
	out := slices.Clone(list)
	slices.SortStableFunc(out, func(a, b Note) int { return rank(a) - rank(b) })
	return out
}
