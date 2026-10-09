package apitest

import "time"

// Status is the result of a case.
type Status string

// Case statuses.
const (
	StatusPassed          Status = "PASSED"           // all checks passed
	StatusFailed          Status = "FAILED"           // unexpected status code
	StatusSchemaViolation Status = "SCHEMA_VIOLATION" // response violates the schema
	StatusExampleMismatch Status = "EXAMPLE_MISMATCH" // response differs from the expected example
	StatusDataMismatch    Status = "DATA_MISMATCH"    // written data was not stored as sent
	StatusError           Status = "ERROR"            // network error, timeout, panic in a hook
	StatusDeviation       Status = "DEVIATION"        // deviation allowed by the deviations file
	StatusTolerated       Status = "TOLERATED"        // error case with another answer, Config.TolerateErrorCases
	StatusNotBuildable    Status = "NOT_BUILDABLE"    // no value for a required parameter or body
	StatusSkipped         Status = "SKIPPED"          // skipped on purpose or because of a failed dependency
)

// fails reports whether the status fails the test.
func (s Status) fails(strict bool) bool {
	switch s {
	case StatusFailed, StatusSchemaViolation, StatusExampleMismatch, StatusDataMismatch, StatusError:
		return true
	case StatusNotBuildable:
		return strict
	}
	return false
}

// CaseResult is the outcome of one case.
type CaseResult struct {
	Name      string // "<Tag>/<operationId>/<example>"
	Number    int    // position in the run with Config.NumberCases, else 0
	Group     string
	Operation string // operationId or "<METHOD> <path>"
	Example   string
	Status    Status
	// Message is a one-line summary. It never contains tokens.
	Message string
	// StatusCode is the HTTP status of the response, 0 if none was received.
	StatusCode int
	Duration   time.Duration
	// Precondition is set if the case only ran because a selected case
	// needed one of its values (FR-GO-02).
	Precondition bool
	// Tolerated is the status a TOLERATED case would have had.
	Tolerated Status
}

// Summary aggregates a run.
type Summary struct {
	Counts            map[Status]int
	Total             int
	Duration          time.Duration
	Operations        int // selected operations
	OperationsCovered int // operations with at least one executed case
	NotSelected       int // cases filtered out by "go test -run"
}

// Result is returned by Run.
type Result struct {
	Cases   []CaseResult
	Summary Summary
	// Report is the absolute path of the written report, "" if none was written.
	Report string
	// Failed reports whether the run failed the test.
	Failed bool
}
