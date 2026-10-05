package apitest

import (
	"context"
	"net/http"
)

// Hooks are optional extension points. A hook that returns an error or
// panics affects only the case or group it was called for.
type Hooks struct {
	// BeforeGroup runs before the cases of a resource group, e.g. to reset
	// database tables. An error skips the group.
	BeforeGroup func(ctx context.Context, group string) error
	// AfterGroup runs after the cases of a resource group.
	AfterGroup func(ctx context.Context, group string) error

	// BeforeRequest may modify the request before it is sent, e.g. to set an
	// idempotency key. An error marks the case as ERROR.
	BeforeRequest func(ctx context.Context, c *Case, req *http.Request) error

	// AfterResponse runs after all built-in checks passed and may perform
	// additional checks, e.g. directly in the database. An error marks the
	// case as FAILED.
	AfterResponse func(ctx context.Context, c *Case, resp *Response) error
}

// Case describes the case a hook is called for.
type Case struct {
	Name        string // "<Tag>/<operationId>/<example>"
	Group       string
	OperationID string // operationId or "<METHOD> <path>"
	Method      string
	Path        string // path template from the spec, e.g. "/planets/{code}"
	Example     string
}

// Response is the response passed to AfterResponse.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}
