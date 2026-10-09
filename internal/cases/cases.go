// Package cases derives test cases from the examples of an OpenAPI spec
// (FR-CASE) and orders them within their resource group.
package cases

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// RankDelete is the rank of DELETE cases, which run last in a group.
const RankDelete = 7

// DefaultExample is the name of the case built without named examples.
const DefaultExample = "default"

// Untagged is the group of operations without tags.
const Untagged = spec.Untagged

// Kind distinguishes regular cases from negative and authentication tests.
type Kind int

// Case kinds.
const (
	Positive     Kind = iota
	Negative          // named request example matched to a 4xx response (FR-CASE-05)
	Unauthorized      // sent without token, expects 401 (FR-CASE-08)
	InvalidToken      // sent with a manipulated token, expects 401 or 403 (FR-CASE-10)
	Forbidden         // sent with Config.ForbiddenToken, expects 403 (FR-CASE-11)
	NotFound          // generated: an unknown key in the path, expects 404
	Conflict          // generated: a POST sent a second time, expects 409
	ServerError       // named request example matched to a 5xx response
)

// IsAuth reports whether k is one of the authentication kinds.
func (k Kind) IsAuth() bool { return k == Unauthorized || k == InvalidToken || k == Forbidden }

// Generated reports whether apitest builds cases of kind k itself instead
// of taking them from named examples of the spec.
func (k Kind) Generated() bool { return k.IsAuth() || k == NotFound || k == Conflict }

// String names the kind for reports.
func (k Kind) String() string {
	return [...]string{"regular", "negative", "unauthorized", "invalid-token", "forbidden", "not-found", "conflict", "server-error"}[k]
}

// ErrorCase reports whether c tests an error answer Config.TolerateErrorCases
// covers: a generated 404 or 409, a named example that expects 404 or 409,
// or one that expects 5xx.
func (c *Case) ErrorCase() bool {
	switch c.Kind {
	case NotFound, Conflict, ServerError:
		return true
	case Negative:
		return c.Expect.Status == http.StatusNotFound || c.Expect.Status == http.StatusConflict
	}
	return false
}

// Names of the authentication cases.
const (
	UnauthorizedExample = "unauthorized"
	InvalidTokenExample = "invalid-token"
	ForbiddenExample    = "forbidden"
)

// RankAuth is the rank of authentication cases of non-DELETE operations:
// after the 4xx examples, before the DELETEs.
const RankAuth = 6

// Expectation is what a case expects from the API (FR-CASE-04).
type Expectation struct {
	Code     string // response key in the spec: "201", "2XX" or "default"
	Status   int    // exact status; 0 if Code is a range or "default"
	Class    int    // status class for ranges: 2 for "2XX"
	Response *openapi3.Response
	// Example is the expected body, if the spec provides one.
	Example    any
	HasExample bool
	// ExampleWhere locates the example in the spec, for the report.
	ExampleWhere string
}

// Matches reports whether status fulfils the expectation.
func (e Expectation) Matches(status int) bool {
	if e.Status != 0 {
		return status == e.Status
	}
	return status/100 == e.Class
}

// String formats the expected status for messages.
func (e Expectation) String() string {
	if e.Status != 0 {
		return strconv.Itoa(e.Status)
	}
	return fmt.Sprintf("%dxx", e.Class)
}

// Case is one planned request with its expectation.
type Case struct {
	Name    string // "<Tag>/<operationId>/<example>" (FR-CASE-09)
	Group   string // resource group (first tag)
	Example string // example name or "default"
	Op      *spec.Operation
	Kind    Kind
	Rank    int // position class within the group
	// Order is x-apitest-order of the operation; it orders cases of the same
	// rank (FR-ORDER-06).
	Order int
	// ParamExample is the example name used to resolve parameters (rank 3),
	// if it differs from Example (authentication cases reuse the example of
	// the regular case).
	ParamExample string

	MediaType string // request media type, "" without body
	Body      any
	HasBody   bool

	Expect Expectation
	// Compare is the comparison mode from x-apitest-compare, "" if not set.
	Compare string
	// Ignore lists fields from x-apitest-ignore (operation and response).
	Ignore []string
	// Unordered is x-apitest-compare-unordered of the expected response
	// (FR-CMP-07).
	Unordered bool

	// NotFoundParam is the path parameter a NotFound case sends
	// NotFoundValue in, a key no record has.
	NotFoundParam string
	NotFoundValue any

	// Skip is set by x-apitest-skip (FR-CASE-07).
	Skip string
	// NotBuildable explains why the case cannot be sent (FR-CASE-06).
	NotBuildable string
}

// Options select operations.
type Options struct {
	Tags       []string // only these tags, in this order; empty = all
	IncludeOps []string // only these operations; empty = all
	ExcludeOps []string
	// ErrorCases adds the generated error cases: not-found (404) and
	// conflict (409), see errorCases.
	ErrorCases bool
}

// Build derives all cases for the selected operations, in execution order.
func Build(s *spec.Spec, opt Options) ([]*Case, error) {
	if err := checkSelection(s, opt); err != nil {
		return nil, err
	}
	var all []*Case
	for _, op := range s.Ops {
		if !selected(op, opt) {
			continue
		}
		all = append(all, forOperation(op, opt.ErrorCases)...)
	}
	seen := map[string]bool{}
	for _, c := range all {
		if seen[c.Name] {
			return nil, fmt.Errorf("duplicate case name %q (%s); tags, operationIds and example names must be unique after normalization", c.Name, c.Op.Where)
		}
		seen[c.Name] = true
	}
	order := groupOrder(all, opt.Tags)
	opIndex := map[*spec.Operation]int{}
	for i, op := range s.Ops {
		opIndex[op] = i
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if order[a.Group] != order[b.Group] {
			return order[a.Group] < order[b.Group]
		}
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		// Authentication cases for DELETE run right before the DELETEs
		// (FR-ORDER-07).
		if a.Kind.IsAuth() != b.Kind.IsAuth() {
			return a.Kind.IsAuth()
		}
		if opIndex[a.Op] != opIndex[b.Op] {
			return opIndex[a.Op] < opIndex[b.Op]
		}
		return a.Example < b.Example
	})
	return all, nil
}

// CheckOptions reports unknown tags or operations in opt (NFR-08).
func CheckOptions(s *spec.Spec, opt Options) error { return checkSelection(s, opt) }

// Selected reports whether op is selected by opt.
func Selected(op *spec.Operation, opt Options) bool { return selected(op, opt) }

func checkSelection(s *spec.Spec, opt Options) error {
	for _, id := range append(append([]string(nil), opt.IncludeOps...), opt.ExcludeOps...) {
		if s.Op(id) == nil {
			return fmt.Errorf("operation %q from IncludeOps/ExcludeOps does not exist in the spec (expected: operationId or \"METHOD /path\")", id)
		}
	}
	tags := map[string]bool{}
	for _, op := range s.Ops {
		tags[group(op)] = true
	}
	for _, t := range opt.Tags {
		if !tags[t] {
			return fmt.Errorf("tag %q from Config.Tags does not exist in the spec", t)
		}
	}
	return nil
}

func selected(op *spec.Operation, opt Options) bool {
	if len(opt.IncludeOps) > 0 && !slices.ContainsFunc(opt.IncludeOps, op.Is) {
		return false
	}
	if slices.ContainsFunc(opt.ExcludeOps, op.Is) {
		return false
	}
	if len(opt.Tags) > 0 && !slices.Contains(opt.Tags, group(op)) {
		return false
	}
	return true
}

func group(op *spec.Operation) string { return op.Group() }

// groupOrder returns the position of each group: Config.Tags order first,
// then alphabetically.
func groupOrder(all []*Case, tags []string) map[string]int {
	var names []string
	for _, c := range all {
		if !slices.Contains(names, c.Group) {
			names = append(names, c.Group)
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		ii, jj := slices.Index(tags, names[i]), slices.Index(tags, names[j])
		switch {
		case ii >= 0 && jj >= 0:
			return ii < jj
		case ii >= 0 || jj >= 0:
			return ii >= 0
		default:
			return names[i] < names[j]
		}
	})
	order := map[string]int{}
	for i, n := range names {
		order[n] = i
	}
	return order
}

func forOperation(op *spec.Operation, errorCases bool) []*Case {
	base := func(example string) *Case {
		c := &Case{
			Name:    Name(group(op), op.ID, example),
			Group:   group(op),
			Example: example,
			Op:      op,
			Compare: extString(op.Op.Extensions, "x-apitest-compare"),
			Ignore:  extStrings(op.Op.Extensions, "x-apitest-ignore"),
			Skip:    extString(op.Op.Extensions, "x-apitest-skip"),
			Order:   extInt(op.Op.Extensions, "x-apitest-order"),
		}
		return c
	}

	var out []*Case
	rb := requestBody(op)
	if rb != nil {
		mt, media := pickMedia(rb.Content)
		names := exampleNames(media)
		if len(names) == 0 {
			c := base(DefaultExample)
			c.MediaType = mt
			if media != nil {
				body, ok, missing := defaultBody(media)
				switch {
				case ok:
					c.Body, c.HasBody = body, true
				case rb.Required:
					c.NotBuildable = fmt.Sprintf("required body without example: no value for %s", missing)
				}
			}
			if !isSupportedMedia(mt) {
				c.NotBuildable = fmt.Sprintf("request media type %q is not supported", mt)
			}
			out = append(out, c)
		} else {
			for _, name := range names {
				c := base(name)
				c.MediaType = mt
				ex := media.Examples[name].Value
				c.Body, c.HasBody = spec.Normalize(ex.Value), true
				if skip := extString(ex.Extensions, "x-apitest-skip"); skip != "" {
					c.Skip = skip
				}
				if cmp := extString(ex.Extensions, "x-apitest-compare"); cmp != "" {
					c.Compare = cmp
				}
				if !isSupportedMedia(mt) {
					c.NotBuildable = fmt.Sprintf("request media type %q is not supported", mt)
				}
				out = append(out, c)
			}
		}
	} else {
		names := paramExampleNames(op)
		if len(names) == 0 {
			names = []string{DefaultExample}
		}
		for _, name := range names {
			out = append(out, base(name))
		}
	}

	for _, c := range out {
		expect(c)
		c.Rank = rank(c)
	}
	extra := authCases(op, out)
	if errorCases {
		extra = append(extra, generatedErrors(op, out)...)
	}
	return append(out, extra...)
}

// ParamSource returns the example name used for named parameter examples.
func (c *Case) ParamSource() string {
	if c.ParamExample != "" {
		return c.ParamExample
	}
	return c.Example
}

// secured reports whether op always requires authentication: it has
// security requirements and none of them is empty (optional auth).
func secured(op *spec.Operation) bool {
	if len(op.Security) == 0 {
		return false
	}
	for _, req := range op.Security {
		if len(req) == 0 {
			return false
		}
	}
	return true
}

// authCases derives the authentication cases of op from its first regular
// case, which provides body and parameters.
func authCases(op *spec.Operation, regular []*Case) []*Case {
	if !secured(op) || op.Op.Responses == nil {
		return nil
	}
	var tmpl *Case
	for _, c := range regular {
		if c.Kind == Positive {
			tmpl = c
			break
		}
	}
	if tmpl == nil {
		return nil
	}
	r401, r403 := op.Op.Responses.Value("401"), op.Op.Responses.Value("403")
	var out []*Case
	add := func(kind Kind, name, code string, ref *openapi3.ResponseRef) {
		c := &Case{
			Name:         Name(group(op), op.ID, name),
			Group:        group(op),
			Example:      name,
			ParamExample: tmpl.Example,
			Op:           op,
			Kind:         kind,
			Rank:         RankAuth,
			Order:        tmpl.Order,
			MediaType:    tmpl.MediaType,
			Body:         tmpl.Body,
			HasBody:      tmpl.HasBody,
			Skip:         tmpl.Skip,
			NotBuildable: tmpl.NotBuildable,
			Compare:      "schema", // error bodies are only checked against the schema
		}
		if op.Method == http.MethodDelete {
			c.Rank = RankDelete
		}
		c.Expect = expectation(code, ref.Value)
		out = append(out, c)
	}
	if r401 != nil {
		add(Unauthorized, UnauthorizedExample, "401", r401)
		add(InvalidToken, InvalidTokenExample, "401", r401)
	} else if r403 != nil {
		add(InvalidToken, InvalidTokenExample, "403", r403)
	}
	if forbiddenOptIn(op) && r403 != nil {
		add(Forbidden, ForbiddenExample, "403", r403)
	}
	return out
}

func forbiddenOptIn(op *spec.Operation) bool {
	v, _ := op.Op.Extensions["x-apitest-forbidden"].(bool)
	return v
}

// AuthFindings reports operations whose authentication cannot be tested
// because the spec documents neither 401 nor 403, or x-apitest-forbidden
// without 403 (FR-CASE-10, FR-CASE-11).
func AuthFindings(s *spec.Spec) []spec.Finding {
	var out []spec.Finding
	for _, op := range s.Ops {
		if !secured(op) || op.Op.Responses == nil {
			continue
		}
		has401, has403 := op.Op.Responses.Value("401") != nil, op.Op.Responses.Value("403") != nil
		if !has401 && !has403 {
			out = append(out, spec.Finding{
				Kind:    spec.FindingAuth,
				Where:   op.Where + ".responses",
				Message: "the operation requires a token but documents neither 401 nor 403; unauthorized and invalid-token cases are not generated",
			})
		}
		if forbiddenOptIn(op) && !has403 {
			out = append(out, spec.Finding{
				Kind:    spec.FindingAuth,
				Where:   op.Where + ".x-apitest-forbidden",
				Message: "x-apitest-forbidden is set but 403 is not documented; the forbidden case is not generated",
			})
		}
	}
	return out
}

func requestBody(op *spec.Operation) *openapi3.RequestBody {
	if op.Op.RequestBody == nil || op.Op.RequestBody.Value == nil || len(op.Op.RequestBody.Value.Content) == 0 {
		return nil
	}
	return op.Op.RequestBody.Value
}

// pickMedia chooses the request media type (FR-HTTP-05): application/json,
// then other JSON types, then form data, then the first alphabetically.
func pickMedia(content openapi3.Content) (string, *openapi3.MediaType) {
	keys := make([]string, 0, len(content))
	for k := range content {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi, pj := mediaPriority(keys[i]), mediaPriority(keys[j])
		if pi != pj {
			return pi < pj
		}
		return keys[i] < keys[j]
	})
	if len(keys) == 0 {
		return "", nil
	}
	return keys[0], content[keys[0]]
}

func mediaPriority(mt string) int {
	switch m := strings.ToLower(strings.TrimSpace(strings.Split(mt, ";")[0])); {
	case m == "application/json":
		return 0
	case spec.IsJSON(m):
		return 1
	case m == "application/x-www-form-urlencoded":
		return 2
	default:
		return 3
	}
}

func isSupportedMedia(mt string) bool {
	return mediaPriority(mt) <= 2
}

// PickMedia exposes the media type preference for responses.
func PickMedia(content openapi3.Content) (string, *openapi3.MediaType) { return pickMedia(content) }

func exampleNames(media *openapi3.MediaType) []string {
	if media == nil {
		return nil
	}
	var names []string
	for name, ex := range media.Examples {
		if ex != nil && ex.Value != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func paramExampleNames(op *spec.Operation) []string {
	var names []string
	for _, p := range op.Params {
		for name, ex := range p.Examples {
			if ex != nil && ex.Value != nil && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// expect sets the expectation of c according to FR-CASE-04/05.
func expect(c *Case) {
	responses := c.Op.Op.Responses
	if responses == nil || responses.Len() == 0 {
		c.setNotBuildable("no responses documented")
		return
	}
	codes := sortedCodes(responses.Map())

	// (1) a response example with the same name as the case.
	if c.Example != DefaultExample {
		for _, code := range codes {
			r := responses.Map()[code].Value
			if r == nil {
				continue
			}
			for _, mt := range sortedMedia(r.Content) {
				ex := r.Content[mt].Examples[c.Example]
				if ex == nil || ex.Value == nil {
					continue
				}
				c.Expect = expectation(code, r)
				c.Expect.Example, c.Expect.HasExample = spec.Normalize(ex.Value.Value), true
				c.Expect.ExampleWhere = fmt.Sprintf("%s.responses.%s.content[%s].examples.%s", c.Op.Where, code, mt, c.Example)
				c.addResponseExtensions(r, ex.Value)
				switch {
				case c.Expect.Status/100 == 4 || c.Expect.Class == 4:
					c.Kind = Negative
				case c.Expect.Status/100 == 5 || c.Expect.Class == 5:
					c.Kind = ServerError
				}
				return
			}
		}
	}

	// (2)/(3) the lowest documented 2xx response.
	for _, code := range codes {
		if !isSuccess(code) {
			continue
		}
		r := responses.Map()[code].Value
		c.Expect = expectation(code, r)
		if r != nil {
			if mt, media := pickMedia(r.Content); media != nil && media.Example != nil {
				c.Expect.Example, c.Expect.HasExample = spec.Normalize(media.Example), true
				c.Expect.ExampleWhere = fmt.Sprintf("%s.responses.%s.content[%s].example", c.Op.Where, code, mt)
			}
			c.addResponseExtensions(r, nil)
		}
		return
	}
	if r := responses.Default(); r != nil {
		c.Expect = Expectation{Code: "default", Class: 2, Response: r.Value}
		return
	}
	c.setNotBuildable("no 2xx response documented")
}

func (c *Case) setNotBuildable(reason string) {
	if c.NotBuildable == "" {
		c.NotBuildable = reason
	}
}

func (c *Case) addResponseExtensions(r *openapi3.Response, ex *openapi3.Example) {
	if cmp := extString(r.Extensions, "x-apitest-compare"); cmp != "" && c.Compare == "" {
		c.Compare = cmp
	}
	if ex != nil {
		if cmp := extString(ex.Extensions, "x-apitest-compare"); cmp != "" {
			c.Compare = cmp
		}
	}
	c.Ignore = append(c.Ignore, extStrings(r.Extensions, "x-apitest-ignore")...)
	if v, _ := r.Extensions["x-apitest-compare-unordered"].(bool); v {
		c.Unordered = true
	}
}

func expectation(code string, r *openapi3.Response) Expectation {
	e := Expectation{Code: code, Response: r}
	if n, err := strconv.Atoi(code); err == nil {
		e.Status = n
	} else if len(code) == 3 && strings.HasSuffix(strings.ToUpper(code), "XX") {
		e.Class = int(code[0] - '0')
	}
	return e
}

func isSuccess(code string) bool {
	return len(code) == 3 && code[0] == '2'
}

// sortedCodes orders response keys: exact codes ascending, then ranges, then "default".
func sortedCodes(m map[string]*openapi3.ResponseRef) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	weight := func(k string) int {
		switch {
		case k == "default":
			return 2
		case strings.HasSuffix(strings.ToUpper(k), "XX"):
			return 1
		default:
			return 0
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if weight(keys[i]) != weight(keys[j]) {
			return weight(keys[i]) < weight(keys[j])
		}
		return keys[i] < keys[j]
	})
	return keys
}

func sortedMedia(content openapi3.Content) []string {
	keys := make([]string, 0, len(content))
	for k := range content {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi, pj := mediaPriority(keys[i]), mediaPriority(keys[j])
		if pi != pj {
			return pi < pj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// rank implements the order within a group: create, read, list, update,
// 4xx examples, authentication cases, delete.
func rank(c *Case) int {
	if c.Kind == Negative || c.Kind == ServerError {
		return 5
	}
	switch c.Op.Method {
	case http.MethodPost:
		return 0
	case http.MethodGet:
		if strings.Contains(c.Op.Path, "{") {
			return 1
		}
		return 2
	case http.MethodPut, http.MethodPatch:
		return 3
	case http.MethodDelete:
		return RankDelete
	default:
		return 4
	}
}

// ApplyMethodOrder re-ranks the regular cases by method (Config.MethodOrder).
// Listed methods come first in the given order, the others keep their
// default order after them; negative, authentication and DELETE cases keep
// their ranks. Within one method the default order stays, so GET by key
// still runs before the list.
func ApplyMethodOrder(all []*Case, order []string) {
	if len(order) == 0 {
		return
	}
	pos := map[string]int{}
	for _, m := range order {
		if up := strings.ToUpper(strings.TrimSpace(m)); up != http.MethodDelete {
			pos[up] = len(pos)
		}
	}
	for _, c := range all {
		if c.Kind != Positive || c.Rank >= 5 {
			continue
		}
		if p, ok := pos[c.Op.Method]; ok {
			c.Rank = p
		} else {
			c.Rank = 4
		}
	}
}

func extString(ext map[string]any, key string) string {
	v, ok := ext[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func extInt(ext map[string]any, key string) int {
	switch v := ext[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// VerifyCase returns the default case of op, used to read a resource after
// writing it (FR-VERIFY-01).
func VerifyCase(op *spec.Operation) *Case {
	c := &Case{
		Name:    Name(group(op), op.ID, DefaultExample),
		Group:   group(op),
		Example: DefaultExample,
		Op:      op,
		Compare: extString(op.Op.Extensions, "x-apitest-compare"),
		Ignore:  extStrings(op.Op.Extensions, "x-apitest-ignore"),
	}
	expect(c)
	return c
}

func extStrings(ext map[string]any, key string) []string {
	v, ok := ext[key].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range v {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
