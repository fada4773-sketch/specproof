package record

import (
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Coverage tells which places apitest needs an example at hold one after
// the run: the path and required query parameters, the JSON request body
// and the first 2xx response with JSON content of every operation.
type Coverage struct {
	Places int // the places that need an example
	// Run are the places with an example this run wrote; Kept those with
	// one taken from the last output (unchanged operations)
	Run, Kept int
	// Generated are the values the run generated because no data read had
	// them (required fields of assembled bodies)
	Generated int
	Missing   []Gap
}

// Gap is a place without example and why it has none.
type Gap struct {
	Op, Place, Why string
}

// Without is the number of places without example.
func (c Coverage) Without() int { return len(c.Missing) }

// coverage looks at every place of every operation in the document.
func (em *emitter) coverage(s *spec.Spec, res *Result, ops, needs map[string]bool) Coverage {
	var c Coverage
	for _, op := range s.Ops {
		for _, pl := range em.places(op) {
			c.Places++
			switch {
			case pl.has && ops[op.ID] && needs[op.ID]:
				c.Run++
			case pl.has:
				c.Kept++
			default:
				c.Missing = append(c.Missing, Gap{Op: op.ID, Place: pl.name, Why: gapReason(res, op, pl, ops, needs)})
			}
		}
	}
	return c
}

// coverPlace is a place of an operation and whether it holds an example.
type coverPlace struct {
	name  string // "{dockCode}", "request body", "response 201"
	where string // its location in the spec, as the notes name it
	has   bool
}

// places are the places of an operation apitest needs an example at.
func (em *emitter) places(op *spec.Operation) []coverPlace {
	var out []coverPlace
	for _, p := range op.Params {
		if p.In != openapi3.ParameterInPath && (p.In != openapi3.ParameterInQuery || !p.Required) {
			continue
		}
		e := em.paramEntry(op, p.Name, p.In)
		out = append(out, coverPlace{name: "{" + p.Name + "}", where: op.Where + ".parameters[" + p.Name + "]",
			has: e != nil && yamldoc.Get(e.target, "example") != nil})
	}
	if requestSchema(op) != nil {
		pl := em.request(op)
		out = append(out, coverPlace{name: "request body", where: op.Where + ".requestBody", has: pl != nil && pl.example() != nil})
	}
	if op.Op.Responses != nil {
		for _, code := range sortedKeys(op.Op.Responses.Map()) {
			if !strings.HasPrefix(code, "2") {
				continue
			}
			pl := em.response(op, code)
			if pl == nil {
				continue // no JSON content: nothing to show
			}
			out = append(out, coverPlace{name: "response " + code, where: op.Where + ".responses." + code, has: pl.example() != nil})
			break
		}
	}
	return out
}

// gapReason is why a place has no example: the note of the run about its
// operation or place, else what the run did with it.
func gapReason(res *Result, op *spec.Operation, pl coverPlace, ops, needs map[string]bool) string {
	switch {
	case !ops[op.ID]:
		return "the operation is not in the run (excluded by the config, or apitest skips it)"
	case !needs[op.ID]:
		return "unchanged, and the last output has no example here: run with -overwrite"
	}
	all := append(append([]Note{}, res.Problems...), res.Notes...)
	for _, n := range all {
		if n.Where == pl.where || strings.HasPrefix(n.Where, pl.where+".") {
			return n.Code + ": " + firstLine(n.Message)
		}
	}
	for _, code := range []string{CodeNoData, CodeNotExecuted, CodeWriteFailed, CodeFetch, CodeParam, CodeShared, CodeBuilt} {
		for _, n := range all {
			if n.Code == code && n.Where == op.ID {
				return n.Code + ": " + firstLine(n.Message)
			}
		}
	}
	if strings.HasPrefix(pl.name, "{") {
		return "no record of the run has a value for it: set it in \"params\""
	}
	return "the run got no data for it"
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
