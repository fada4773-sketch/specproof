package contract

import (
	"context"
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/gen/record"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Examples writes the requests and answers of a passed run into the spec
// as examples, through the writer of "apitest-gen record": per operation
// the first regular step (the default example, named "default" when the
// operation has error examples too), and one named example per documented
// error the scenario provoked ("duplicate" under 409, "stale" under 412,
// "unknown" under 404). Authentication steps get no example; apitest
// builds those cases itself. A step whose request does not fit the schema
// (the 400/422 step) is left out.
func Examples(s *spec.Spec, doc *yamldoc.Doc, sc *scenario.Scenario, run []apitest.StepExchange) (*record.Result, error) {
	type entry struct {
		op   *spec.Operation
		ex   apitest.StepExchange
		name string
	}
	var entries []*entry
	perOp := map[string][]*entry{}
	v := spec.NewValidator()
	statusSeen := map[string]bool{}
	for _, ex := range run {
		st := sc.Step(ex.ID)
		if st == nil || !ex.Passed || st.Token != "" {
			continue
		}
		op := s.Op(ex.OperationID)
		if op == nil {
			continue
		}
		key := fmt.Sprintf("%s %d", op.ID, ex.Status)
		if statusSeen[key] {
			continue
		}
		if !fits(v, op, ex) {
			continue
		}
		statusSeen[key] = true
		e := &entry{op: op, ex: ex}
		if ex.Status >= 400 {
			e.name = variant(ex.ID)
		}
		entries = append(entries, e)
		perOp[op.ID] = append(perOp[op.ID], e)
	}
	for _, list := range perOp {
		if len(list) < 2 {
			continue
		}
		used := map[string]bool{}
		for _, e := range list {
			if e.name == "" {
				e.name = cases.DefaultExample
			}
			base, n := e.name, 2
			for used[e.name] {
				e.name = fmt.Sprintf("%s-%d", base, n)
				n++
			}
			used[e.name] = true
		}
	}
	tags := map[string][]any{}
	var order []string
	for _, e := range entries {
		m := map[string]any{"status": record.StatusApproved}
		if e.name != "" {
			m["name"] = e.name
		}
		if len(e.ex.Path) > 0 {
			m["path"] = e.ex.Path
		}
		if len(e.ex.Query) > 0 {
			m["query"] = e.ex.Query
		}
		if e.ex.HasBody {
			m["body"] = e.ex.Body
		}
		resp := map[string]any{"status": e.ex.Status}
		if e.ex.ResponseBody != nil {
			resp["body"] = e.ex.ResponseBody
		}
		m["response"] = resp
		tag := e.op.Group()
		if _, ok := tags[tag]; !ok {
			order = append(order, tag)
		}
		tags[tag] = append(tags[tag], map[string]any{e.op.Method + " " + e.op.Path: m})
	}
	top := map[string]any{}
	for _, t := range order {
		top[t] = tags[t]
	}
	text, err := scenario.EncodeValue("", top)
	if err != nil {
		return nil, err
	}
	f, err := record.Parse(text)
	if err != nil {
		return nil, fmt.Errorf("examples: %w", err)
	}
	return record.Run(context.Background(), record.Input{Spec: s, Doc: doc, File: f})
}

// variant is the last part of a step id: "book1.create.duplicate" →
// "duplicate".
func variant(id string) string {
	return id[strings.LastIndex(id, ".")+1:]
}

// fits reports whether the request of a step fits the schema, as the
// examples of the spec must.
func fits(v *spec.Validator, op *spec.Operation, ex apitest.StepExchange) bool {
	for _, p := range op.Params {
		var val any
		var ok bool
		switch p.In {
		case openapi3.ParameterInPath:
			val, ok = ex.Path[p.Name]
		case openapi3.ParameterInQuery:
			val, ok = ex.Query[p.Name]
		}
		if !ok || p.Schema == nil || p.Schema.Value == nil {
			continue
		}
		if len(v.Validate(p.Schema.Value, spec.Normalize(val), spec.ModePlain)) > 0 {
			// a number from an answer for a string parameter is fine
			if _, isNum := val.(interface{ String() string }); !isNum {
				return false
			}
		}
	}
	if ex.HasBody {
		if s := requestSchema(op); s != nil && len(v.Validate(s, spec.Normalize(ex.Body), spec.ModeRequest)) > 0 {
			return false
		}
	}
	return true
}
