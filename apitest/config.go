package apitest

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/internal/compare"
)

// CompareMode selects how a response body is compared with the expected
// example (FR-CMP-02).
type CompareMode string

// Comparison modes.
const (
	// CompareSubset requires every field of the example with the same value;
	// additional fields are allowed. This is the default.
	CompareSubset CompareMode = "subset"
	// CompareExact requires an identical body apart from ignored fields.
	CompareExact CompareMode = "exact"
	// CompareSchema only validates the body against the schema.
	CompareSchema CompareMode = "schema"
)

// Names of the authentication cases, for Config.SkipAuthCases.
const (
	AuthUnauthorized = "unauthorized"  // without token, expects 401
	AuthInvalidToken = "invalid-token" // with a manipulated token, expects 401 or 403
	AuthForbidden    = "forbidden"     // with Config.ForbiddenToken, expects 403
)

// DefaultRequestTimeout is used if Config.RequestTimeout is zero.
const DefaultRequestTimeout = 10 * time.Second

// Config configures a run. Only SpecPath and either BaseURL or Handler are
// required; the zero value of every other field is a sensible default.
type Config struct {
	// SpecPath is the path of the OpenAPI file (YAML or JSON). Relative paths
	// are resolved against the test package directory, like any file access
	// in a Go test.
	SpecPath string
	// BaseURL of the running API, e.g. "http://127.0.0.1:8080/api/v3". It
	// replaces the servers entry of the spec and must contain its base path.
	BaseURL string
	// Handler serves the API in the same process. apitest starts it with
	// httptest.NewServer and requests go to its URL plus the servers' base
	// path. Set either Handler or BaseURL.
	Handler http.Handler

	// Token is required as soon as a selected operation uses securitySchemes.
	// It is used for every case except authentication cases.
	Token TokenSource
	// ForbiddenToken is a valid token with insufficient rights. It is used
	// for the "forbidden" case of operations marked with
	// x-apitest-forbidden: true (FR-CASE-11). Without it those cases are
	// skipped.
	ForbiddenToken TokenSource

	// Tags limits the run to these tags, in this order; empty means all.
	Tags []string
	// IncludeOps limits the run to these operations (operationId or
	// "<METHOD> <path>"); empty means all.
	IncludeOps []string
	// ExcludeOps leaves these operations out.
	ExcludeOps []string

	// Params sets fixed parameter values by parameter name. A key of the
	// form "<operationId>.<name>" applies to that operation only and takes
	// precedence over the plain name, e.g. for a generic "id".
	Params map[string]string
	// Headers are added to every request.
	Headers map[string]string

	// SkipAuthCases leaves these authentication cases out completely: they
	// are not run, not reported and do not appear as subtests, e.g.
	// AuthInvalidToken where no proxy validates tokens. Valid names:
	// AuthUnauthorized, AuthInvalidToken, AuthForbidden.
	SkipAuthCases []string
	// TamperToken derives the token of the invalid-token case from the real
	// one. nil means TruncateToken (the first 100 characters); TamperSignature
	// keeps the form and only breaks the signature.
	TamperToken func(token string) string

	// MethodOrder orders the regular cases of a group by method, e.g.
	// {"POST", "PUT", "GET"}. The default is POST, GET, PUT/PATCH; methods
	// that are not listed follow in that order. DELETE always runs last and
	// may only be the last entry. Bindings still win: a case runs after the
	// case it needs a value from.
	MethodOrder []string
	// DeleteLast runs the DELETE cases of all groups after every other case,
	// in reverse group order. By default they only wait for the groups that
	// depend on them through bindings, so a DELETE can remove data that a
	// later group uses through Params.
	DeleteLast bool

	// IgnoreFields are excluded from the value comparison at every level,
	// e.g. "id" or "createdAt". JSON pointers ("/items/*/id") work as well.
	IgnoreFields []string
	// CompareMode is the default comparison mode; empty means CompareSubset.
	CompareMode CompareMode
	// CaseInsensitive compares the response with the example, and the
	// stored record with the body sent, without regard to case: the string
	// "Nord" matches "nord" and the field "Name" matches "name"; an exact
	// field name wins. It applies to IgnoreFields too. The schema check
	// (enum, pattern, required fields) stays case-sensitive.
	CaseInsensitive bool

	// Strict makes NOT_BUILDABLE cases and expired deviations fail the test.
	// apitest does not detect CI environments; the project decides.
	Strict bool
	// DeviationsPath is the path of the file with accepted deviations
	// (FR-DEV). Relative paths are resolved like SpecPath.
	DeviationsPath string
	// RequestTimeout limits each request; zero means DefaultRequestTimeout.
	RequestTimeout time.Duration
	// HealthPath is requested before the first case and must answer 2xx.
	// Without it any HTTP response on "/" counts as ready.
	HealthPath string
	// AllowedHosts may receive writing requests in addition to localhost and
	// loopback addresses (NFR-07).
	AllowedHosts []string
	// AllowRemoteWrites permits writing requests to any host.
	AllowRemoteWrites bool
	// HTTPClient is used for all requests; nil means a default client.
	HTTPClient *http.Client

	// DisableReports writes no report files at all; results are only
	// reported through go test and the returned Result.
	DisableReports bool
	// ReportPath is where the Markdown report is written. The default is
	// "apitest-report/<test name>.md" in the test package directory.
	ReportPath string
	// ReportJSON additionally writes the results as JSON next to the report,
	// with the extension ".json" (FR-REP-07).
	ReportJSON bool
	// OmitBodies leaves request and response bodies out of the report.
	OmitBodies bool
	// ReportPassedDetails shows request, response, headers and curl command
	// for passed cases too, instead of one table row each.
	ReportPassedDetails bool
	// NumberCases prefixes the subtest names with their position in the run,
	// e.g. "007_Organization/createOrganization/default", so IDEs that sort
	// alphabetically show the execution order. Case names in deviations,
	// hooks and results stay without number.
	NumberCases bool
	// DisableWarnings drops all warnings and spec findings: they appear
	// neither in the go test output nor in the report. Errors are not
	// affected.
	DisableWarnings bool
	// Redact lists additional field names whose values are replaced by "***".
	Redact []string

	Hooks Hooks

	// now replaces the clock in tests (deviation expiry).
	now func() time.Time
}

// validate checks the configuration and applies defaults. Every message says
// what is wrong and how to fix it (NFR-08).
func (c *Config) validate() error {
	var errs []error
	if c.SpecPath == "" {
		errs = append(errs, errors.New("Config.SpecPath missing: set the path of the OpenAPI file"))
	}
	switch {
	case c.BaseURL == "" && c.Handler == nil:
		errs = append(errs, errors.New("neither Config.BaseURL nor Config.Handler set: set exactly one of them"))
	case c.BaseURL != "" && c.Handler != nil:
		errs = append(errs, errors.New("Config.BaseURL and Config.Handler are both set: set exactly one of them"))
	}
	if c.DisableReports && (c.ReportJSON || c.ReportPath != "") {
		errs = append(errs, errors.New("Config.DisableReports is set together with ReportPath or ReportJSON: disable reports or configure them, not both"))
	}
	for _, name := range c.SkipAuthCases {
		if name != AuthUnauthorized && name != AuthInvalidToken && name != AuthForbidden {
			errs = append(errs, fmt.Errorf("Config.SkipAuthCases: unknown case %q (valid: %q, %q, %q)", name, AuthUnauthorized, AuthInvalidToken, AuthForbidden))
		}
	}
	errs = append(errs, checkMethodOrder(c.MethodOrder)...)
	if c.CompareMode == "" {
		c.CompareMode = CompareSubset
	}
	if _, err := compare.ParseMode(string(c.CompareMode)); err != nil {
		errs = append(errs, fmt.Errorf("Config.CompareMode: %w", err))
	}
	if c.RequestTimeout < 0 {
		errs = append(errs, errors.New("Config.RequestTimeout must not be negative"))
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = DefaultRequestTimeout
	}
	return errors.Join(errs...)
}

// orderable are the methods MethodOrder may list besides a final DELETE.
var orderable = []string{http.MethodPost, http.MethodGet, http.MethodPut, http.MethodPatch}

func checkMethodOrder(order []string) []error {
	var errs []error
	seen := map[string]bool{}
	for i, m := range order {
		up := strings.ToUpper(strings.TrimSpace(m))
		switch {
		case seen[up]:
			errs = append(errs, fmt.Errorf("Config.MethodOrder lists %s twice", up))
		case up == http.MethodDelete && i != len(order)-1:
			errs = append(errs, errors.New("Config.MethodOrder: DELETE must be the last entry; a DELETE before other methods removes what they need (use DeleteLast to run all DELETEs at the end)"))
		case up != http.MethodDelete && !slices.Contains(orderable, up):
			errs = append(errs, fmt.Errorf("Config.MethodOrder: unknown method %q (valid: POST, GET, PUT, PATCH, DELETE last)", m))
		}
		seen[up] = true
	}
	return errs
}
