package record

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Codes of the notes and problems.
const (
	CodeUnknownOp   = "UNKNOWN_OPERATION"  // an entry names no operation of the spec
	CodeDuplicate   = "DUPLICATE"          // two entries of one endpoint with the same name
	CodeParam       = "PARAMETER"          // a parameter the endpoint does not have, or one without value
	CodeBody        = "BODY"               // a body missing, or one the endpoint does not take
	CodePlaceholder = "PLACEHOLDER"        // "{{name}}" no earlier entry saves
	CodeRequest     = "REQUEST_INVALID"    // the request of an entry violates the schema
	CodeNeedsURL    = "NEEDS_INSTANCE"     // answers to record, but no -base-url
	CodeFailed      = "REQUEST_FAILED"     // the instance rejected a request
	CodeSave        = "SAVE_MISSING"       // the answer or the request lacks a value to save
	CodeFilter      = "FILTER"             // a filter without list, or one no element matches
	CodeStatus      = "STATUS"             // the answer has a status the spec does not document
	CodeShared      = "SHARED"             // one place of the spec needs two examples
	CodeSchema      = "RESPONSE_SCHEMA"    // a recorded answer violates the schema
	CodeStale       = "RESPONSE_STALE"     // a stored answer no longer fits the schema
	CodeApproved    = "APPROVED_NO_ANSWER" // an approved entry without answer
	CodeOrder       = "ORDER"              // apitest runs an entry in another order
	CodeNotRun      = "NOT_RUN"            // apitest does not run the case of an entry
	CodeNotInFile   = "NOT_IN_FILE"        // apitest runs a case the file has no entry for
	CodeSkipped     = "SKIPPED"            // -analyse leaves out a case it cannot build
)

// Note is one finding of a run.
type Note struct{ Code, Where, Message string }

func (n Note) String() string { return fmt.Sprintf("%s %s: %s", n.Code, n.Where, n.Message) }

// States of a step after a run.
const (
	StateKept     = "kept"     // not sent; the stored answer is used
	StateRecorded = "recorded" // sent, its answer is stored now, approved
	StateIgnored  = "ignored"  // status ignore: not sent, not written
	StateFailed   = "failed"   // the instance rejected it
	StateStale    = "stale"    // the stored answer no longer fits the schema
	StateNotSent  = "not sent" // after a failed request
)

// StepResult is what a run did with one step.
type StepResult struct {
	Step   *Step
	State  string
	Status int    // the status the instance answered; 0 if not sent
	URL    string // the path sent, with its values
	Why    string // why it was sent: status new or repeat, -refresh
	Sent   any    // the body sent, for a failed request
	Answer string // the answer, for a failed request
}

// Result is what a run did.
type Result struct {
	Notes    []Note
	Problems []Note
	Steps    []StepResult
	// Recorded is the number of answers stored by this run.
	Recorded int
	// Sent is the number of requests sent.
	Sent int
	// Examples is the number of examples written into the spec that differ
	// from what it held.
	Examples    int
	FileChanged bool
	SpecChanged bool
}

func (r *Result) note(code, where, format string, args ...any) {
	r.Notes = append(r.Notes, Note{code, where, fmt.Sprintf(format, args...)})
}

func (r *Result) problem(code, where, format string, args ...any) {
	r.Problems = append(r.Problems, Note{code, where, fmt.Sprintf(format, args...)})
}

// where locates a step for the report: "line 12 POST /docks".
func (st *Step) where() string { return fmt.Sprintf("line %d %s", st.Line, st) }

// lookup finds the operation of an entry key: "METHOD /path" in any case
// of the method, or an operationId.
func lookup(s *spec.Spec, key string) *spec.Operation {
	if op := s.Op(key); op != nil {
		return op
	}
	if f := strings.Fields(key); len(f) == 2 {
		return s.Op(strings.ToUpper(f[0]) + " " + f[1])
	}
	return nil
}

// bind finds the operation of every step and checks the file against the
// spec: endpoints, names, parameters, bodies and the saved values each
// entry uses. Problems go into res.
func (f *File) bind(s *spec.Spec, res *Result) {
	byOp := map[string][]*Step{}
	saved := map[string]bool{}
	for _, st := range f.Steps {
		st.Op = lookup(s, st.Key)
		if st.Op == nil {
			res.problem(CodeUnknownOp, st.where(), "the spec has no operation %q; write \"METHOD /path\" as the spec has it, or the operationId", st.Key)
			continue
		}
		byOp[st.Op.ID] = append(byOp[st.Op.ID], st)
		checkParams(st, res)
		checkBody(st, res)
		for _, n := range st.uses() {
			if !saved[n] {
				res.problem(CodePlaceholder, st.where(), "{{%s}}: no entry above saves %q; add it to the \"save\" of the entry that creates it", n, n)
			}
		}
		for _, sv := range st.Save {
			saved[sv.Name] = true
		}
	}
	for _, id := range sortedKeys(byOp) {
		sts := byOp[id]
		names := map[string]bool{}
		for _, st := range sts {
			if len(sts) > 1 && st.Name == "" {
				res.problem(CodeDuplicate, st.where(), "%s appears %d times; give every entry of it its own \"name\"", method(st.Op), len(sts))
				continue
			}
			if names[st.Name] {
				res.problem(CodeDuplicate, st.where(), "the name %q is used twice for %s", st.Name, method(st.Op))
			}
			names[st.Name] = true
			if st.Name != "" && !st.hasPlace() {
				res.problem(CodeDuplicate, st.where(), "a named example needs a request body or a path or query parameter to live at; %s has none, so apitest runs it only once", method(st.Op))
			}
		}
	}
}

// hasPlace reports whether the step can hold a named example: apitest
// takes the example names of a case from the request body, or from the
// parameters of an endpoint without body.
func (st *Step) hasPlace() bool {
	if requestMedia(st.Op) != nil {
		return st.Body != nil
	}
	return (st.Path != nil && len(st.Path.Content) > 0) || (st.Query != nil && len(st.Query.Content) > 0)
}

// uses are the saved values the request of a step uses.
func (st *Step) uses() []string {
	var out []string
	for _, n := range []*yaml.Node{st.Path, st.Query, st.Body, st.Filter} {
		for _, name := range names(n) {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

func checkParams(st *Step, res *Result) {
	for in, n := range map[string]*yaml.Node{openapi3.ParameterInPath: st.Path, openapi3.ParameterInQuery: st.Query} {
		keys, _ := mapping(n)
		for _, k := range keys {
			if param(st.Op, k, in) == nil {
				res.problem(CodeParam, st.where(), "%s has no %s parameter %q (it has: %s)", method(st.Op), in, k, strings.Join(paramNames(st.Op, in), ", "))
			}
		}
	}
	_, path := mapping(st.Path)
	_, query := mapping(st.Query)
	for _, p := range st.Op.Params {
		switch {
		case p.In == openapi3.ParameterInPath && path[p.Name] == nil:
			res.problem(CodeParam, st.where(), "the path parameter {%s} has no value; set it under \"path\"", p.Name)
		case p.In == openapi3.ParameterInQuery && p.Required && query[p.Name] == nil:
			res.problem(CodeParam, st.where(), "the required query parameter %q has no value; set it under \"query\"", p.Name)
		}
	}
}

func checkBody(st *Step, res *Result) {
	rb := st.Op.Op.RequestBody
	switch {
	case st.Body != nil && (rb == nil || rb.Value == nil):
		res.problem(CodeBody, st.where(), "%s takes no request body; remove \"body\"", method(st.Op))
	case st.Body != nil && requestMedia(st.Op) == nil:
		res.problem(CodeBody, st.where(), "%s takes no JSON body; record sends JSON only", method(st.Op))
	case st.Body == nil && rb != nil && rb.Value != nil && rb.Value.Required:
		res.problem(CodeBody, st.where(), "%s requires a request body; set \"body\"", method(st.Op))
	}
}

// param finds a path or query parameter of an operation.
func param(op *spec.Operation, name, in string) *openapi3.Parameter {
	for _, p := range op.Params {
		if p.Name == name && p.In == in {
			return p
		}
	}
	return nil
}

func paramNames(op *spec.Operation, in string) []string {
	var out []string
	for _, p := range op.Params {
		if p.In == in {
			out = append(out, p.Name)
		}
	}
	if len(out) == 0 {
		return []string{"none"}
	}
	return out
}

// requestMedia returns the JSON media type of the request body apitest
// sends, and its schema; nil if there is none.
func requestMedia(op *spec.Operation) *media {
	rb := op.Op.RequestBody
	if rb == nil || rb.Value == nil || len(rb.Value.Content) == 0 {
		return nil
	}
	mt, m := cases.PickMedia(rb.Value.Content)
	if m == nil || !spec.IsJSON(mt) {
		return nil
	}
	return &media{Type: mt, Schema: m.Schema}
}

// media is a JSON media type and its schema.
type media struct {
	Type   string
	Schema *openapi3.SchemaRef
}

// responseCode is the response of the spec a status falls under: the
// status itself, its range ("2XX") or "default"; "" if none.
func responseCode(op *spec.Operation, status int) string {
	if op.Op.Responses == nil {
		return ""
	}
	m := op.Op.Responses.Map()
	code := fmt.Sprint(status)
	if m[code] != nil {
		return code
	}
	for _, r := range []string{code[:1] + "XX", code[:1] + "xx"} {
		if m[r] != nil {
			return r
		}
	}
	if m["default"] != nil {
		return "default"
	}
	return ""
}

// lowestSuccess is the response apitest expects for a default example: the
// lowest documented 2xx.
func lowestSuccess(op *spec.Operation) string {
	if op.Op.Responses == nil {
		return ""
	}
	var codes []string
	for code := range op.Op.Responses.Map() {
		if len(code) == 3 && code[0] == '2' {
			codes = append(codes, code)
		}
	}
	slices.SortFunc(codes, func(a, b string) int {
		ra, rb := strings.ContainsAny(a, "Xx"), strings.ContainsAny(b, "Xx")
		if ra != rb {
			if ra {
				return 1
			}
			return -1
		}
		return strings.Compare(a, b)
	})
	if len(codes) == 0 {
		return ""
	}
	return codes[0]
}

// responseMedia returns the JSON media type of a response and its schema.
func responseMedia(op *spec.Operation, code string) *media {
	if code == "" || op.Op.Responses == nil {
		return nil
	}
	r := op.Op.Responses.Map()[code]
	if r == nil || r.Value == nil {
		return nil
	}
	mt, m := cases.PickMedia(r.Value.Content)
	if m == nil || !spec.IsJSON(mt) {
		return nil
	}
	return &media{Type: mt, Schema: m.Schema}
}

// method names a request for messages: "POST /docks".
func method(op *spec.Operation) string { return op.Method + " " + op.Path }

// sortedKeys returns the keys of a map in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
