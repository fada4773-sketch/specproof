package tree

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/redact"
)

// Entry is one line of a timeline: a request of the fetch, a step of the
// test as it will run, or a step of a run, in the order it ran.
type Entry struct {
	// Step says what it is: a step of the test (docks1.create), or the
	// node a request of the fetch is for (ships under docks1).
	Step   string `json:"step"`
	Method string `json:"method,omitempty"`
	Target string `json:"target,omitempty"`
	// Status is the status of the answer, 0 for a step that has not run;
	// Expect the status the step expects.
	Status int   `json:"status,omitempty"`
	Expect int   `json:"expect,omitempty"`
	Ms     int64 `json:"ms,omitempty"`
	// Failed marks a request without answer or out of 2xx (fetch), a step
	// that did not pass (run).
	Failed bool `json:"failed,omitempty"`
	// Result says how it ended (the outcome and message of a step, the
	// error of a request) or, for the test, its decisions and gaps.
	Result     string `json:"result,omitempty"`
	Request    any    `json:"request,omitempty"`
	HasRequest bool   `json:"hasRequest,omitempty"`
	Answer     any    `json:"answer,omitempty"`
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
		e := Entry{Step: ex.For, Method: ex.Method, Target: red.String(ex.Target), Status: ex.Status, Ms: ex.Ms,
			Failed: ex.Error != "" || ex.Status/100 != 2, Result: red.String(ex.Error), HasRequest: ex.HasRequest}
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
func TestTimeline(built *Built) []Entry {
	if built == nil || built.Scenario == nil {
		return []Entry{}
	}
	notes := map[string][]string{}
	for _, n := range built.Notes {
		notes[n.Step] = append(notes[n.Step], n.What+": "+n.Why)
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
		what = append(what, notes[st.ID]...)
		e := Entry{Step: st.ID, Method: method, Target: target, Expect: st.Expect, Result: strings.Join(what, "; "), HasRequest: st.HasBody}
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
			b.WriteString("\n| # | For | Request | Status | Result |\n|---|---|---|---|---|\n")
			for i, e := range list {
				if e.Failed {
					fmt.Fprintf(&b, "| %d | %s | %s %s | %s | %s |\n", i+1, cell(e.Step), e.Method, cell(e.Target), statusText(e), cell(e.Result))
				}
			}
		}
	case ReviewScenario:
		fmt.Fprintf(&b, "\n<details><summary>Steps in the order they run (%d)</summary>\n\nBodies: [%s](%s#%s).\n\n", len(list), page, page, step)
		b.WriteString("| # | Step | Request | Expect | Checks, decisions and gaps |\n|---|---|---|---|---|\n")
		for i, e := range list {
			fmt.Fprintf(&b, "| %d | %s | %s %s | %d | %s |\n", i+1, cell(e.Step), e.Method, cell(e.Target), e.Expect, cell(e.Result))
		}
		b.WriteString("\n</details>\n")
	case ReviewUpdate:
		fmt.Fprintf(&b, "\n<details><summary>Examples written, in the order their steps ran (%d)</summary>\n\n", len(list))
		b.WriteString("| # | Step | Operation | Status | Example |\n|---|---|---|---|---|\n")
		for i, e := range list {
			fmt.Fprintf(&b, "| %d | %s | %s %s | %d | %s |\n", i+1, cell(e.Step), e.Method, cell(e.Target), e.Status, cell(e.Result))
		}
		b.WriteString("\n</details>\n")
	default:
		open := ""
		if failed > 0 {
			open = " open"
		}
		fmt.Fprintf(&b, "\n<details%s><summary>Steps in the order they ran (%d, %d did not pass)</summary>\n\nBodies: [%s](%s#%s).\n\n", open, len(list), failed, page, page, step)
		b.WriteString("| # | Step | Request | Status | ms | Result |\n|---|---|---|---|---|---|\n")
		for i, e := range list {
			fmt.Fprintf(&b, "| %d | %s | %s %s | %s | %d | %s |\n", i+1, cell(e.Step), e.Method, cell(e.Target), statusText(e), e.Ms, cell(e.Result))
		}
		b.WriteString("\n</details>\n")
	}
	return b.String()
}

// statusText is the status of a line: ✓ or ✗, with the expected status
// when it differs.
func statusText(e Entry) string {
	mark := "✓"
	if e.Failed {
		mark = "✗"
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
