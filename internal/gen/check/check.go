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
)

// Problem is one finding.
type Problem struct {
	Kind    string
	Where   string // case name or spec location
	Message string
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
			res.Problems = append(res.Problems, Problem{KindExampleSchema, f.Where, f.Message})
		}
	}
	set, err := bind.Resolve(s)
	if err != nil {
		res.Problems = append(res.Problems, Problem{KindBinding, "bindings", err.Error()})
		return res
	}
	all, err := cases.Build(s, cases.Options{})
	if err != nil {
		res.Problems = append(res.Problems, Problem{KindCases, "cases", err.Error()})
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
			res.Problems = append(res.Problems, Problem{KindNotBuildable, c.Name, nb.Reason})
			continue
		}
		res.Ready++
	}
	sort.SliceStable(res.Problems, func(i, j int) bool {
		if res.Problems[i].Kind != res.Problems[j].Kind {
			return res.Problems[i].Kind < res.Problems[j].Kind
		}
		return res.Problems[i].Where < res.Problems[j].Where
	})
	return res
}
