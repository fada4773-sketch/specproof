package tree

import (
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// opName is an operationId read as words: GetGardensByPersonName is the
// verb get, the subject gardens and the filter person name.
type opName struct {
	verb    string
	subject []string
	// by are the filters ("By PersonName And Season"), with what the
	// answer must have ("With Beds"). State words stay in the subject
	// ("Open" of GetOpenGardens).
	by, with [][]string
}

// words splits an identifier into lower-case words: camelCase, PascalCase,
// snake_case, kebab-case and digits ("getHTTPStatus2" → get http status 2).
func words(id string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	rs := []rune(id)
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
			continue
		case unicode.IsUpper(r) && len(cur) > 0:
			prevLower := unicode.IsLower(cur[len(cur)-1]) || unicode.IsDigit(cur[len(cur)-1])
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if prevLower || nextLower && unicode.IsUpper(cur[len(cur)-1]) {
				flush()
			}
		case unicode.IsDigit(r) && len(cur) > 0 && !unicode.IsDigit(cur[len(cur)-1]):
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// parseOpName reads an operationId. The first word is the verb; the words
// up to the first By, With, For, Of or In are the subject (and states);
// By, For, Of and In start filters, With what the answer must have, And
// separates filters.
func parseOpName(id string) opName {
	ws := words(id)
	var n opName
	if len(ws) == 0 {
		return n
	}
	n.verb, ws = ws[0], ws[1:]
	var cur *[][]string
	for _, w := range ws {
		switch w {
		case "by", "for", "of", "in":
			n.by = append(n.by, nil)
			cur = &n.by
			continue
		case "with":
			n.with = append(n.with, nil)
			cur = &n.with
			continue
		case "and":
			if cur != nil {
				*cur = append(*cur, nil)
			}
			continue
		}
		if cur == nil {
			n.subject = append(n.subject, w)
			continue
		}
		l := *cur
		l[len(l)-1] = append(l[len(l)-1], w)
	}
	// drop empty phrases ("ByAnd")
	clean := func(l [][]string) [][]string {
		return slices.DeleteFunc(l, func(p []string) bool { return len(p) == 0 })
	}
	n.by, n.with = clean(n.by), clean(n.with)
	return n
}

// hint is what a word phrase of an operationId says a parameter holds: a
// field of a resource (person name → Person.name).
type hint struct {
	res    *model.Resource
	field  string
	phrase string // the words as written, for notes ("PersonName")
}

// sameName compares a resource name with words, singular or plural.
func sameName(name string, ws []string) bool {
	j := strings.Join(ws, "")
	return strings.EqualFold(name, j) || strings.EqualFold(singular(name), singular(j))
}

// phrase finds the resource and field a phrase names: a resource and a
// field of it (person name → Person.name), a resource alone (person →
// Person and its key), else a field of the subject (season → Harvest.season).
func phrase(m *model.Model, subject *model.Resource, ws []string) (hint, bool) {
	text := pascal(ws)
	for i := len(ws); i >= 1; i-- {
		for _, r := range m.Resources {
			if !sameName(r.Name, ws[:i]) {
				continue
			}
			if i == len(ws) {
				if keys := keyPointers(r); len(keys) > 0 {
					return hint{res: r, field: PointerField(keys[0]), phrase: text}, true
				}
				continue
			}
			if f := r.Field(lowerFirst(pascal(ws[i:]))); f != "" {
				return hint{res: r, field: f, phrase: text}, true
			}
		}
	}
	if subject != nil {
		if f := subject.Field(lowerFirst(text)); f != "" {
			return hint{res: subject, field: f, phrase: text}, true
		}
	}
	return hint{}, false
}

func pascal(ws []string) string {
	var b strings.Builder
	for _, w := range ws {
		if w == "" {
			continue
		}
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

// pagingNames are query parameters that never take a filter value.
var pagingNames = []string{"page", "pagenumber", "pageindex", "offset", "skip", "size", "pagesize", "limit", "per_page", "perpage", "page_size", "sort", "order", "orderby", "fields", "expand"}

// hints maps the parameters of op to what its operationId says they hold:
// GetGardensByPersonName takes the person's name in "personName", "name"
// or, without such parameter, in its only free query parameter.
func (p *proposer) hints(op *spec.Operation) map[string]hint {
	if h, ok := p.hintCache[op]; ok {
		return h
	}
	out := map[string]hint{}
	if p.hintCache == nil {
		p.hintCache = map[*spec.Operation]map[string]hint{}
	}
	p.hintCache[op] = out
	if !op.HasOperationID {
		return out
	}
	n := parseOpName(op.ID)
	m := p.all
	if m == nil {
		m = p.m
	}
	subject := resourceOf(m, op)
	taken := map[string]bool{}
	var free []string
	for _, prm := range op.Params {
		if prm.In != openapi3.ParameterInPath && prm.In != openapi3.ParameterInQuery {
			continue
		}
		if prm.In == openapi3.ParameterInQuery && slices.Contains(pagingNames, strings.ToLower(prm.Name)) {
			continue
		}
		free = append(free, prm.Name)
	}
	for _, ws := range n.by {
		h, ok := phrase(m, subject, ws)
		if !ok {
			continue
		}
		var match string
		for _, name := range free {
			lname := strings.ToLower(name)
			if taken[name] {
				continue
			}
			// a generic name like id holds the field only below a word that
			// names no other record: /Dock/{id}/pilot/{pilotCode} of
			// GetSettingsByPilot is the id of a dock
			field := lname == strings.ToLower(h.field) && (!generic(name) || !p.namesOther(model.SegmentBefore(op.Path, name), h.res))
			if lname == strings.ToLower(pascal(ws)) || field ||
				lname == strings.ToLower(h.res.Name+h.field) || lname == strings.ToLower(singular(h.res.Name)+h.field) {
				match = name
				break
			}
		}
		if match == "" && len(n.by) == 1 {
			// the only free query parameter takes the only filter
			var left []string
			for _, name := range free {
				if !taken[name] && queryParam(op, name) {
					left = append(left, name)
				}
			}
			if len(left) == 1 {
				match = left[0]
			}
		}
		if match != "" {
			taken[match] = true
			out[match] = h
		}
	}
	return out
}

func queryParam(op *spec.Operation, name string) bool {
	for _, prm := range op.Params {
		if prm.Name == name && prm.In == openapi3.ParameterInQuery {
			return true
		}
	}
	return false
}

// needs are the values op needs: its path parameters, its required query
// parameters without documented value, and the query parameters its
// operationId names as filter (GetGardensByPersonName: name).
func (p *proposer) needs(op *spec.Operation) []need {
	out := needs(op)
	hs := p.hints(op)
	for _, prm := range op.Params {
		if _, ok := hs[prm.Name]; !ok || prm.In != openapi3.ParameterInQuery {
			continue
		}
		if !slices.ContainsFunc(out, func(n need) bool { return n.name == prm.Name }) {
			out = append(out, need{prm.Name, openapi3.ParameterInQuery})
		}
	}
	return out
}

// actionVerbs gives per verb of an action the states a record must be in
// for it; a verb not listed needs any state but the one it leads to.
var actionVerbs = map[string][]string{
	"open":     {"draft", "new", "closed", "created"},
	"publish":  {"draft", "new", "created", "unpublished"},
	"activate": {"inactive", "draft", "new", "suspended", "disabled"},
	"enable":   {"disabled", "inactive"},
	"disable":  {"enabled", "active"},
	"approve":  {"pending", "submitted", "review", "in_review"},
	"reject":   {"pending", "submitted", "review", "in_review"},
	"submit":   {"draft", "new"},
	"close":    {"open", "active", "in_progress"},
	"reopen":   {"closed", "done", "completed"},
	"complete": {"open", "in_progress", "active", "started"},
	"start":    {"new", "created", "planned", "open", "draft"},
	"cancel":   {"open", "new", "pending", "active", "created", "draft", "planned"},
	"suspend":  {"active", "enabled"},
	"resume":   {"suspended", "paused"},
	"pause":    {"active", "running", "started"},
	"archive":  {},
	"lock":     {},
	"unlock":   {"locked"},
}

// actionName is the name of an action: a POST, PUT or PATCH whose path
// ends in a word after a parameter (POST /gardens/{gardenId}/open) that is
// no list of records (no GET at that path), or whose operationId starts with
// a verb of an action and names a record by a path parameter.
func actionName(s *spec.Spec, op *spec.Operation) string {
	switch op.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return ""
	}
	segs := strings.Split(strings.Trim(op.Path, "/"), "/")
	if len(segs) >= 2 {
		last, before := segs[len(segs)-1], segs[len(segs)-2]
		if !strings.HasPrefix(last, "{") && strings.HasPrefix(before, "{") {
			for _, o := range s.Ops {
				if o.Path == op.Path && o.Method == http.MethodGet {
					return ""
				}
			}
			if ws := words(last); len(ws) > 0 {
				return lowerFirst(pascal(ws))
			}
		}
	}
	if op.HasOperationID && len(pathParams(op)) > 0 {
		if ws := words(op.ID); len(ws) > 0 {
			if _, ok := actionVerbs[ws[0]]; ok {
				return ws[0]
			}
		}
	}
	return ""
}

// enumValues are the values of an enum field of a resource as text.
func enumValues(r *model.Resource, field string) []string {
	ref := r.Schema(field)
	if ref == nil || ref.Value == nil {
		return nil
	}
	var out []string
	for _, v := range ref.Value.Enum {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// stateField is the enum field of a resource that holds its state: one
// named status, state or phase, else the only enum field.
func stateField(r *model.Resource) string {
	var enums []string
	for _, f := range r.FieldNames() {
		if len(enumValues(r, f)) > 0 {
			enums = append(enums, f)
		}
	}
	for _, f := range enums {
		switch strings.ToLower(f) {
		case "status", "state", "phase", "lifecycle":
			return f
		}
	}
	if len(enums) == 1 {
		return enums[0]
	}
	return ""
}

// actionStates are the values of the state field a record must have for
// an action: those the verb starts from, else all but the state it leads
// to (archive → all but ARCHIVED). nil when the field or verb tells
// nothing.
func actionStates(values []string, verb string) []string {
	from, known := actionVerbs[verb]
	var out []string
	if known && len(from) > 0 {
		for _, v := range values {
			if slices.Contains(from, strings.ToLower(v)) {
				out = append(out, v)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	// all but the state the verb leads to (archive → ARCHIVED)
	done := ""
	for _, v := range values {
		lv := strings.ToLower(v)
		if strings.HasPrefix(lv, verb) && lv != verb || lv == verb+"d" || lv == verb+"ed" || lv == verb+"led" {
			done = v
		}
	}
	if done == "" {
		return nil
	}
	for _, v := range values {
		if v != done {
			out = append(out, v)
		}
	}
	return out
}

// states are the values of an enum field the words of an operationId name
// (GetOpenGardens: OPEN of status).
func states(r *model.Resource, ws []string) (string, []string) {
	f := stateField(r)
	if f == "" {
		return "", nil
	}
	var out []string
	for _, v := range enumValues(r, f) {
		for _, w := range ws {
			if strings.EqualFold(strings.ReplaceAll(v, "_", ""), w) {
				out = append(out, v)
			}
		}
	}
	return f, out
}
