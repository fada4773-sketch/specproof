package record

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// SuggestionsKey is the key the run writes its suggestions under in the
// defaults file.
const SuggestionsKey = "$suggestions"

// Suggestion is what to change in the defaults file so an operation gets
// an example (NO_DATA) or apitest finds its data (NOT_IN_CONTAINER): the
// entries to merge into "params", "select", "seed" or "$apitest".
type Suggestion struct {
	Where   string
	Code    string
	Problem string
	Hint    string
	Fix     map[string]any
}

// suggest adds a suggestion; the same one twice is kept once.
func (r *Result) suggest(code, where, problem, hint string, fix map[string]any) {
	s := Suggestion{Where: where, Code: code, Problem: problem, Hint: hint, Fix: fix}
	for _, old := range r.Suggestions {
		if old.Where == where && old.Code == code && text(old.Fix) == text(fix) {
			return
		}
	}
	r.Suggestions = append(r.Suggestions, s)
}

// suggestions writes what can be merged into the defaults file: per
// operation a list of {code, problem, hint, and the entries}.
func suggestions(list []Suggestion) map[string]any {
	out := map[string]any{"$comment": "written by apitest-gen record: merge an entry into \"params\", \"select\", \"seed\" or \"$apitest\" and run again; every run writes this anew"}
	for _, s := range list {
		e := map[string]any{"code": s.Code, "problem": s.Problem, "hint": s.Hint}
		for k, v := range s.Fix {
			e[k] = v
		}
		prev, _ := out[s.Where].([]any)
		out[s.Where] = append(prev, e)
	}
	return out
}

// SaveSuggestions writes "$suggestions" into the defaults file, or removes
// it when there are none.
func SaveSuggestions(path string, list []Suggestion) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var out []byte
	if len(list) == 0 {
		out, err = deleteKey(b, SuggestionsKey)
	} else {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false) // "<field …>" stays readable
		enc.SetIndent("  ", "  ")
		if err = enc.Encode(suggestions(list)); err == nil {
			out, err = setKey(b, SuggestionsKey, bytes.TrimRight(buf.Bytes(), "\n"))
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, out, 0o644)
}

// dto is a DTO name of a table for the defaults file: the shortest schema
// of the spec that belongs to it, else the table itself.
func (rd *reader) dto(t string) string {
	best := ""
	if rd.s.Doc != nil && rd.s.Doc.Components != nil {
		for _, name := range sortedKeys(rd.s.Doc.Components.Schemas) {
			if rd.n.table(name) == t && (best == "" || len(name) < len(best)) {
				best = name
			}
		}
	}
	if best == "" {
		return t
	}
	return best
}

// missing are the path and required query parameters of an operation
// without a value.
func (rd *reader) missing(op *spec.Operation) []string {
	var out []string
	for _, p := range op.Params {
		if p.In != openapi3.ParameterInPath && (p.In != openapi3.ParameterInQuery || !p.Required) {
			continue
		}
		if _, ok := rd.value(op, p, rd.k); !ok {
			out = append(out, p.Name)
		}
	}
	return out
}

// suggestParams proposes values for the parameters without one.
func (rd *reader) suggestParams(code string, op *spec.Operation, problem string) {
	missing := rd.missing(op)
	if len(missing) == 0 {
		return
	}
	params := map[string]any{}
	for _, name := range missing {
		params[op.ID+"."+name] = map[string]any{"field": "<field of a selected record that holds it>"}
	}
	rd.res.suggest(code, op.ID, problem,
		fmt.Sprintf("no selected record has a field for {%s}: name the field ({\"field\": …}), put it together ({\"format\": \"{Dto.field}-{field}\"}) or set a fixed value", strings.Join(missing, "}, {")),
		map[string]any{"params": params})
}

// suggestRead proposes how a GET the instance answered with an error gets
// a record it answers for: "select" takes a record of the table of its last
// parameter for which the GET answers 2xx.
func (rd *reader) suggestRead(op *spec.Operation, problem string) {
	if strings.HasPrefix(rd.failed[op.ID], "no value for") {
		rd.suggestParams(CodeNoData, op, problem)
		return
	}
	_, vals, ok := rd.url(op, rd.k)
	last := lastParam(op.Path)
	if !ok || last == "" {
		return
	}
	if t := vals[last].table; t != "" {
		dto := rd.dto(t)
		rd.res.suggest(CodeNoData, op.ID, problem,
			fmt.Sprintf("the selected %s has no data at %s; let \"select\" take a %s for which it answers 2xx", dto, op.Path, dto),
			map[string]any{"select": map[string]any{dto: map[string]any{"details": map[string]any{op.Path: map[string]any{}}}}})
		return
	}
	rd.res.suggest(CodeNoData, op.ID, problem,
		fmt.Sprintf("{%s} comes from \"params\"; set a value the instance holds", last),
		map[string]any{"params": map[string]any{op.ID + "." + last: "<a value the instance holds>"}})
}

// suggestSeed proposes to put a DTO into the seed, so the empty environment
// holds it before the test.
func (s *sim) suggestSeed(where, problem, t string) {
	dto := s.rd.dto(t)
	seed := slices.Clone(s.cfg.Seed)
	if !slices.ContainsFunc(seed, func(name string) bool { return s.rd.n.table(name) == t }) {
		seed = append(seed, dto)
	}
	s.res.suggest(CodeContainer, where, problem,
		fmt.Sprintf("the empty environment holds no %s at that point: put it into the seed (create it before the test)", dto),
		map[string]any{"seed": seed})
}

// suggestOrder proposes to run the write of a path before its GET.
func (s *sim) suggestOrder(where, problem string, write *spec.Operation) {
	order := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodGet}
	if write.Method == http.MethodPost {
		order = []string{http.MethodPost, http.MethodGet}
	}
	s.res.suggest(CodeContainer, where, problem,
		fmt.Sprintf("let %s run before the GET of its path", write.ID),
		map[string]any{"$apitest": map[string]any{"MethodOrder": order}})
}

// suggestDeleteLast proposes to run the DELETEs at the end.
func (s *sim) suggestDeleteLast(where, problem string) {
	s.res.suggest(CodeContainer, where, problem, "a DELETE runs before this GET; run the DELETEs at the end",
		map[string]any{"$apitest": map[string]any{"DeleteLast": true}})
}
