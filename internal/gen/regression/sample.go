// Package regression checks that a deployment does not change what a
// stage answers. Before the deployment it draws a sample of real records
// of the stage by the rules of the sample file (GET requests only), reads
// every GET of the spec with them and keeps the answers as the pre
// snapshot; after the deployment it reads the same requests again and
// compares. Nothing is ever written to the stage.
package regression

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/gen/discover"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Sample is the sample file (regression-sample.yaml): which real data a
// regression check reads from a stage.
type Sample struct {
	// Seed is "random" (a new sample every pre run) or a number.
	Seed string
	Sets []*Set
	// Exclude are GETs that are not read ("GET /health").
	Exclude []string
	// Reads set query values of single GETs, by "GET /path".
	Reads map[string]map[string]any
	// Ignore are fields never compared between pre and post.
	Ignore []string
	// ListKey is the field list elements are matched by.
	ListKey string
	// Probe is the time between the two reads of each GET; 0 reads once.
	Probe time.Duration
}

// Set is one kind of record the sample takes.
type Set struct {
	Name string
	// From is the list GET the candidates come from.
	From  string
	Query map[string]any
	// Pages is how many pages to read when the list is paged.
	Pages int
	// Where keeps candidates: a field "present", a value, or a comparison
	// (">=1950").
	Where map[string]any
	// Check are GETs that must answer 2xx for a candidate; their path
	// parameters take the values of As.
	Check []string
	// Pick is how many candidates are chosen at random.
	Pick int
	// As are the values a candidate gives, by name: JSON pointers into it.
	As map[string]string
}

var sampleKeys = []string{"seed", "sets", "reads", "ignore", "lists", "probe"}
var setKeys = []string{"from", "query", "pages", "where", "check", "pick", "as"}

// LoadSample reads a sample file. Every request in it must be a GET.
func LoadSample(path string) (*Sample, error) {
	v, err := scenario.ReadYAML(path)
	if err != nil {
		return nil, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: the sample file is a mapping (%s)", path, strings.Join(sampleKeys, ", "))
	}
	where := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...))
	}
	for k := range top {
		if !slices.Contains(sampleKeys, k) {
			return nil, where("unknown key %q (known: %s)", k, strings.Join(sampleKeys, ", "))
		}
	}
	s := &Sample{Seed: scenario.Text(top["seed"]), ListKey: "id", Probe: 2 * time.Second, Reads: map[string]map[string]any{}}
	if s.Seed == "" {
		s.Seed = "random"
	}
	if s.Seed != "random" {
		if _, err := strconv.ParseUint(s.Seed, 10, 64); err != nil {
			return nil, where("seed must be random or a number")
		}
	}
	if p := scenario.Text(top["probe"]); p != "" {
		d, err := time.ParseDuration(p)
		if err != nil {
			return nil, where("probe: %v (e.g. 2s, or 0 to read once)", err)
		}
		s.Probe = d
	}
	if l, ok := top["lists"].(map[string]any); ok && scenario.Text(l["key"]) != "" {
		s.ListKey = scenario.Text(l["key"])
	}
	for _, x := range asList(top["ignore"]) {
		s.Ignore = append(s.Ignore, scenario.Text(x))
	}
	if r, ok := top["reads"].(map[string]any); ok {
		for k, x := range r {
			if k == "exclude" {
				for _, e := range asList(x) {
					s.Exclude = append(s.Exclude, normalizeRequest(scenario.Text(e)))
				}
				continue
			}
			req := normalizeRequest(k)
			if !strings.HasPrefix(req, "GET ") {
				return nil, where("reads: %q is no GET; a regression check only reads", k)
			}
			m, _ := x.(map[string]any)
			q, _ := m["query"].(map[string]any)
			s.Reads[req] = q
		}
	}
	sets, _ := top["sets"].(map[string]any)
	for _, name := range sortedKeys(sets) {
		m, ok := sets[name].(map[string]any)
		if !ok {
			return nil, where("sets.%s must be a mapping (%s)", name, strings.Join(setKeys, ", "))
		}
		for k := range m {
			if !slices.Contains(setKeys, k) {
				return nil, where("sets.%s: unknown key %q (known: %s)", name, k, strings.Join(setKeys, ", "))
			}
		}
		set := &Set{Name: name, From: normalizeRequest(scenario.Text(m["from"])), Pick: 5, Pages: 1, As: map[string]string{}}
		if !strings.HasPrefix(set.From, "GET ") {
			return nil, where("sets.%s.from %q: a set reads its candidates with a GET", name, set.From)
		}
		set.Query, _ = m["query"].(map[string]any)
		set.Where, _ = m["where"].(map[string]any)
		if n := scenario.Text(m["pick"]); n != "" {
			if set.Pick, err = strconv.Atoi(n); err != nil || set.Pick < 1 {
				return nil, where("sets.%s.pick must be a number from 1", name)
			}
		}
		if n := scenario.Text(m["pages"]); n != "" {
			if set.Pages, err = strconv.Atoi(n); err != nil || set.Pages < 1 {
				return nil, where("sets.%s.pages must be a number from 1", name)
			}
		}
		for _, c := range asList(m["check"]) {
			req := normalizeRequest(scenario.Text(c))
			if !strings.HasPrefix(req, "GET ") {
				return nil, where("sets.%s.check %q: a check reads with a GET", name, req)
			}
			set.Check = append(set.Check, req)
		}
		as, _ := m["as"].(map[string]any)
		for k, x := range as {
			p := scenario.Text(x)
			if !strings.HasPrefix(p, "/") {
				return nil, where("sets.%s.as.%s: %q is no JSON pointer", name, k, p)
			}
			set.As[k] = p
		}
		if len(set.As) == 0 {
			return nil, where("sets.%s needs \"as\": the values a record gives, e.g. {bookId: /id}", name)
		}
		s.Sets = append(s.Sets, set)
	}
	return s, nil
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// normalizeRequest writes "get /books" as "GET /books".
func normalizeRequest(r string) string {
	if m, p, ok := strings.Cut(strings.TrimSpace(r), " "); ok {
		return strings.ToUpper(m) + " " + strings.TrimSpace(p)
	}
	return strings.TrimSpace(r)
}

// Propose writes the sets the spec suggests: one per resource with a list
// GET, with the ids of its records, the references that must exist and
// the GETs that must answer for a record. Sets of an existing file stay.
func Propose(s *spec.Spec, old *Sample) *Sample {
	out := &Sample{Seed: "random", ListKey: "id", Probe: 2 * time.Second, Reads: map[string]map[string]any{}}
	if old != nil {
		*out = *old
		out.Sets = slices.Clone(old.Sets)
	}
	m := model.Detect(s, nil)
	for _, r := range m.Resources {
		name := lowerFirst(r.Name)
		if slices.ContainsFunc(out.Sets, func(set *Set) bool { return set.Name == name }) {
			continue
		}
		var list *spec.Operation
		for _, mo := range r.OpsWith(model.RoleList) {
			if !hasPathParam(mo.Op) && !requiredQuery(mo.Op) {
				list = mo.Op
				break
			}
		}
		if list == nil {
			continue
		}
		set := &Set{Name: name, From: "GET " + list.Path, Pick: 5, Pages: 1, As: map[string]string{}, Where: map[string]any{}}
		for _, p := range list.Params {
			if p.In != openapi3.ParameterInQuery {
				continue
			}
			switch strings.ToLower(p.Name) {
			case "pagesize", "size", "limit", "per_page", "perpage", "page_size":
				n := 100.0
				if p.Schema != nil && p.Schema.Value != nil && p.Schema.Value.Max != nil && *p.Schema.Value.Max < 500 {
					n = *p.Schema.Value.Max
				}
				set.Query = map[string]any{p.Name: int(n)}
			case "page", "pagenumber", "pageindex":
				set.Pages = 3
			}
		}
		key := ""
		if len(r.Keys) > 0 {
			key = r.Keys[0]
		}
		for _, mo := range r.OpsWith(model.RoleRead) {
			for _, p := range mo.Params {
				if p.Resource == r && p.Field != "" {
					set.As[p.Name] = "/" + p.Field
				}
			}
		}
		if len(set.As) == 0 && key != "" {
			set.As[lowerFirst(r.Name)+strings.ToUpper(key[:1])+key[1:]] = "/" + key
		}
		for _, f := range r.Relations {
			if t, _ := r.Ref(f); t != nil {
				set.As[f] = "/" + f
				set.Where[f] = "present"
			}
		}
		if len(set.As) == 0 {
			continue
		}
		// GETs of other resources and sub-resources that take these values
		for _, op := range s.Ops {
			if op.Method != http.MethodGet || !hasPathParam(op) {
				continue
			}
			if mo := m.Op(op); mo != nil && mo.Resource == r && mo.Role == model.RoleRead {
				continue
			}
			all := true
			for _, p := range op.Params {
				if p.In == openapi3.ParameterInPath && set.As[p.Name] == "" {
					all = false
				}
			}
			if all {
				set.Check = append(set.Check, "GET "+op.Path)
			}
		}
		out.Sets = append(out.Sets, set)
	}
	return out
}

// Encode writes the sample file.
func (s *Sample) Encode() ([]byte, error) {
	sets := map[string]any{}
	for _, set := range s.Sets {
		m := map[string]any{"from": set.From, "pick": set.Pick, "as": set.As}
		if len(set.Query) > 0 {
			m["query"] = set.Query
		}
		if set.Pages > 1 {
			m["pages"] = set.Pages
		}
		if len(set.Where) > 0 {
			m["where"] = set.Where
		}
		if len(set.Check) > 0 {
			m["check"] = set.Check
		}
		sets[set.Name] = m
	}
	reads := map[string]any{}
	if len(s.Exclude) > 0 {
		reads["exclude"] = s.Exclude
	}
	for k, q := range s.Reads {
		reads[k] = map[string]any{"query": q}
	}
	top := map[string]any{"seed": s.Seed, "sets": sets, "lists": map[string]any{"key": s.ListKey}, "probe": s.Probe.String()}
	if len(reads) > 0 {
		top["reads"] = reads
	}
	if len(s.Ignore) > 0 {
		top["ignore"] = s.Ignore
	}
	return scenario.EncodeValue(sampleHead, top)
}

const sampleHead = ` What "apitest-gen regression" reads from a stage. GET requests only.
 Proposed by "apitest-gen regression init"; yours from then on.
   seed:  random (a new sample every pre run) or a number (repeat one)
   sets:  per kind of record: from (the list GET), query, pages, where
          (present, a value, ">=1950"), check (GETs that must answer 2xx
          for a record), pick (how many, at random), as (the values a
          record gives: {bookId: /id})
   reads: exclude (GETs not read), "GET /path": {query: {authorId: '{{book.authorId}}'}}
   ignore: fields never compared; lists.key: list elements are matched by it
   probe: time between the two reads of each GET (0 reads once)`

// Picked is the drawn sample: per set the values of the chosen records.
type Picked struct {
	Seed string
	Sets []scenario.SampleSet
}

// Draw reads the candidates of every set from the stage (GET only),
// keeps those that pass where and check, and picks at random.
func Draw(ctx context.Context, s *Sample, opt discover.Options) (*Picked, error) {
	seed := s.Seed
	if seed == "random" || seed == "" {
		seed = strconv.FormatUint(rand.Uint64()%1000000, 10)
	}
	n, err := strconv.ParseUint(seed, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("seed %q is no number", seed)
	}
	rnd := rand.New(rand.NewPCG(n, 7))
	out := &Picked{Seed: seed}
	for _, set := range s.Sets {
		ss, err := draw(ctx, set, opt, rnd)
		if err != nil {
			return nil, fmt.Errorf("set %s: %w", set.Name, err)
		}
		out.Sets = append(out.Sets, ss)
	}
	return out, nil
}

func draw(ctx context.Context, set *Set, opt discover.Options, rnd *rand.Rand) (scenario.SampleSet, error) {
	ss := scenario.SampleSet{Name: set.Name, LeftOut: map[string]int{}}
	path := strings.TrimPrefix(set.From, "GET ")
	var candidates []map[string]any
	for page := 1; page <= set.Pages; page++ {
		target := path
		q := map[string]any{}
		for k, v := range set.Query {
			q[k] = v
		}
		if set.Pages > 1 {
			q["page"] = page
		}
		if len(q) > 0 {
			target += "?" + encodeQuery(q)
		}
		v, err := discover.Get(ctx, opt, target)
		if err != nil {
			if page == 1 {
				return ss, fmt.Errorf("GET %s: %w", target, err)
			}
			break
		}
		list := listOf(v)
		if len(list) == 0 {
			break
		}
		for _, el := range list {
			if m, ok := el.(map[string]any); ok {
				candidates = append(candidates, m)
			}
		}
	}
	ss.Candidates = len(candidates)
	rnd.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	for _, c := range candidates {
		if len(ss.Picked) == set.Pick {
			break
		}
		if why := where(c, set.Where); why != "" {
			ss.LeftOut[why]++
			continue
		}
		values := map[string]any{}
		missing := ""
		for _, name := range sortedKeys(set.As) {
			v, ok := bind.Pointer(c, set.As[name])
			if !ok || v == nil {
				missing = name
				break
			}
			values[name] = v
		}
		if missing != "" {
			ss.LeftOut["no "+missing]++
			continue
		}
		failed := ""
		for _, chk := range set.Check {
			target, ok := fillPath(strings.TrimPrefix(chk, "GET "), values)
			if !ok {
				failed = chk + " (a value is missing)"
				break
			}
			if _, err := discover.Get(ctx, opt, target); err != nil {
				failed = chk
				break
			}
		}
		if failed != "" {
			ss.LeftOut[failed+" failed"]++
			continue
		}
		ss.Picked = append(ss.Picked, values)
	}
	return ss, nil
}

// where explains why a candidate does not pass the conditions, or "".
func where(c map[string]any, conds map[string]any) string {
	for _, f := range sortedKeys(conds) {
		want := scenario.Text(conds[f])
		v, ok := c[f]
		if strings.HasPrefix(f, "/") {
			v, ok = bind.Pointer(c, f)
		}
		switch {
		case want == "present":
			if !ok || v == nil {
				return "without " + f
			}
		case strings.HasPrefix(want, ">=") || strings.HasPrefix(want, "<=") || strings.HasPrefix(want, ">") || strings.HasPrefix(want, "<"):
			op := strings.TrimRight(want[:2], "0123456789.-")
			limit, err := strconv.ParseFloat(strings.TrimSpace(want[len(op):]), 64)
			n, err2 := strconv.ParseFloat(scenario.Text(v), 64)
			if err != nil || err2 != nil || !ok {
				return f + " not " + want
			}
			pass := map[string]bool{">=": n >= limit, "<=": n <= limit, ">": n > limit, "<": n < limit}[op]
			if !pass {
				return f + " not " + want
			}
		default:
			if !ok || scenario.Text(v) != want {
				return f + " not " + want
			}
		}
	}
	return ""
}

// fillPath fills the {parameters} of a path with values by name.
func fillPath(path string, values map[string]any) (string, bool) {
	ok := true
	out := paramRe.ReplaceAllStringFunc(path, func(m string) string {
		name := m[1 : len(m)-1]
		v, found := values[name]
		if !found {
			ok = false
			return m
		}
		return pathEscape(scenario.Text(v))
	})
	return out, ok
}

func hasPathParam(op *spec.Operation) bool {
	return slices.ContainsFunc(op.Params, func(p *openapi3.Parameter) bool { return p.In == openapi3.ParameterInPath })
}

func requiredQuery(op *spec.Operation) bool {
	return slices.ContainsFunc(op.Params, func(p *openapi3.Parameter) bool { return p.In == openapi3.ParameterInQuery && p.Required })
}

// ErrNoSets is returned when the sample file has no set.
var ErrNoSets = errors.New("the sample file has no sets; propose them with apitest-gen regression init")

// Save writes the sample file.
func (s *Sample) Save(path string) error {
	b, err := s.Encode()
	if err != nil {
		return err
	}
	return scenario.WriteFile(path, b)
}

// Exists reports whether a file exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
