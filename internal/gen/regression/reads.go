package regression

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Plan turns the sample into the reads of a regression check: every GET of
// the spec, once per record of the set that gives its path parameters, or
// once if it has none. notes lists the GETs that are not read and why.
func Plan(s *spec.Spec, sample *Sample, picked *Picked) (*scenario.Scenario, []string) {
	byName := map[string]scenario.SampleSet{}
	for _, ss := range picked.Sets {
		byName[ss.Name] = ss
	}
	sc := &scenario.Scenario{}
	var notes []string
	seen := map[string]bool{}
	ids := map[string]int{}
	for _, op := range s.Ops {
		if op.Method != http.MethodGet {
			continue
		}
		req := "GET " + op.Path
		if slices.Contains(sample.Exclude, req) || slices.Contains(sample.Exclude, op.ID) {
			continue
		}
		var pathParams []string
		for _, p := range op.Params {
			if p.In == openapi3.ParameterInPath {
				pathParams = append(pathParams, p.Name)
			}
		}
		query := sample.Reads[req]
		set := ""
		if len(pathParams) > 0 {
			set = setFor(sample, pathParams)
			if set == "" {
				notes = append(notes, fmt.Sprintf("%s: no set gives {%s}", req, strings.Join(pathParams, "}, {")))
				continue
			}
		} else if names := scenario.Names(query); len(names) > 0 {
			set, _, _ = strings.Cut(names[0], ".")
		}
		var elems []map[string]any
		if set != "" {
			elems = byName[set].Picked
			if len(elems) == 0 {
				notes = append(notes, fmt.Sprintf("%s: the set %s has no record on this stage", req, set))
				continue
			}
		} else {
			elems = []map[string]any{nil}
		}
		for _, el := range elems {
			vars := map[string]any{}
			for _, ss := range picked.Sets {
				if len(ss.Picked) > 0 {
					for k, v := range ss.Picked[0] {
						vars[ss.Name+"."+k] = v
					}
				}
			}
			for k, v := range el {
				vars[set+"."+k] = v
			}
			st := &scenario.Step{Call: op.Method + " " + op.Path, Expect: success(op)}
			var key []string
			for _, name := range pathParams {
				if st.Path == nil {
					st.Path = map[string]any{}
				}
				st.Path[name] = el[name]
				key = append(key, scenario.Text(el[name]))
			}
			if q, missing := scenario.Fill(anyMap(query), vars); len(missing) == 0 && q != nil {
				st.Query, _ = q.(map[string]any)
				for _, k := range sortedKeys(st.Query) {
					key = append(key, scenario.Text(st.Query[k]))
				}
			} else if len(missing) > 0 {
				notes = append(notes, fmt.Sprintf("%s: reads names {{%s}}, which no set gives", req, strings.Join(missing, "}}, {{")))
				continue
			}
			if why := requiredQueryValues(op, st); why != "" {
				notes = append(notes, fmt.Sprintf("%s: %s", req, why))
				break
			}
			target := op.Path + "?" + encodeQuery(st.Path) + "&" + encodeQuery(st.Query)
			if seen[target] {
				continue
			}
			seen[target] = true
			id := sanitize(op.ID)
			if len(key) > 0 {
				id += ":" + sanitize(strings.Join(key, "-"))
			}
			ids[id]++
			if ids[id] > 1 {
				id += "." + strconv.Itoa(ids[id])
			}
			st.ID = id
			sc.Steps = append(sc.Steps, st)
		}
	}
	return sc, notes
}

// setFor is the set whose values give all path parameters.
func setFor(sample *Sample, params []string) string {
	for _, set := range sample.Sets {
		all := true
		for _, p := range params {
			if _, ok := set.As[p]; !ok {
				all = false
			}
		}
		if all {
			return set.Name
		}
	}
	return ""
}

// requiredQueryValues gives the required query parameters without value
// the example, default or first enum value of the spec.
func requiredQueryValues(op *spec.Operation, st *scenario.Step) string {
	for _, p := range op.Params {
		if p.In != openapi3.ParameterInQuery || !p.Required {
			continue
		}
		if _, ok := st.Query[p.Name]; ok {
			continue
		}
		var v any
		switch {
		case p.Example != nil:
			v = spec.Normalize(p.Example)
		case p.Schema != nil && p.Schema.Value != nil && p.Schema.Value.Default != nil:
			v = spec.Normalize(p.Schema.Value.Default)
		case p.Schema != nil && p.Schema.Value != nil && len(p.Schema.Value.Enum) > 0:
			v = spec.Normalize(p.Schema.Value.Enum[0])
		default:
			return fmt.Sprintf("no value for the required query parameter %q; set it in reads", p.Name)
		}
		if st.Query == nil {
			st.Query = map[string]any{}
		}
		st.Query[p.Name] = v
	}
	return ""
}

func success(op *spec.Operation) int {
	best := 0
	if op.Op.Responses != nil {
		for code := range op.Op.Responses.Map() {
			if n, err := strconv.Atoi(code); err == nil && n/100 == 2 && (best == 0 || n < best) {
				best = n
			}
		}
	}
	if best == 0 {
		return http.StatusOK
	}
	return best
}

var idChars = regexp.MustCompile(`[^A-Za-z0-9_.:-]+`)

// sanitize makes a text usable in a step id.
func sanitize(s string) string {
	s = strings.Trim(idChars.ReplaceAllString(s, "_"), "_")
	if s == "" {
		return "_"
	}
	return s
}

var paramRe = regexp.MustCompile(`\{[^}]+\}`)

func pathEscape(s string) string { return url.PathEscape(s) }

func encodeQuery(m map[string]any) string {
	v := url.Values{}
	for _, k := range sortedKeys(m) {
		v.Set(k, scenario.Text(m[k]))
	}
	return v.Encode()
}

func anyMap(m map[string]any) any {
	if m == nil {
		return nil
	}
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// listOf is the list in an answer: the answer itself, or its one field
// that is a list.
func listOf(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any:
		var found []any
		n := 0
		for _, c := range x {
			if l, ok := c.([]any); ok {
				found = l
				n++
			}
		}
		if n == 1 {
			return found
		}
	}
	return nil
}
