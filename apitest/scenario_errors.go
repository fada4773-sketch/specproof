package apitest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/exec"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// ErrorsInvalid is the entry of ScenarioConfig.Errors that generates the
// steps with invalid input: a parameter or a body field that violates the
// schema of the spec. They accept the statuses of
// ScenarioConfig.InvalidStatus.
const ErrorsInvalid = "invalid"

// generated marks a step RunScenario built itself from a step of the
// scenario (ScenarioConfig.Errors).
type generated struct {
	// accept are the statuses the step accepts ("400", "4xx"); nil
	// accepts the Expect of the step only.
	accept []string
}

// errorMatches reports whether an entry of ScenarioConfig.Errors or
// InvalidStatus ("404", "4xx") matches a status.
func errorMatches(entry string, code int) bool {
	e, c := strings.ToLower(entry), strconv.Itoa(code)
	return e == c || strings.HasSuffix(e, "xx") && len(e) == 3 && e[0] == c[0]
}

// errorSelected reports whether ScenarioConfig.Errors selects a status.
func (s *scenarioRunner) errorSelected(code int) bool {
	return slices.ContainsFunc(s.scfg.Errors, func(e string) bool { return errorMatches(e, code) })
}

// invalidSelected reports whether ScenarioConfig.Errors asks for the steps
// with invalid input.
func (s *scenarioRunner) invalidSelected() bool {
	return slices.ContainsFunc(s.scfg.Errors, func(e string) bool { return strings.EqualFold(e, ErrorsInvalid) })
}

// invalidStatus are the statuses a step with invalid input accepts.
func (s *scenarioRunner) invalidStatus() []string {
	if len(s.scfg.InvalidStatus) == 0 {
		return []string{"4xx"}
	}
	return s.scfg.InvalidStatus
}

// statusFits reports whether a step accepts the status of its answer.
func (s *scenarioRunner) statusFits(st *scenario.Step, code int) bool {
	if g := s.gen[st]; g != nil && g.accept != nil {
		return slices.ContainsFunc(g.accept, func(e string) bool { return errorMatches(e, code) })
	}
	return code == st.Expect
}

// expectText is the status a step expects, as the report shows it.
func (s *scenarioRunner) expectText(st *scenario.Step) string {
	if g := s.gen[st]; g != nil && g.accept != nil {
		return strings.Join(g.accept, " | ")
	}
	return strconv.Itoa(st.Expect)
}

// generateErrors adds the error steps ScenarioConfig.Errors asks for. The
// steps of the scenario before its first DELETE run first (the clean
// state), then per operation of the scenario, built from its first
// successful step:
//
//   - 409: a POST that documents 409 sends the body of that step again;
//   - 404: the last path parameter gets a key no record has, if the
//     operation documents 404;
//   - 401: without token and with a manipulated token, if the operation
//     needs a token and documents 401 (403 for the manipulated one when
//     only 403 is documented);
//   - 403: with Config.ForbiddenToken, if the operation documents 403 and
//     sets x-apitest-forbidden;
//   - invalid: a required body field left out, a body field of the wrong
//     type, a body field and a parameter that break a rule of their schema
//     (enum, pattern, length, range, format).
//
// The DELETEs and the steps after them follow. An operation, status and
// token the scenario already has a step for gets none. With Update nothing
// is generated.
func (s *scenarioRunner) generateErrors(steps []*scenario.Step) []*scenario.Step {
	if len(s.scfg.Errors) == 0 || s.scfg.Update {
		return steps
	}
	split := len(steps)
	for i, st := range steps {
		if s.caseOf[st].Op.Method == http.MethodDelete {
			split = i
			break
		}
	}
	before, after := steps[:split], steps[split:]
	saved, earlier := map[string]bool{}, map[string]bool{}
	for _, st := range before {
		earlier[st.ID] = true
		for name := range st.Save {
			saved[name] = true
		}
	}
	// a template after the split may only use what the clean state gives
	usable := func(st *scenario.Step) bool {
		for _, name := range st.Uses() {
			if !saved[name] {
				return false
			}
		}
		return st.BodyFrom == "" || earlier[st.BodyFrom]
	}
	have := map[string]bool{}
	for _, st := range s.sc.Steps {
		have[fmt.Sprintf("%s %d %s", s.caseOf[st].Op.ID, st.Expect, st.Token)] = true
	}
	exists := func(op *spec.Operation, code int, token string) bool {
		return have[fmt.Sprintf("%s %d %s", op.ID, code, token)]
	}
	var conflict, notFound, unauthorized, forbidden, invalid []*scenario.Step
	seen := map[string]bool{}
	for i, t := range steps {
		op := s.caseOf[t].Op
		if seen[op.ID] || t.Expect/100 != 2 || t.Token != scenario.TokenDefault || i >= split && !usable(t) {
			continue
		}
		seen[op.ID] = true
		rs := op.Op.Responses
		if rs == nil {
			continue
		}
		if s.errorSelected(http.StatusConflict) && op.Method == http.MethodPost && rs.Value("409") != nil &&
			(t.HasBody || t.BodyFrom != "") && !exists(op, http.StatusConflict, scenario.TokenDefault) {
			st := s.errorStep(t, "conflict", http.StatusConflict, cases.Conflict, "sends the body of "+t.ID+" again: 409")
			st.Body, st.HasBody, st.BodyFrom = nil, false, t.ID
			conflict = append(conflict, st)
		}
		if s.errorSelected(http.StatusNotFound) && rs.Value("404") != nil && !exists(op, http.StatusNotFound, scenario.TokenDefault) {
			if st := s.notFoundStep(t, op); st != nil {
				notFound = append(notFound, st)
			}
		}
		if exec.ResolveAuth(op.Security, s.schemes).Needed() {
			unauthorized = append(unauthorized, s.authSteps(t, op, exists)...)
			if s.errorSelected(http.StatusForbidden) && s.cfg.ForbiddenToken != nil && rs.Value("403") != nil &&
				forbiddenOptIn(op) && !exists(op, http.StatusForbidden, scenario.TokenForbidden) {
				st := s.errorStep(t, "forbidden", http.StatusForbidden, cases.Forbidden, "with the forbidden token: 403")
				st.Token = scenario.TokenForbidden
				forbidden = append(forbidden, st)
			}
		}
		if s.invalidSelected() {
			invalid = append(invalid, s.invalidSteps(t, op)...)
		}
	}
	return slices.Concat(before, conflict, notFound, unauthorized, forbidden, invalid, after)
}

func forbiddenOptIn(op *spec.Operation) bool {
	v, _ := op.Op.Extensions["x-apitest-forbidden"].(bool)
	return v
}

// errorStep builds a step from a template: its call, parameters, body and
// unique pointers, without saves and checks.
func (s *scenarioRunner) errorStep(t *scenario.Step, variant string, expect int, kind cases.Kind, why string) *scenario.Step {
	st := &scenario.Step{
		ID:       s.freeID(t.ID + "." + variant),
		Call:     t.Call,
		Path:     cloneParams(t.Path),
		Query:    cloneParams(t.Query),
		Header:   cloneParams(t.Header),
		Body:     scenario.Clone(t.Body),
		HasBody:  t.HasBody,
		BodyFrom: t.BodyFrom,
		Token:    t.Token,
		Expect:   expect,
		Unique:   t.Unique,
		Why:      why,
	}
	c := cases.Step(s.caseOf[t].Op, st.ID, expect, st.Token)
	c.Kind = kind
	s.caseOf[st] = c
	s.gen[st] = &generated{}
	return st
}

// freeID is id, or id with a number when a step has it already.
func (s *scenarioRunner) freeID(id string) string {
	taken := func(x string) bool {
		if s.sc.Step(x) != nil {
			return true
		}
		for st := range s.gen {
			if st.ID == x {
				return true
			}
		}
		return false
	}
	out := id
	for n := 2; taken(out); n++ {
		out = fmt.Sprintf("%s%d", id, n)
	}
	return out
}

func cloneParams(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out, _ := scenario.Clone(m).(map[string]any)
	return out
}

// notFoundStep sends a key no record has in the last path parameter.
func (s *scenarioRunner) notFoundStep(t *scenario.Step, op *spec.Operation) *scenario.Step {
	p := lastPathParam(op)
	if p == nil {
		return nil
	}
	v, why := cases.NotFoundValue(p)
	if why != "" {
		s.warn(fmt.Sprintf("Errors: no 404 step for %s: %s", op.ID, why))
		return nil
	}
	st := s.errorStep(t, "notFound", http.StatusNotFound, cases.NotFound, fmt.Sprintf("{%s} no record has: 404", p.Name))
	if st.Path == nil {
		st.Path = map[string]any{}
	}
	st.Path[p.Name] = v
	return st
}

// lastPathParam is the last path parameter of op: the key of the record
// the path addresses.
func lastPathParam(op *spec.Operation) *openapi3.Parameter {
	segs := strings.Split(strings.Trim(op.Path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		name, ok := strings.CutPrefix(segs[i], "{")
		if !ok {
			continue
		}
		if p := findParam(op, openapi3.ParameterInPath, strings.TrimSuffix(name, "}")); p != nil {
			return p
		}
	}
	return nil
}

// authSteps sends a step without token and with a manipulated token.
func (s *scenarioRunner) authSteps(t *scenario.Step, op *spec.Operation, exists func(*spec.Operation, int, string) bool) []*scenario.Step {
	rs := op.Op.Responses
	var out []*scenario.Step
	if rs.Value("401") != nil {
		if s.errorSelected(http.StatusUnauthorized) && !exists(op, http.StatusUnauthorized, scenario.TokenNone) {
			st := s.errorStep(t, "unauthorized", http.StatusUnauthorized, cases.Unauthorized, "without token: 401")
			st.Token = scenario.TokenNone
			out = append(out, st)
		}
		if s.errorSelected(http.StatusUnauthorized) && !exists(op, http.StatusUnauthorized, scenario.TokenInvalid) {
			st := s.errorStep(t, "invalidToken", http.StatusUnauthorized, cases.InvalidToken, "with a manipulated token: 401")
			st.Token = scenario.TokenInvalid
			out = append(out, st)
		}
		return out
	}
	if rs.Value("403") != nil && s.errorSelected(http.StatusForbidden) && !exists(op, http.StatusForbidden, scenario.TokenInvalid) {
		st := s.errorStep(t, "invalidToken", http.StatusForbidden, cases.InvalidToken, "with a manipulated token: 403")
		st.Token = scenario.TokenInvalid
		out = append(out, st)
	}
	return out
}

// invalidSteps sends input the schema forbids: a required body field left
// out, a body field of the wrong type, a body field and a parameter that
// break a rule of their schema. Each step changes one value.
func (s *scenarioRunner) invalidSteps(t *scenario.Step, op *spec.Operation) []*scenario.Step {
	expect := http.StatusBadRequest
	if op.Op.Responses.Value("400") == nil && op.Op.Responses.Value("422") != nil {
		expect = http.StatusUnprocessableEntity
	}
	add := func(variant, why string) *scenario.Step {
		st := s.errorStep(t, variant, expect, cases.Invalid, why+": "+strings.Join(s.invalidStatus(), " | "))
		s.gen[st].accept = s.invalidStatus()
		return st
	}
	var out []*scenario.Step
	body, _ := t.Body.(map[string]any)
	mt, _ := cases.RequestMedia(op)
	sch := requestSchema(s.caseOf[t])
	if body != nil && sch != nil && cases.IsJSONMedia(mt) {
		if name := firstRequired(sch, body); name != "" {
			st := add("invalidMissing", "leaves out the required field "+name)
			delete(st.Body.(map[string]any), name)
			out = append(out, st)
		}
		for _, k := range scenarioKeys(body) {
			p := schemaProperty(sch, k)
			if p == nil || p.ReadOnly {
				continue
			}
			if v, ok := wrongType(p); ok {
				st := add("invalidType", fmt.Sprintf("%s of the wrong type", k))
				st.Body.(map[string]any)[k] = v
				out = append(out, st)
				break
			}
		}
		for _, k := range scenarioKeys(body) {
			p := schemaProperty(sch, k)
			if p == nil || p.ReadOnly {
				continue
			}
			if v, rule, ok := breakRule(p, true); ok {
				st := add("invalidValue", fmt.Sprintf("%s breaks %s", k, rule))
				st.Body.(map[string]any)[k] = v
				out = append(out, st)
				break
			}
		}
	}
	for _, in := range []string{openapi3.ParameterInPath, openapi3.ParameterInQuery} {
		for _, p := range op.Params {
			if p.In != in || p.Schema == nil || p.Schema.Value == nil {
				continue
			}
			given := t.Path
			if in == openapi3.ParameterInQuery {
				given = t.Query
			}
			if _, ok := given[p.Name]; !ok && !p.Required {
				continue
			}
			v, ok := wrongType(p.Schema.Value)
			rule := "its type"
			if _, isText := v.(string); !ok || !isText {
				// a parameter is text: only a text of the wrong form fits the URL
				v, rule, ok = breakRule(p.Schema.Value, in != openapi3.ParameterInPath)
			}
			if !ok {
				continue
			}
			st := add("invalidParam", fmt.Sprintf("%s parameter %s breaks %s", in, p.Name, rule))
			if in == openapi3.ParameterInPath {
				if st.Path == nil {
					st.Path = map[string]any{}
				}
				st.Path[p.Name] = v
			} else {
				if st.Query == nil {
					st.Query = map[string]any{}
				}
				st.Query[p.Name] = v
			}
			return append(out, st)
		}
	}
	return out
}

// firstRequired is the first required field of the schema the body holds.
func firstRequired(sch *openapi3.Schema, body map[string]any) string {
	var req []string
	var collect func(x *openapi3.Schema, depth int)
	collect = func(x *openapi3.Schema, depth int) {
		if x == nil || depth > 20 {
			return
		}
		req = append(req, x.Required...)
		for _, part := range x.AllOf {
			collect(part.Value, depth+1)
		}
	}
	collect(sch, 0)
	for _, name := range req {
		if _, ok := body[name]; ok {
			return name
		}
	}
	return ""
}

// violates reports whether a value breaks a schema.
func violates(sch *openapi3.Schema, v any) bool {
	return len(spec.NewValidator().Validate(sch, spec.Normalize(v), spec.ModePlain)) > 0
}

// wrongType is a value of another type than the schema allows.
func wrongType(sch *openapi3.Schema) (any, bool) {
	if sch.Type == nil || len(sch.Type.Slice()) == 0 {
		return nil, false
	}
	var v any
	switch {
	case sch.Type.Is("integer") || sch.Type.Is("number"):
		v = "not-a-number"
	case sch.Type.Is("boolean"):
		v = "not-a-boolean"
	case sch.Type.Is("string"):
		// a number may be read as text; an object never is
		v = map[string]any{"invalid": true}
	case sch.Type.Is("array"):
		v = "not-an-array"
	case sch.Type.Is("object"):
		v = "not-an-object"
	default:
		return nil, false
	}
	return v, violates(sch, v)
}

// breakRule is a value of the right type that breaks another rule of the
// schema: enum, format, pattern, length or range. empty allows "".
func breakRule(sch *openapi3.Schema, empty bool) (v any, rule string, ok bool) {
	type candidate struct {
		v    any
		rule string
	}
	var cs []candidate
	switch {
	case sch.Type.Is("string"):
		if len(sch.Enum) > 0 {
			cs = append(cs, candidate{"NOT_IN_ENUM", "its enum"})
		}
		switch sch.Format {
		case "uuid":
			cs = append(cs, candidate{"not-a-uuid", "format uuid"})
		case "date", "date-time":
			cs = append(cs, candidate{"not-a-date", "format " + sch.Format})
		case "email":
			cs = append(cs, candidate{"not-an-email", "format email"})
		}
		if sch.MaxLength != nil {
			cs = append(cs, candidate{strings.Repeat("x", int(*sch.MaxLength)+1), fmt.Sprintf("maxLength %d", *sch.MaxLength)})
		}
		if sch.Pattern != "" {
			for _, x := range []string{"#invalid value#", "~", "0"} {
				cs = append(cs, candidate{x, "pattern " + sch.Pattern})
			}
		}
		if sch.MinLength > 0 && empty {
			cs = append(cs, candidate{"", fmt.Sprintf("minLength %d", sch.MinLength)})
		}
	case sch.Type.Is("integer") || sch.Type.Is("number"):
		if len(sch.Enum) > 0 {
			cs = append(cs, candidate{json.Number("987654321"), "its enum"})
		}
		if sch.Max != nil {
			cs = append(cs, candidate{json.Number(strconv.FormatFloat(*sch.Max+1, 'f', -1, 64)), "maximum " + strconv.FormatFloat(*sch.Max, 'f', -1, 64)})
		}
		if sch.Min != nil {
			cs = append(cs, candidate{json.Number(strconv.FormatFloat(*sch.Min-1, 'f', -1, 64)), "minimum " + strconv.FormatFloat(*sch.Min, 'f', -1, 64)})
		}
	}
	for _, c := range cs {
		if violates(sch, c.v) {
			return c.v, c.rule, true
		}
	}
	return nil, "", false
}

// unusedErrors warns about entries of ScenarioConfig.Errors no step
// matches: neither the scenario nor the generation has such a step.
func (s *scenarioRunner) unusedErrors() {
	if s.scfg.Update {
		return
	}
	for _, e := range s.scfg.Errors {
		used := slices.ContainsFunc(s.steps, func(st *scenario.Step) bool {
			if strings.EqualFold(e, ErrorsInvalid) {
				return s.caseOf[st].Kind == cases.Invalid
			}
			return st.Expect >= 400 && errorMatches(e, st.Expect)
		})
		if used {
			continue
		}
		msg := fmt.Sprintf("ScenarioConfig.Errors: no step for %q: the scenario has none and the spec documents it for no operation the scenario calls", e)
		if !strings.EqualFold(e, ErrorsInvalid) && e[0] == '5' {
			msg += fmt.Sprintf("; a 5xx answer to invalid input is accepted with %q in Errors and InvalidStatus", ErrorsInvalid)
		}
		s.warn(msg)
	}
}
