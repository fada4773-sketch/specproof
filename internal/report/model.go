// Package report renders the reports (FR-REP): Markdown for pull requests
// and CI, and a self-contained HTML dashboard; both are written atomically.
package report

import "time"

// Status values, identical to apitest.Status.
const (
	Passed          = "PASSED"
	Failed          = "FAILED"
	SchemaViolation = "SCHEMA_VIOLATION"
	ExampleMismatch = "EXAMPLE_MISMATCH"
	DataMismatch    = "DATA_MISMATCH"
	Error           = "ERROR"
	Deviation       = "DEVIATION"
	Tolerated       = "TOLERATED"
	NotBuildable    = "NOT_BUILDABLE"
	Skipped         = "SKIPPED"
)

// StatusOrder is the order of statuses in the summary table.
var StatusOrder = []string{Passed, Failed, SchemaViolation, ExampleMismatch, DataMismatch, Error, Deviation, Tolerated, NotBuildable, Skipped}

// IsError reports whether a status belongs to the "Errors" section.
func IsError(status string) bool {
	switch status {
	case Failed, SchemaViolation, ExampleMismatch, DataMismatch, Error:
		return true
	}
	return false
}

// Report is everything the report shows. All strings must already be redacted.
type Report struct {
	SpecTitle   string
	SpecVersion string // info.version
	OpenAPI     string // openapi field
	SpecFile    string
	BaseURL     string
	Started     time.Time
	Duration    time.Duration
	GoVersion   string
	Version     string // library version
	Strict      bool
	Passed      bool // overall result
	Aborted     string
	Warnings    []string
	Findings    []Finding
	Coverage    Coverage
	Cases       []Case
	NotSelected int // cases filtered out by "go test -run"
	// Deviations lists the entries of the deviations file (FR-DEV-03/04/05).
	Deviations []DeviationEntry
	// DeviationsFile is the path of the deviations file, "" if none.
	DeviationsFile string
	// PassedDetails renders passed cases with their details instead of
	// one table row each.
	PassedDetails bool
	// Tolerate is Config.TolerateErrorCases.
	Tolerate bool
}

// DeviationEntry is one entry of the deviations file and how it was used.
type DeviationEntry struct {
	Index   int
	Case    string
	Rule    string // e.g. "404 → 200" or "EXAMPLE_MISMATCH at /name"
	Reason  string
	Ticket  string
	Expires string
	Uses    int
	Expired bool
	Soon    bool // expires within 14 days
}

// Finding is a spec finding.
type Finding struct {
	Where   string
	Message string
}

// Coverage summarizes which operations and examples were executed.
type Coverage struct {
	Operations        int
	OperationsCovered int
	Examples          int
	ExamplesCovered   int
	Uncovered         []Uncovered
}

// Uncovered is an operation without an executed case.
type Uncovered struct {
	Operation string
	Reason    string
}

// Case is the result of one case.
type Case struct {
	Name      string
	Number    string // position in the run, e.g. "007"; "" without numbering
	Group     string
	Operation string // operationId or "METHOD /path"
	Status    string
	// Kind is the kind of case: "regular", "negative", "not-found",
	// "conflict", "server-error", "unauthorized", …
	Kind string
	// ErrorCase marks a case that tests a documented error (404, 409, 5xx).
	ErrorCase bool
	// Tolerated is the status a TOLERATED case would have had.
	Tolerated string
	// Code is the HTTP status received, 0 without answer.
	Code     int
	Duration time.Duration
	Message  string // one line
	Method   string
	Target   string // path and query
	Expected string // e.g. "201"
	Actual   string // e.g. "200"
	Diffs    []Diff
	Problems []string // schema errors, one per line
	Request  *Body
	Response *Body
	// ResponseHeaders lists the response headers, also when the body is
	// empty, e.g. to see whether a 401 came from the API or a proxy.
	ResponseHeaders *Body
	// Verify is the response of the GET that checked a write (FR-VERIFY).
	Verify *Body
	Curl   string
	// Precondition marks a case that only ran because a selected case needed
	// its binding (FR-GO-02).
	Precondition bool
}

// Diff is one value difference.
type Diff struct {
	Pointer, Expected, Actual, Note string
}

// Body is a request or response body prepared for display.
type Body struct {
	Title    string // e.g. "Response (201)"
	Lang     string // code block language, e.g. "json"
	Text     string
	Original int // original size in bytes if Text was truncated, else 0
}
