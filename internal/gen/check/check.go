// Package check reports what would keep apitest from running a spec: cases
// that would be NOT_BUILDABLE and examples that violate their schema. It
// uses apitest's own case building, bindings and request preparation, so
// the result is what a real run would see.
package check

import (
	"errors"
	"sort"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/exec"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Kinds of problems.
const (
	KindNotBuildable  = "NOT_BUILDABLE"  // apitest cannot send the case
	KindExampleSchema = "EXAMPLE_SCHEMA" // an example violates its schema
	KindBinding       = "BINDING"        // x-apitest-bind or links are invalid
	KindCases         = "CASES"          // cases cannot be built at all
	KindHeuristic     = "HEURISTIC"      // a parameter apitest binds by guessing (Lint)
	KindValidation    = "VALIDATION"     // the spec breaks a rule of OpenAPI (Lint)
	KindAuth          = "AUTH"           // authentication cases apitest cannot build (Lint)
)

// Problem is one finding.
type Problem struct {
	Kind    string
	Where   string // case name or spec location
	Message string
	// Fix says how to remove the finding; set by Lint.
	Fix string
}

// fixes say per kind how to remove a finding.
var fixes = map[string]string{
	KindNotBuildable:  "give the case the value it lacks: an example in the spec, a default or enum in the schema, Config.Params (\"$apitest\" or the values of the defaults file), or skip it with x-apitest-skip",
	KindExampleSchema: "change the example or the schema so they agree; apitest fails the case otherwise",
	KindBinding:       "x-apitest-bind needs {from: <operationId>, pointer: /field} (or header: Location, source: request); a link needs the operationId and the parameters of an existing operation",
	KindCases:         "the cases cannot be built; fix the spec at the place named",
	KindHeuristic:     "declare where the value comes from: x-apitest-bind: {from: <operationId>, pointer: /field} at the parameter, or a link in the 2xx response of the producer; \"apitest-gen record\" writes it for the bindings it can check",
	KindValidation:    "the spec breaks a rule of OpenAPI; fix it at the place named",
	KindAuth:          "document 401 and 403 for operations with security, or remove x-apitest-forbidden",
}

// lintKinds maps the findings of the spec and the bindings to kinds.
var lintKinds = map[string]string{
	spec.FindingHeuristic:  KindHeuristic,
	spec.FindingBinding:    KindBinding,
	spec.FindingValidation: KindValidation,
	spec.FindingAuth:       KindAuth,
}

// Result of a check.
type Result struct {
	Problems []Problem
	Cases    int // all cases
	Ready    int // cases apitest can send
}

// Run checks s. fixed are values apitest would get through Config.Params
// (plain or "<operationId>.<name>" keys), e.g. from defaults.json.
func Run(s *spec.Spec, fixed map[string]string) *Result {
	res := &Result{}
	for _, f := range s.Findings {
		if f.Kind == spec.FindingExampleSchema {
			res.Problems = append(res.Problems, Problem{Kind: KindExampleSchema, Where: f.Where, Message: f.Message})
		}
	}
	set, err := bind.Resolve(s)
	if err != nil {
		res.Problems = append(res.Problems, Problem{Kind: KindBinding, Where: "bindings", Message: err.Error()})
		return res
	}
	all, err := cases.Build(s, cases.Options{})
	if err != nil {
		res.Problems = append(res.Problems, Problem{Kind: KindCases, Where: "cases", Message: err.Error()})
		return res
	}
	var schemes openapi3.SecuritySchemes
	if s.Doc.Components != nil {
		schemes = s.Doc.Components.SecuritySchemes
	}
	for _, c := range all {
		res.Cases++
		if c.Skip != "" {
			continue // skipped on purpose with x-apitest-skip
		}
		_, err := exec.Prepare(c, exec.Input{
			Base: "http://localhost",
			Params: params.Inputs{
				Fixed: fixed,
				OpID:  c.Op.ID,
				// bound values exist at run time; any value stands for them
				Binding: func(p *openapi3.Parameter) (any, bool) {
					return "bound", set.For(c.Op, p) != nil
				},
			},
			Auth:  exec.ResolveAuth(c.Op.Security, schemes),
			Token: "token",
		})
		if nb := (*exec.NotBuildableError)(nil); errors.As(err, &nb) {
			res.Problems = append(res.Problems, Problem{Kind: KindNotBuildable, Where: c.Name, Message: nb.Reason})
			continue
		}
		res.Ready++
	}
	sortProblems(res.Problems)
	return res
}

// Lint reports everything apitest.Run reports about s before it sends a
// request, each finding with how to fix it: what Run finds, the other
// findings of the spec (OpenAPI rules, security), the parameters apitest
// binds by guessing and the links it cannot use.
func Lint(s *spec.Spec, fixed map[string]string) *Result {
	res := Run(s, fixed)
	findings := append([]spec.Finding(nil), s.Findings...)
	if set, err := bind.Resolve(s); err == nil {
		findings = append(findings, set.Findings...)
	}
	findings = append(findings, cases.AuthFindings(s)...)
	for _, f := range findings {
		if kind := lintKinds[f.Kind]; kind != "" {
			res.Problems = append(res.Problems, Problem{Kind: kind, Where: f.Where, Message: f.Message})
		}
	}
	for i := range res.Problems {
		res.Problems[i].Fix = fixes[res.Problems[i].Kind]
	}
	sortProblems(res.Problems)
	return res
}

func sortProblems(ps []Problem) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Kind != ps[j].Kind {
			return ps[i].Kind < ps[j].Kind
		}
		return ps[i].Where < ps[j].Where
	})
}
