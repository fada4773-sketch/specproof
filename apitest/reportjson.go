package apitest

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/internal/report"
)

// jsonReport is the machine-readable report (FR-REP-07).
type jsonReport struct {
	Spec       string     `json:"spec"`
	BaseURL    string     `json:"baseUrl"`
	Started    time.Time  `json:"started"`
	DurationMs int64      `json:"durationMs"`
	Strict     bool       `json:"strict"`
	Passed     bool       `json:"passed"`
	Aborted    string     `json:"aborted,omitempty"`
	Counts     any        `json:"counts"`
	Cases      []jsonCase `json:"cases"`
}

type jsonCase struct {
	Name         string `json:"name"`
	Number       int    `json:"number,omitempty"` // with Config.NumberCases
	Group        string `json:"group"`
	Operation    string `json:"operation"`
	Example      string `json:"example"`
	Status       Status `json:"status"`
	Message      string `json:"message,omitempty"`
	Method       string `json:"method,omitempty"`
	Target       string `json:"target,omitempty"`
	StatusCode   int    `json:"statusCode,omitempty"`
	DurationMs   int64  `json:"durationMs"`
	Precondition bool   `json:"precondition,omitempty"`
	Deviation    int    `json:"deviation,omitempty"` // entry number in the deviations file
}

// writeJSON writes the results next to the Markdown report, with ".json"
// instead of ".md".
func (r *runner) writeJSON() error {
	counts := map[Status]int{}
	out := jsonReport{
		BaseURL:    r.red.String(r.base),
		Started:    r.start.UTC(),
		DurationMs: timeSince(r.start).Milliseconds(),
		Strict:     r.cfg.Strict,
		Passed:     !r.failed,
		Aborted:    r.aborted,
		Cases:      []jsonCase{},
	}
	if r.spec != nil {
		out.Spec = r.spec.Path
	}
	for _, o := range r.results {
		counts[o.status]++
		jc := jsonCase{
			Name: o.c.Name, Number: r.numbers[o.c], Group: o.c.Group, Operation: o.c.Op.ID, Example: o.c.Example,
			Status: o.status, Message: o.message, StatusCode: o.code,
			DurationMs: o.duration.Milliseconds(), Precondition: o.precondition,
		}
		if p := o.prepared; p != nil {
			jc.Method, jc.Target = p.Method, r.red.String(p.Target)
		}
		if o.deviation != nil {
			jc.Deviation = o.deviation.Index
		}
		out.Cases = append(out.Cases, jc)
	}
	out.Counts = counts
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON report: %w", err)
	}
	return report.WriteFile(strings.TrimSuffix(r.reportPath, ".md")+".json", append(b, '\n'))
}
