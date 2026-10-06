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

	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// SuggestionsKey is the key the run writes its suggestions under in the
// defaults file.
const SuggestionsKey = "$suggestions"

// Suggestion is what to change in the defaults file so an operation gets
// an example (NO_DATA), apitest finds its data (NOT_IN_CONTAINER) or
// ignores fields that change (VOLATILE, DATA_CHANGED): the entries to merge
// into "params", "select", "seed" or "$apitest". Without entries the hint
// explains a cause the defaults cannot fix.
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
		if old.Where == where && old.Code == code && text(old.Fix) == text(fix) && (fix != nil || old.Hint == hint) {
			return
		}
	}
	r.Suggestions = append(r.Suggestions, s)
}

// suggestions writes what can be merged into the defaults file: per
// operation a list of {code, problem, hint, and the entries}.
func suggestions(list []Suggestion) map[string]any {
	out := map[string]any{"$comment": "written by apitest-gen record: merge an entry into \"params\", \"select\", \"seed\" or \"$apitest\" and run again; an entry without one only explains the cause; every run writes this anew"}
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

// suggestParams proposes values for the parameters without one: the
// fields of the selected records that may hold it, as "<DTO>.<field>", which
// "field" finds also where the path segment names no DTO.
func (rd *reader) suggestParams(code string, op *spec.Operation, problem string) {
	missing := rd.missing(op)
	if len(missing) == 0 {
		return
	}
	params := map[string]any{}
	var hints []string
	for _, name := range missing {
		cands := rd.candidates(op, name)
		if e, ok := rd.cfg.param(op.ID, name); ok && e.Field != "" {
			hints = append(hints, fmt.Sprintf("{%s}: no selected record has the field %q", name, e.Field))
		}
		if len(cands) == 0 {
			params[op.ID+"."+name] = "<a value the instance holds>"
			hints = append(hints, fmt.Sprintf("{%s}: no selected record has a field like it (selected: %s); set a fixed value", name, strings.Join(rd.selected(), ", ")))
			continue
		}
		params[op.ID+"."+name] = map[string]any{"field": cands[0]}
		if len(cands) > 1 {
			if len(cands) > 6 {
				cands = append(cands[:6], "…")
			}
			hints = append(hints, fmt.Sprintf("{%s}: other fields that may hold it: %s", name, strings.Join(cands[1:], ", ")))
		}
	}
	hint := fmt.Sprintf("no selected record has a field the run takes for {%s}; check the field proposed (\"<DTO>.<field>\"), put a value together ({\"format\": \"{Dto.field}-{field}\"}) or set a fixed value",
		strings.Join(missing, "}, {"))
	if len(hints) > 0 {
		hint += "; " + strings.Join(hints, "; ")
	}
	rd.res.suggest(code, op.ID, problem, hint, map[string]any{"params": params})
}

// candidates are the fields of the selected records that may hold a
// parameter, as "<DTO>.<field>": the same name in the DTO the path segment
// in front of it names first (/Ship/id/{id} → ShipRead.id), then the same
// name elsewhere (dockCode, or code of a dock), then names that contain it
// or that it contains.
func (rd *reader) candidates(op *spec.Operation, name string) []string {
	ln := strings.ToLower(name)
	seg := strings.ToLower(model.SegmentBefore(op.Path, name))
	var own, exact, near []string
	for _, key := range sortedKeys(rd.k.names) {
		t, lf, _ := strings.Cut(key, ".")
		c := rd.dto(t) + "." + rd.k.names[key]
		switch {
		case (lf == ln || t+lf == ln) && seg != "" && (t == seg || t == strings.TrimSuffix(seg, "s")):
			own = append(own, c)
		case lf == ln || t+lf == ln:
			exact = append(exact, c)
		case len(lf) > 1 && (strings.Contains(ln, lf) || strings.Contains(lf, ln)):
			near = append(near, c)
		}
	}
	return append(append(own, exact...), near...)
}

// selected names the DTOs of the selected records.
func (rd *reader) selected() []string {
	var out []string
	for _, t := range sortedKeys(rd.recs) {
		out = append(out, rd.dto(t))
	}
	if len(out) == 0 {
		return []string{"none"}
	}
	return out
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
		if _, has := rd.cfg.selection(t, rd.n).Details[op.Path]; has {
			rd.res.suggest(CodeNoData, op.ID, problem,
				fmt.Sprintf("\"select\" already requires that %s answers for the %s, but {%s} takes %s of the %s %s, and the instance answers %s for it: check whether the GET belongs to this %s or set {%s} in \"params\"",
					op.Path, dto, last, vals[last].field, dto, text(vals[last].v), rd.failed[op.ID], dto, last), nil)
			return
		}
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
// holds it before the test. The seed holds one record per DTO, the selected
// one; if the DTO is in the seed already, the data refer to another record
// (id): then "select" must take that one.
func (s *sim) suggestSeed(where, problem, t string, id any) {
	dto := s.rd.dto(t)
	if s.cfg.seeded(t, s.rd.n) {
		sel := "only one " + dto + ", the selected one"
		if r := s.rd.recs[t]; r != nil && r.id != nil {
			sel = fmt.Sprintf("only one %s, the selected one, id %s (%s)", dto, text(r.id), r.from)
		}
		if n := len(s.rd.seedRecs(t)); n > 1 {
			sel = fmt.Sprintf("%d of them (\"count\"), the first selected from %s", n, s.rd.recs[t].from)
		}
		more := ""
		if s.cfg.selection(t, s.rd.n).Count != All {
			more = fmt.Sprintf(", or let the seed hold more of them: \"select\": {\"%s\": {\"count\": \"*\"}}", dto)
		}
		if id == nil {
			s.res.suggest(CodeContainer, where, problem,
				fmt.Sprintf("%s is in the seed already, but the seed holds %s; the GET reads another one: let its parameters take the fields of the selected %s (\"params\")%s", dto, sel, dto, more), nil)
			return
		}
		s.res.suggest(CodeContainer, where, problem,
			fmt.Sprintf("%s is in the seed already, but the seed holds %s; this refers to id %s: let \"select\" take that %s (or let the parameters take the fields of the selected one)%s", dto, sel, text(id), dto, more),
			map[string]any{"select": map[string]any{dto: map[string]any{"equal": map[string]any{"id": id}}}})
		return
	}
	seed := slices.Clone(s.cfg.Seed)
	seed = append(seed, dto)
	s.res.suggest(CodeContainer, where, problem,
		fmt.Sprintf("the empty environment holds no %s at that point: put it into the seed (create it before the test)", dto),
		map[string]any{"seed": seed})
}

// suggestOrder proposes to run the write of a path before its GET. If the
// write runs before it already, it did not write this record: it was not
// sent, or for another record; another order does not help then.
func (s *sim) suggestOrder(where, problem string, get, write *spec.Operation) {
	if s.stepped[write.ID] {
		why := "it wrote another record"
		if x := s.w.byOp[write.ID]; x == nil || x.url == "" {
			why = "it has no value for a parameter (NO_DATA/NOT_EXECUTED of " + write.ID + ")"
		}
		s.res.suggest(CodeContainer, where, problem,
			fmt.Sprintf("%s runs before the GET already, but %s; fix that, the order is right", write.ID, why), nil)
		return
	}
	if write.Group() != get.Group() {
		s.res.suggest(CodeContainer, where, problem,
			fmt.Sprintf("%s belongs to the tag %s, which runs after the tag %s of the GET; MethodOrder orders only within a tag: put %s before %s in \"$apitest\".Tags (and in the Tags of the test)",
				write.ID, write.Group(), get.Group(), write.Group(), get.Group()), nil)
		return
	}
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
	if s.cfg.Run.DeleteLast {
		s.res.suggest(CodeContainer, where, problem,
			"DeleteLast is set already and a DELETE still runs before this GET: the GET belongs to a tag that runs after the DELETEs, e.g. a GET that needs a record of a later tag; put its tag earlier in \"$apitest\".Tags", nil)
		return
	}
	s.res.suggest(CodeContainer, where, problem, "a DELETE runs before this GET; run the DELETEs at the end",
		map[string]any{"$apitest": map[string]any{"DeleteLast": true}})
}

// suggestIgnore proposes fields for "$apitest".IgnoreFields, added to the
// ones set already.
func (r *Result) suggestIgnore(cfg *Config, code, where, problem, hint string, fields []string) {
	list := slices.Clone(cfg.Run.IgnoreFields)
	for _, f := range fields {
		if !ignores(list, f) {
			list = append(list, f)
		}
	}
	r.suggest(code, where, problem, hint, map[string]any{"$apitest": map[string]any{"IgnoreFields": list}})
}

// ignores reports whether IgnoreFields leaves a field out: by its name, or
// a JSON pointer that ends with it ("/items/*/updatedAt").
func ignores(list []string, field string) bool {
	for _, x := range list {
		if strings.EqualFold(x, field) {
			return true
		}
		if strings.HasPrefix(x, "/") {
			segs := strings.Split(x, "/")
			if strings.EqualFold(segs[len(segs)-1], field) {
				return true
			}
		}
	}
	return false
}
