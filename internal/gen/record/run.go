package record

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Input is what a run needs.
type Input struct {
	Spec *spec.Spec
	Doc  *yamldoc.Doc // the spec as a node tree; the examples go here
	File *File
	// Client sends the requests; nil when no instance is given. Then only
	// the stored answers are written, and a missing one is a problem.
	Client *Client
	// Refresh names entries whose answers are recorded again: "all", a
	// tag, an operationId or "METHOD /path".
	Refresh []string
	// IgnoreFields are fields no answer is compared on ("$apitest").
	IgnoreFields []string
}

// Run brings the record file and the spec together. It checks the file
// against the spec; sends, in the order of the file, every request up to
// the last one whose answer is missing, no longer fits the schema or is
// to be refreshed, and stores those answers in the file; then it writes
// every entry into the spec as examples. The instance must be empty: the
// requests before an answer to record are sent again to build its data.
func Run(ctx context.Context, in Input) (*Result, error) {
	res := &Result{}
	f := in.File
	f.bind(in.Spec, res)
	if len(res.Problems) > 0 {
		return res, nil
	}
	v := spec.NewValidator()
	needs := map[*Step]string{}
	last := -1
	for i, st := range f.Steps {
		why := ""
		switch {
		case refreshed(st, in.Refresh):
			why = "-refresh"
		case st.Response == nil:
			why = "no answer yet"
		default:
			if msg := responseFits(v, st); msg != "" {
				if in.Client == nil {
					res.note(CodeStale, st.where(), "the stored answer no longer fits the schema (%s); pass -base-url of an empty instance to record it again", msg)
					continue
				}
				why = "the schema changed: " + msg
			}
		}
		if why != "" {
			needs[st] = why
			last = i
		}
	}
	// the requests must fit the schema before anything is sent
	vars := map[string]any{}
	for _, st := range f.Steps {
		r, missing := st.request(vars)
		if len(missing) == 0 {
			if msg := requestFits(v, st, r); msg != "" {
				res.problem(CodeRequest, st.where(), "%s; fix the entry in the record file", msg)
			}
		}
		if st.Response != nil {
			st.saveFrom(st.Response, nil, r, vars)
		}
	}
	if len(res.Problems) > 0 {
		return res, nil
	}
	if last >= 0 && in.Client == nil {
		var list []string
		for _, st := range f.Steps {
			if needs[st] != "" {
				list = append(list, fmt.Sprintf("line %d %s (%s)", st.Line, st, needs[st]))
			}
		}
		res.problem(CodeNeedsURL, "record file", "%d entries need an answer from the instance; start an empty instance and pass it with -base-url:\n  %s",
			len(list), strings.Join(list, "\n  "))
		return res, nil
	}
	ok := true
	if last >= 0 {
		ok = send(ctx, in, res, needs, last)
	}
	for i, st := range f.Steps {
		if i > last {
			state := StateKept
			if needs[st] == "" && st.Response != nil {
				if msg := responseFits(v, st); msg != "" {
					state = StateStale
				}
			}
			res.Steps = append(res.Steps, StepResult{Step: st, State: state})
		}
	}
	if !ok {
		return res, nil
	}
	w := newWriter(in.Doc, res)
	w.write(f, v)
	res.SpecChanged = w.changed
	return res, nil
}

// refreshed reports whether -refresh names a step.
func refreshed(st *Step, refresh []string) bool {
	for _, r := range refresh {
		r = strings.TrimSpace(r)
		if r == "all" || r == st.Tag || r == st.Op.Group() || st.Op.Is(r) || r == st.Key || (st.Name != "" && r == st.Op.ID+"/"+st.Name) {
			return true
		}
		if f := strings.Fields(r); len(f) == 2 && st.Op.Is(strings.ToUpper(f[0])+" "+f[1]) {
			return true
		}
	}
	return false
}

// send sends the steps up to last in the order of the file. A step that
// needs an answer stores it; the others are compared with their stored
// answer. It stops at the first request the instance rejects and reports
// whether all were sent.
func send(ctx context.Context, in Input, res *Result, needs map[*Step]string, last int) bool {
	vars := map[string]any{}
	failed := false
	for _, st := range in.File.Steps[:last+1] {
		if failed {
			res.Steps = append(res.Steps, StepResult{Step: st, State: StateNotSent})
			continue
		}
		sr := StepResult{Step: st, Why: needs[st]}
		r, missing := st.request(vars)
		if len(missing) > 0 {
			res.problem(CodeSave, st.where(), "no value for {{%s}}: the entry that saves it was not answered", strings.Join(missing, "}}, {{"))
			sr.State, failed = StateFailed, true
			res.Steps = append(res.Steps, sr)
			continue
		}
		path, err := r.url(st.Op)
		if err != nil {
			res.problem(CodeParam, st.where(), "%v", err)
			sr.State, failed = StateFailed, true
			res.Steps = append(res.Steps, sr)
			continue
		}
		sr.URL = path
		mt := ""
		if m := requestMedia(st.Op); m != nil {
			mt = m.Type
		}
		ans, err := in.Client.send(ctx, st.Op.Method, path, mt, r.body, r.hasBody)
		res.Sent++
		if err != nil {
			res.problem(CodeFailed, st.where(), "%s %s: %v", st.Op.Method, path, err)
			sr.State, failed = StateFailed, true
			res.Steps = append(res.Steps, sr)
			continue
		}
		sr.Status = ans.Status
		got := &Response{Status: ans.Status}
		if len(bytes.TrimSpace(ans.Raw)) > 0 {
			if n, err := jsonNode(ans.Raw); err == nil {
				compact(n)
				got.Body = n
			} else if ans.Status/100 == 2 && needs[st] != "" {
				res.note(CodeSchema, st.where(), "the answer is not JSON; no body is stored")
			}
		}
		if st.Filter != nil && ans.Status/100 == 2 {
			if msg := st.filter(got, vars); msg != "" {
				res.problem(CodeFilter, st.where(), "%s", msg)
				sr.State, failed = StateFailed, true
				res.Steps = append(res.Steps, sr)
				continue
			}
			if got.Body != nil && len(listOf(got.Body).Content) == 0 {
				res.note(CodeFilter, st.where(), "no element of the list matches the filter; the example is an empty list")
			}
		}
		rejected := ans.Status/100 != 2 && (needs[st] != "" || st.Response == nil || st.Response.Status != ans.Status)
		if rejected {
			sr.State, sr.Sent, sr.Answer = StateFailed, r.body, clip(strings.TrimSpace(string(ans.Raw)))
			res.problem(CodeFailed, st.where(), "%s %s answered %d: %s\n  sent: %s",
				st.Op.Method, path, ans.Status, sr.Answer, clip(text(r.body)))
			failed = true
			res.Steps = append(res.Steps, sr)
			continue
		}
		got.Headers = savedHeaders(st, ans.Header)
		if needs[st] != "" {
			st.setResponse(got)
			res.Recorded++
			res.FileChanged = true
			sr.State = StateRecorded
		} else {
			sr.State = StateSent
			if d := differs(st, got, in.IgnoreFields); d != "" {
				sr.State = StateDiffers
				res.note(CodeDiffers, st.where(), "sent again, the instance answers otherwise than stored (%s); is the instance empty? A field that changes on every run belongs under \"ignore\"", d)
			}
		}
		if miss := st.saveFrom(got, ans.Header, r, vars); len(miss) > 0 {
			res.problem(CodeSave, st.where(), "no value at %s; fix \"save\"", strings.Join(miss, ", "))
			failed = true
		}
		res.Steps = append(res.Steps, sr)
	}
	return !failed
}

// request is a step's request with its values filled in.
type request struct {
	path, query map[string]any
	body        any
	hasBody     bool
	bodyNode    *yaml.Node
	pathNodes   map[string]*yaml.Node
	queryNodes  map[string]*yaml.Node
}

// request fills in the saved values; missing lists the ones vars lacks.
func (st *Step) request(vars map[string]any) (*request, []string) {
	r := &request{path: map[string]any{}, query: map[string]any{}, pathNodes: map[string]*yaml.Node{}, queryNodes: map[string]*yaml.Node{}}
	var missing []string
	for _, x := range []struct {
		n      *yaml.Node
		values map[string]any
		nodes  map[string]*yaml.Node
		in     string
	}{{st.Path, r.path, r.pathNodes, openapi3.ParameterInPath}, {st.Query, r.query, r.queryNodes, openapi3.ParameterInQuery}} {
		keys, m := mapping(x.n)
		for _, k := range keys {
			n, miss := fill(m[k], vars)
			missing = append(missing, miss...)
			v := decode(n)
			if p := param(st.Op, k, x.in); p != nil && p.Schema != nil && p.Schema.Value != nil {
				v = coerce(v, p.Schema.Value)
			}
			x.values[k], x.nodes[k] = v, n
		}
	}
	if st.Body != nil {
		n, miss := fill(st.Body, vars)
		missing = append(missing, miss...)
		r.body, r.hasBody, r.bodyNode = decode(n), true, n
	}
	return r, missing
}

// url is the path of the request with its parameters, serialized by the
// rules of the spec.
func (r *request) url(op *spec.Operation) (string, error) {
	path := op.Path
	var pairs []params.Pair
	for _, p := range op.Params {
		switch p.In {
		case openapi3.ParameterInPath:
			v, ok := r.path[p.Name]
			if !ok {
				return "", fmt.Errorf("the path parameter {%s} has no value", p.Name)
			}
			s, err := params.Path(p, v)
			if err != nil {
				return "", err
			}
			path = strings.ReplaceAll(path, "{"+p.Name+"}", s)
		case openapi3.ParameterInQuery:
			v, ok := r.query[p.Name]
			if !ok {
				continue
			}
			ps, err := params.Query(p, v)
			if err != nil {
				return "", err
			}
			pairs = append(pairs, ps...)
		}
	}
	if len(pairs) > 0 {
		path += "?" + params.EncodeQuery(pairs)
	}
	return path, nil
}

// saveFrom keeps the values a step saves, from its answer or from the
// request it sent; missing lists the sources without value. A stored
// answer (header nil) gives the headers it stored.
func (st *Step) saveFrom(r *Response, header http.Header, req *request, vars map[string]any) (missing []string) {
	body := decode(r.Body)
	for _, sv := range st.Save {
		v, ok := st.source(sv.From, body, r, header, req)
		if !ok {
			missing = append(missing, sv.From)
			continue
		}
		vars[sv.Name] = v
	}
	return missing
}

// source reads the value of a save source.
func (st *Step) source(from string, body any, r *Response, header http.Header, req *request) (any, bool) {
	if rest, ok := strings.CutPrefix(from, "request "); ok {
		if req == nil {
			return nil, false
		}
		if name, ok := strings.CutPrefix(rest, "path "); ok {
			v, found := req.path[name]
			return v, found
		}
		if name, ok := strings.CutPrefix(rest, "query "); ok {
			v, found := req.query[name]
			return v, found
		}
		if rest == "/" {
			return req.body, req.hasBody
		}
		return bind.Pointer(req.body, rest)
	}
	if name, ok := strings.CutPrefix(from, "header "); ok {
		h := r.Headers[http.CanonicalHeaderKey(name)]
		if header != nil {
			h = header.Get(name)
		}
		if h == "" {
			return nil, false
		}
		if strings.EqualFold(name, "Location") {
			return bind.LastSegment(h), true
		}
		return h, true
	}
	if from == "/" {
		return body, body != nil
	}
	return bind.Pointer(body, from)
}

// filter keeps the elements of the list in an answer that match the
// filter of the step; it returns why it cannot, or "".
func (st *Step) filter(r *Response, vars map[string]any) string {
	n, missing := fill(st.Filter, vars)
	if len(missing) > 0 {
		return fmt.Sprintf("filter: no value for {{%s}}", strings.Join(missing, "}}, {{"))
	}
	want, _ := decode(n).(map[string]any)
	out, ok := filterList(r.Body, want)
	if !ok {
		return "filter: the answer holds no list (an array, or an object with one array field)"
	}
	r.Body = out
	return ""
}

// savedHeaders are the headers of an answer a step saves values from;
// they are stored with the answer, so later runs read them too.
func savedHeaders(st *Step, h http.Header) map[string]string {
	var out map[string]string
	for _, sv := range st.Save {
		if name, ok := strings.CutPrefix(sv.From, "header "); ok && h.Get(name) != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[http.CanonicalHeaderKey(name)] = h.Get(name)
		}
	}
	return out
}

// differs describes how an answer differs from the stored one, the way
// apitest compares them; "" if it does not.
func differs(st *Step, got *Response, ignore []string) string {
	if got.Status != st.Response.Status {
		return fmt.Sprintf("status %d instead of %d", got.Status, st.Response.Status)
	}
	var schema *openapi3.Schema
	if m := responseMedia(st.Op, responseCode(st.Op, got.Status)); m != nil && m.Schema != nil {
		schema = m.Schema.Value
	}
	diffs := compare.Values(decode(st.Response.Body), decode(got.Body), compare.Options{Mode: compare.ModeSubset,
		Ignore: append(slices.Clone(st.Ignore), ignore...), Schema: schema, Unordered: st.Filter != nil})
	if len(diffs) == 0 {
		return ""
	}
	var parts []string
	for i, d := range diffs {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("%d more", len(diffs)-3))
			break
		}
		parts = append(parts, fmt.Sprintf("%s: stored %s, now %s", d.Pointer, d.Expected, d.Actual))
	}
	return strings.Join(parts, "; ")
}

// responseFits checks a stored answer against the schema of its response;
// it returns what violates it, or "".
func responseFits(v *spec.Validator, st *Step) string {
	m := responseMedia(st.Op, responseCode(st.Op, st.Response.Status))
	if m == nil || m.Schema == nil || m.Schema.Value == nil || st.Response.Body == nil {
		return ""
	}
	return firstViolation(v, m.Schema.Value, decode(st.Response.Body), spec.ModeResponse)
}

// requestFits checks the parameters and the body of a request against the
// schema; it returns what violates it, or "".
func requestFits(v *spec.Validator, st *Step, r *request) string {
	for _, in := range []string{openapi3.ParameterInPath, openapi3.ParameterInQuery} {
		values := r.path
		if in == openapi3.ParameterInQuery {
			values = r.query
		}
		for _, k := range sortedKeys(values) {
			p := param(st.Op, k, in)
			if p == nil || p.Schema == nil || p.Schema.Value == nil {
				continue
			}
			if msg := firstViolation(v, p.Schema.Value, values[k], spec.ModePlain); msg != "" {
				return fmt.Sprintf("%s parameter %q: %s", in, k, msg)
			}
		}
	}
	if m := requestMedia(st.Op); m != nil && r.hasBody && m.Schema != nil && m.Schema.Value != nil {
		if msg := firstViolation(v, m.Schema.Value, r.body, spec.ModeRequest); msg != "" {
			return "body " + msg
		}
	}
	return ""
}

// firstViolation validates a value; it returns the first violations as
// text, or "".
func firstViolation(v *spec.Validator, s *openapi3.Schema, value any, mode spec.Mode) string {
	errs := v.Validate(s, spec.Normalize(value), mode)
	if len(errs) == 0 {
		return ""
	}
	var parts []string
	for i, e := range errs {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("%d more", len(errs)-3))
			break
		}
		ptr := e.Pointer
		if ptr == "" {
			ptr = "/"
		}
		parts = append(parts, ptr+": "+e.Reason)
	}
	return strings.Join(parts, "; ")
}
