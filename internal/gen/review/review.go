// Package review evaluates what apitest would report about a spec, the
// spec findings, cases it could not send and values the generator could
// not create, and proposes a fix for each: a defaults.json entry where one
// can solve it, otherwise what to do. The proposals are added to
// defaults.json for the user to review; "apply" takes them into the spec.
package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/exec"
	"github.com/fada4773-sketch/specproof/internal/gen/apply"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/scenario"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Actions: what solves a finding.
const (
	ActionDefault = "DEFAULT" // a defaults.json entry, proposed with its value
	ActionChoose  = "CHOOSE"  // a defaults.json entry whose value only the user knows
	ActionApply   = "APPLY"   // "apitest-gen apply" fixes it
	ActionSpec    = "SPEC"    // the spec has to be changed by hand
	ActionEdit    = "EDIT"    // an entry of the existing defaults is wrong
)

// Suggestion is the proposed fix for one finding.
type Suggestion struct {
	Action  string
	Finding string // the finding kind, e.g. "heuristic" or "NOT_BUILDABLE"
	Where   string // location in the spec or the dictionary
	Message string // the finding as apitest reports it
	Key     string // defaults key (DEFAULT, CHOOSE)
	Value   any    // proposed value (DEFAULT) or the current one (CHOOSE)
	Fix     string // what to do and why
}

// Result of a review.
type Result struct {
	Suggestions []Suggestion
}

// Count returns the number of suggestions with the action.
func (r *Result) Count(action string) int {
	n := 0
	for _, s := range r.Suggestions {
		if s.Action == action {
			n++
		}
	}
	return n
}

// Input of a review.
type Input struct {
	Spec       *spec.Spec
	Dict       *dict.Dict
	DictNotes  []dict.Note // from dict.Build
	Defaults   *defaults.Defaults
	GenericIDs []string
	// Apply is the result of "apply" run in memory on the same input; its
	// problems are reviewed too.
	Apply *apply.Result
	// Model is the resource model; path parameters that hold a record key
	// get their example from the records, so they need no default.
	Model *model.Model
	// PathOrder are the paths in the order of the spec file; "$snapshot"
	// lists the resources in this order. Without it they follow by name.
	PathOrder []string
}

type reviewer struct {
	in    Input
	res   *Result
	known map[string]bool // lower-case keys of the defaults with a value
	todo  map[string]bool // lower-case keys of the defaults with null
	seen  map[string]bool
	// chosen are parameters ("query.zone") that already have a CHOOSE
	// entry from a case that cannot be sent
	chosen map[string]bool
	set    *bind.Set // the bindings in the spec, nil if they are invalid
}

// Run reviews a spec.
func Run(in Input) *Result {
	r := &reviewer{in: in, res: &Result{}, known: map[string]bool{}, todo: map[string]bool{}, seen: map[string]bool{}, chosen: map[string]bool{}}
	for _, k := range in.Defaults.Keys() {
		if in.Defaults.IsTodo(k) {
			r.todo[strings.ToLower(k)] = true
			continue
		}
		r.known[strings.ToLower(k)] = true
	}
	for _, k := range in.Defaults.Rejected {
		r.known[strings.ToLower(k)] = true // decided: not proposed again
	}
	r.specFindings()
	set, err := bind.Resolve(in.Spec)
	if err != nil {
		r.add(Suggestion{Action: ActionSpec, Finding: spec.FindingBinding, Where: "bindings", Message: err.Error(),
			Fix: "an x-apitest-bind in the spec is invalid; apitest stops before the first request. Fix or remove it in the spec"})
		set = nil
	} else {
		r.set = set
		r.bindings(set)
	}
	r.auth()
	if set != nil {
		r.notBuildable(set)
		r.genericIDs(set)
	}
	r.dictNotes()
	r.applyProblems()
	r.snapshot()
	return r.res
}

// modeled reports whether a path parameter of op holds a record key: the
// records give it its example.
func (r *reviewer) modeled(op *spec.Operation, name string) bool {
	o := r.in.Model.Op(op)
	return o != nil && o.Param(name) != nil
}

// snapshot proposes "$snapshot" with the request each resource is fetched
// from, if the defaults have none yet. Parameters with a known value (a key
// of a record of the last run, a default, a record field of the same name)
// are filled in, the others stay placeholders; "$comment" shows the path
// template, so the filled values can be checked.
func (r *reviewer) snapshot() {
	if r.in.Model == nil || len(r.in.Defaults.Snapshot) > 0 {
		return
	}
	type entry struct {
		res *model.Resource
		o   *model.Op
		pos int
	}
	var entries []entry
	for _, res := range r.in.Model.Resources {
		o := scenario.AutoSource(res)
		if o == nil {
			o = r.anyList(res)
		}
		if o == nil {
			continue
		}
		pos := slices.Index(r.in.PathOrder, o.Op.Path)
		if pos < 0 {
			pos = len(r.in.PathOrder)
		}
		entries = append(entries, entry{res, o, pos})
	}
	// in the order of the spec: the snapshot runs them in the order of the
	// defaults, so a "seed" can fill the requests below it
	slices.SortStableFunc(entries, func(a, b entry) int { return a.pos - b.pos })
	var v defaults.Ordered
	var names, open []string
	for _, e := range entries {
		res, o := e.res, e.o
		from := map[string]string{}
		target, unknown := scenario.Fill(o, r.value(o, from))
		comment := scenario.Template(o)
		if o.Op.HasOperationID {
			comment = o.Op.ID + ": " + comment
		}
		var filled []string
		for _, p := range o.Op.Params {
			if why, ok := from[p.Name]; ok {
				filled = append(filled, p.Name+" = "+why)
			}
		}
		if len(filled) > 0 {
			comment += "; filled: " + strings.Join(filled, ", ")
		}
		var missing []string
		for _, name := range unknown {
			if mp := o.Param(name); mp != nil {
				// a key of the parent: the snapshot takes it from its record
				comment += fmt.Sprintf("; {%s} is the %s of the first %s", name, mp.Field, mp.Resource.Name)
				continue
			}
			missing = append(missing, name)
		}
		if len(missing) > 0 {
			comment += "; replace {" + strings.Join(missing, "}, {") + "} with values that exist in the instance"
			open = append(open, res.Name)
		}
		v = append(v, defaults.Pair{Key: res.Name, Value: defaults.Ordered{
			{Key: "from", Value: target},
			{Key: "count", Value: 1},
			{Key: "seed", Value: []string{}},
			{Key: "validation", Value: defaults.Ordered{
				{Key: "mandatoryFields", Value: []string{}},
				{Key: "equalFields", Value: map[string]any{}},
				{Key: "followingDetails", Value: []string{}},
			}},
			{Key: "$comment", Value: comment},
		}})
		names = append(names, res.Name)
	}
	if len(v) == 0 {
		return
	}
	fix := "with apitest-gen -base-url <instance> the examples get the data of the running instance; raise \"count\" to show more elements in the list examples"
	if len(open) > 0 {
		fix = "set the placeholders in \"from\" of " + strings.Join(open, ", ") + "; " + fix
	}
	r.add(Suggestion{Action: ActionDefault, Finding: "SNAPSHOT_SOURCE", Where: "model", Key: defaults.SnapshotKey, Value: v,
		Message: fmt.Sprintf("the records of %s are fetched with these requests with -base-url", strings.Join(names, ", ")),
		Fix:     fix})
}

// anyList is the list of res with the fewest parameters and no key of res
// itself in its path, for a source whose parameters the user fills in.
func (r *reviewer) anyList(res *model.Resource) *model.Op {
	var best *model.Op
	for _, o := range res.OpsWith(model.RoleList) {
		own := false
		for _, p := range o.Params {
			own = own || p.Resource == res
		}
		if !own && (best == nil || len(o.Op.Params) < len(best.Op.Params)) {
			best = o
		}
	}
	return best
}

// value returns what review knows about the parameters of o: the key of the
// first record of the last run, a default, or a record field with the
// parameter's name (bookCode → Book.Code).
// Where a value comes from is noted in from, for the comment.
func (r *reviewer) value(o *model.Op, from map[string]string) func(*openapi3.Parameter) any {
	return func(p *openapi3.Parameter) any {
		if mp := o.Param(p.Name); mp != nil && p.In == openapi3.ParameterInPath {
			v := r.record(mp.Resource)[mp.Field]
			if v != nil {
				from[p.Name] = fmt.Sprintf("%s of the first %s", mp.Field, mp.Resource.Name)
			}
			return v
		}
		if o.Op.HasOperationID {
			if e := r.in.Defaults.Scoped(o.Op.ID, p.Name); e != nil {
				from[p.Name] = fmt.Sprintf("default %q", e.Key)
				return e.Value
			}
		}
		if e := r.in.Defaults.Plain(p.Name); e != nil {
			from[p.Name] = fmt.Sprintf("default %q", e.Key)
			return e.Value
		}
		for _, res := range r.in.Model.Resources {
			rec := r.record(res)
			for _, f := range res.FieldNames() {
				if v, ok := rec[f]; ok && v != nil && (strings.EqualFold(f, p.Name) || strings.EqualFold(res.Name+f, p.Name)) {
					from[p.Name] = fmt.Sprintf("%s of the first %s", f, res.Name)
					return v
				}
			}
		}
		return nil
	}
}

// record is the first record of res from the dictionary, nil before the
// first run.
func (r *reviewer) record(res *model.Resource) map[string]any {
	if r.in.Dict == nil {
		return nil
	}
	for name, recs := range r.in.Dict.Records {
		if strings.EqualFold(name, res.Name) && len(recs) > 0 {
			return recs[0]
		}
	}
	return nil
}

// add records a suggestion once. Keys the defaults already have, or that
// were rejected, are decided and dropped; a key with null is still open
// and only listed as a value to choose.
func (r *reviewer) add(s Suggestion) {
	k := strings.ToLower(s.Key)
	if s.Key != "" && (r.known[k] || (r.todo[k] && s.Action != ActionChoose)) {
		return
	}
	id := s.Action + "|" + s.Key + "|" + s.Where + "|" + s.Message
	if s.Key != "" {
		id = s.Action + "|" + s.Key
	}
	if r.seen[id] {
		return
	}
	r.seen[id] = true
	r.res.Suggestions = append(r.res.Suggestions, s)
}

func (r *reviewer) specFindings() {
	for _, f := range r.in.Spec.Findings {
		switch {
		case f.Kind == spec.FindingExampleSchema && strings.Contains(f.Where, ".examples."):
			r.add(Suggestion{Action: ActionSpec, Finding: f.Kind, Where: f.Where, Message: f.Message,
				Fix: "a curated named example violates its schema; apitest-gen never changes named examples. Fix the value in the spec, or remove the example"})
		case f.Kind == spec.FindingExampleSchema:
			r.add(Suggestion{Action: ActionApply, Finding: f.Kind, Where: f.Where, Message: f.Message,
				Fix: "run apitest-gen apply: it replaces examples that violate their schema"})
		case f.Kind == spec.FindingValidation && f.Where == "swagger":
			// information only: the conversion needs no fix
		case f.Kind == spec.FindingValidation:
			r.add(Suggestion{Action: ActionSpec, Finding: f.Kind, Where: f.Where, Message: f.Message,
				Fix: "rename one of the paths or merge the operations; apitest tests both, but a server can only route one"})
		}
	}
	if scheme, ok := r.in.Spec.AssumeBearer(); ok {
		r.add(Suggestion{Action: ActionSpec, Finding: spec.FindingAuth, Where: "security",
			Message: fmt.Sprintf("the spec declares no security; with Config.Token apitest sends it as a bearer token (scheme %q) to every operation", scheme),
			Fix: "declare it in the spec, so the authentication cases are generated:\n" +
				"components:\n  securitySchemes:\n    bearerAuth: { type: http, scheme: bearer }\nsecurity:\n  - bearerAuth: []"})
	}
}

var quoted = regexp.MustCompile(`"([^"]*)"`)

// bindings reviews the links of the spec. Heuristic bindings need no
// entry: the generator gives the examples the values the heuristic takes.
func (r *reviewer) bindings(set *bind.Set) {
	s := r.in.Spec
	for _, f := range set.Findings {
		if f.Kind != spec.FindingBinding {
			continue
		}
		fix := "fix the link in the spec"
		if m := quoted.FindStringSubmatch(f.Message); m != nil {
			switch {
			case strings.Contains(f.Message, "unknown operation"):
				if c := closest(m[1], opIDs(s)); c != "" {
					fix = fmt.Sprintf("the link names an operation that does not exist; did you mean %q?", c)
				}
			case strings.Contains(f.Message, "does not exist in"):
				fix = "the link sets a parameter the target operation does not have; fix the name or remove it"
			}
		}
		r.add(Suggestion{Action: ActionSpec, Finding: f.Kind, Where: f.Where, Message: f.Message, Fix: fix})
	}
}

func (r *reviewer) auth() {
	for _, f := range cases.AuthFindings(r.in.Spec) {
		op := r.opAt(f.Where)
		if strings.HasSuffix(f.Where, ".x-apitest-forbidden") && op != nil && op.HasOperationID {
			r.add(Suggestion{Action: ActionDefault, Finding: f.Kind, Where: f.Where, Message: f.Message,
				Key: op.ID + ".x-apitest-forbidden", Value: false,
				Fix: "turns the forbidden case off; or document a 403 response in the spec to keep it"})
			continue
		}
		r.add(Suggestion{Action: ActionSpec, Finding: f.Kind, Where: f.Where, Message: f.Message,
			Fix: "document the response the API sends without a valid token, e.g.\n\"401\":\n  description: Unauthorized\n" +
				"or leave these cases out with Config.SkipAuthCases"})
	}
}

func (r *reviewer) opAt(where string) *spec.Operation {
	for _, op := range r.in.Spec.Ops {
		if strings.HasPrefix(where, op.Where+".") {
			return op
		}
	}
	return nil
}

// notBuildable proposes values for the cases apitest could not send.
func (r *reviewer) notBuildable(set *bind.Set) {
	s := r.in.Spec
	all, err := cases.Build(s, cases.Options{})
	if err != nil {
		r.add(Suggestion{Action: ActionSpec, Finding: "CASES", Where: "cases", Message: err.Error(), Fix: "fix the spec; apitest cannot build any case"})
		return
	}
	var schemes openapi3.SecuritySchemes
	if s.Doc.Components != nil {
		schemes = s.Doc.Components.SecuritySchemes
	}
	fixed := r.in.Defaults.Params()
	for _, c := range all {
		if c.Skip != "" {
			continue
		}
		in := params.Inputs{Fixed: fixed, OpID: c.Op.ID, CaseName: c.ParamSource(),
			Binding: func(p *openapi3.Parameter) (any, bool) {
				return "bound", set.For(c.Op, p) != nil || (p.In == openapi3.ParameterInPath && r.modeled(c.Op, p.Name))
			}}
		_, err := exec.Prepare(c, exec.Input{Base: "http://localhost", Params: in,
			Auth: exec.ResolveAuth(c.Op.Security, schemes), Token: "token"})
		nb := (*exec.NotBuildableError)(nil)
		if !errors.As(err, &nb) {
			continue
		}
		missing := false
		for _, p := range c.Op.Params {
			if _, ok := params.Resolve(p, in); ok || (!p.Required && p.In != openapi3.ParameterInPath) {
				continue
			}
			missing = true
			if p.In == openapi3.ParameterInPath && r.generic(p.Name) {
				continue // proposed by genericIDs
			}
			r.missingParam(c.Op, p)
		}
		if missing {
			continue
		}
		action, fix, where := ActionSpec, "fix the spec: "+nb.Reason, c.Name
		if strings.HasPrefix(nb.Reason, "required body without example") {
			// one entry per operation, not per case
			action, where = ActionApply, c.Op.Where+".requestBody"
			fix = "run apitest-gen apply: it writes the body example; a field without value is listed as NO_VALUE or PATTERN_PENDING"
		}
		r.add(Suggestion{Action: action, Finding: "NOT_BUILDABLE", Where: where, Message: nb.Reason, Fix: fix})
	}
}

func (r *reviewer) missingParam(op *spec.Operation, p *openapi3.Parameter) {
	where := fmt.Sprintf("%s.parameters[%s]", op.Where, p.Name)
	msg := fmt.Sprintf("no value for required parameter %q (%s)", p.Name, p.In)
	if n := r.in.Dict.Parameters[p.In+"."+p.Name]; n != nil && n.Value != nil {
		r.add(Suggestion{Action: ActionApply, Finding: "NOT_BUILDABLE", Where: where, Message: msg,
			Fix: fmt.Sprintf("run apitest-gen apply: it writes the dictionary value %s as example", compact(n.Value))})
		return
	}
	r.chosen[p.In+"."+p.Name] = true
	key := p.Name
	if op.HasOperationID {
		key = op.ID + "." + p.Name
	}
	r.add(Suggestion{Action: ActionChoose, Finding: "NOT_BUILDABLE", Where: where, Message: msg, Key: key,
		Fix: "set a value that exists in the test environment" + constraints(p.Schema)})
}

// genericIDs proposes real ids for generic path parameters that have no
// binding and no default; apply would invent one.
func (r *reviewer) genericIDs(set *bind.Set) {
	for _, op := range r.in.Spec.Ops {
		for _, p := range op.Params {
			if p.In != openapi3.ParameterInPath || !r.generic(p.Name) {
				continue
			}
			if set.For(op, p) != nil || r.modeled(op, p.Name) {
				continue // a producer or a record provides the id
			}
			keys := apply.GenericIDKeys(op, p)
			if slices.ContainsFunc(keys, func(k string) bool { return r.known[strings.ToLower(k)] }) {
				continue
			}
			current := r.in.Dict.Paths[op.Path]
			fix := "no producer creates this resource during the test; set the id of one that exists in the test environment"
			if current != nil {
				fix += fmt.Sprintf(" (generated so far: %s)", compact(current))
			}
			r.add(Suggestion{Action: ActionChoose, Finding: "GENERIC_ID", Where: fmt.Sprintf("%s.parameters[%s]", op.Where, p.Name),
				Message: fmt.Sprintf("{%s} has no binding and no default", p.Name), Key: op.Path, Value: current, Fix: fix})
		}
	}
}

func (r *reviewer) generic(name string) bool {
	return slices.ContainsFunc(r.in.GenericIDs, func(g string) bool { return strings.EqualFold(g, name) })
}

// dictNotes proposes defaults for values the generator could not create.
func (r *reviewer) dictNotes() {
	for _, n := range r.in.DictNotes {
		switch n.Code {
		case dict.CodeNoValue, dict.CodePatternPending, dict.CodeTypeConflict:
			if r.chosen[strings.TrimPrefix(n.Where, "parameters.")] {
				continue // proposed with the operation already
			}
			r.add(Suggestion{Action: ActionChoose, Finding: n.Code, Where: n.Where, Message: n.Message, Key: dictKey(n.Where),
				Fix: "the generator cannot create a value that fits; set one by hand"})
		case dict.CodeValueInvalid:
			r.add(Suggestion{Action: ActionApply, Finding: n.Code, Where: n.Where, Message: n.Message,
				Fix: "run apitest-gen apply -repair to regenerate it, or correct the value in the dictionary"})
		}
	}
}

var noValue = regexp.MustCompile(`(\S+) has no value`)

// applyProblems proposes fixes for what apply reports.
func (r *reviewer) applyProblems() {
	if r.in.Apply == nil {
		return
	}
	for _, f := range r.in.Apply.Fatal {
		key := ""
		if m := quoted.FindStringSubmatch(f); m != nil {
			key = m[1]
		}
		r.edit(Suggestion{Action: ActionEdit, Finding: strings.Fields(f)[0], Where: "defaults", Message: f, Key: key,
			Fix: "correct or remove this entry; apply writes nothing while it is wrong"})
	}
	for _, n := range r.in.Apply.Notes {
		switch n.Code {
		case apply.CodeDefaultUnused:
			key := ""
			if m := quoted.FindStringSubmatch(n.Message); m != nil {
				key = m[1]
			}
			r.edit(Suggestion{Action: ActionEdit, Finding: n.Code, Where: n.Where, Message: n.Message, Key: key,
				Fix: "fix the spelling, use the key named in the message, or remove the entry"})
		case apply.CodeIncomplete:
			if strings.Contains(n.Where, ".parameters[") {
				continue // a case that cannot be sent, proposed with the operation
			}
			if m := noValue.FindStringSubmatch(n.Message); m != nil && !strings.Contains(m[1], "(") {
				r.add(Suggestion{Action: ActionChoose, Finding: n.Code, Where: n.Where, Message: n.Message, Key: m[1],
					Fix: "no value fits this field; set one"})
				continue
			}
			r.add(Suggestion{Action: ActionSpec, Finding: n.Code, Where: n.Where, Message: n.Message,
				Fix: "the schema allows no example (a cycle or a contradiction); simplify it in the spec"})
		case apply.CodeSharedParam:
			if r.boundAt(n.Where) {
				continue // a binding from a list GET is proposed for it
			}
			r.add(Suggestion{Action: ActionSpec, Finding: n.Code, Where: n.Where, Message: n.Message,
				Fix: "define the parameter in the operation instead of a shared component, then the operation can have its own value"})
		case apply.CodeBindSkipped, apply.CodeExternalRef:
			r.add(Suggestion{Action: ActionSpec, Finding: n.Code, Where: n.Where, Message: n.Message,
				Fix: "change the spec as the message says"})
		}
	}
}

// boundAt reports whether the parameter at where
// ("paths./x/{id}.get.parameters[id]") is bound in the spec or holds a
// record key.
func (r *reviewer) boundAt(where string) bool {
	for _, op := range r.in.Spec.Ops {
		for _, p := range op.Params {
			if fmt.Sprintf("%s.parameters[%s]", op.Where, p.Name) != where {
				continue
			}
			if r.modeled(op, p.Name) || (r.set != nil && r.set.For(op, p) != nil) {
				return true
			}
		}
	}
	return false
}

// edit records a fix for an existing defaults entry, which add would drop.
func (r *reviewer) edit(s Suggestion) {
	id := "edit|" + s.Key + "|" + s.Message
	if r.seen[id] {
		return
	}
	r.seen[id] = true
	r.res.Suggestions = append(r.res.Suggestions, s)
}

// dictKey turns a dictionary path into a defaults key:
// schemas.Garden.Sealed → Garden.Sealed, parameters.path.code → code.
func dictKey(where string) string {
	parts := strings.Split(strings.ReplaceAll(where, "[]", ""), ".")
	switch {
	case len(parts) >= 3 && parts[0] == "schemas":
		return parts[1] + "." + parts[len(parts)-1]
	case len(parts) == 2 && parts[0] == "schemas":
		return apply.DTOKey(parts[1])
	case len(parts) >= 3 && parts[0] == "parameters":
		return parts[len(parts)-1]
	}
	return where
}

func constraints(ref *openapi3.SchemaRef) string {
	if ref == nil || ref.Value == nil {
		return ""
	}
	s := ref.Value
	var c []string
	if t := s.Type; t != nil && len(*t) > 0 {
		c = append(c, "type "+strings.Join(*t, "|"))
	}
	if s.Format != "" {
		c = append(c, "format "+s.Format)
	}
	if s.Pattern != "" {
		c = append(c, "pattern "+s.Pattern)
	}
	if len(s.Enum) > 0 {
		c = append(c, "one of "+compact(s.Enum))
	}
	if len(c) == 0 {
		return ""
	}
	return " (" + strings.Join(c, ", ") + ")"
}

func opIDs(s *spec.Spec) []string {
	var out []string
	for _, op := range s.Ops {
		if op.HasOperationID {
			out = append(out, op.ID)
		}
	}
	return out
}

// closest returns the candidate nearest to name: equal ignoring case, or
// at most a third of its letters different.
func closest(name string, candidates []string) string {
	best, bestD := "", len(name)/3+1
	for _, c := range candidates {
		if strings.EqualFold(c, name) {
			return c
		}
		if d := distance(strings.ToLower(name), strings.ToLower(c)); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func compact(v any) string { return marshal(v, "") }

// marshal renders JSON without HTML escaping, indented after prefix.
func marshal(v any, prefix string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if prefix != "" {
		enc.SetIndent(prefix, "  ")
	}
	_ = enc.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

// Changes returns the entries review adds to defaults.json: proposed
// values and bindings, and for values only the user knows the value used
// so far, to be checked. Nothing else is written; the reasons are printed.
func (r *Result) Changes() []defaults.Pair {
	var add []defaults.Pair
	for _, s := range r.Suggestions {
		if s.Key == "" || s.Value == nil {
			continue
		}
		if s.Action == ActionDefault || s.Action == ActionChoose {
			add = append(add, defaults.Pair{Key: s.Key, Value: s.Value})
		}
	}
	return add
}
