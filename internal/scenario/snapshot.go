package scenario

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	yaml "github.com/oasdiff/yaml3"
)

// Kinds of snapshots.
const (
	KindGolden         = "golden"         // golden snapshot of a scenario
	KindRegressionPre  = "regression-pre" // a stage before a deployment
	KindRegressionPost = "regression-post"
)

// Snapshot is the answers of one run, step by step.
type Snapshot struct {
	Kind        string
	Spec        string
	SpecVersion string
	BaseURL     string
	Created     string // RFC 3339
	// Scenario is the scenario file a golden snapshot was made from.
	Scenario string
	// Pre is the pre snapshot a post snapshot replays.
	Pre string
	// Seed is the seed of the sample of a regression check.
	Seed string
	// Reread is set if the reading steps were sent twice to find the
	// fields that change by themselves.
	Reread bool
	// Sample is the data a regression check read, by set.
	Sample []SampleSet
	Steps  []*Shot
}

// SampleSet is the sample of one set of a regression check.
type SampleSet struct {
	Name       string
	Candidates int
	// LeftOut counts candidates a check left out, by reason.
	LeftOut map[string]int
	Picked  []map[string]any
}

// Shot is one request and its answer.
type Shot struct {
	ID string
	// Op is the operationId (or "METHOD /path") the request was sent to.
	Op string
	// Request is "METHOD /target" relative to the base URL.
	Request string
	Status  int
	// Headers are the headers that are compared.
	Headers map[string]string
	Body    any
	HasBody bool
	// Volatile lists what changes by itself: JSON pointers into the body
	// ("/updatedAt", "/items/*/updatedAt") and "header <Name>".
	Volatile []string
	// Lists names list keys by JSON pointer: the elements of the list at
	// "/items" are matched by the field "id" ("/items": "id").
	Lists      map[string]string
	DurationMs int64
	// Error is set if no answer came.
	Error string
	// Problems are the schema violations of the answer.
	Problems []string
}

// LoadSnapshot reads a snapshot file.
func LoadSnapshot(path string) (*Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := ParseSnapshot(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// ParseSnapshot reads a snapshot.
func ParseSnapshot(b []byte) (*Snapshot, error) {
	v, err := decodeYAML(b)
	if err != nil {
		return nil, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("a snapshot is a mapping with the keys snapshot and steps")
	}
	s := &Snapshot{}
	if meta, ok := top["snapshot"].(map[string]any); ok {
		s.Kind = Text(meta["kind"])
		s.Spec = Text(meta["spec"])
		s.SpecVersion = Text(meta["specVersion"])
		s.BaseURL = Text(meta["baseURL"])
		s.Created = Text(meta["created"])
		s.Scenario = Text(meta["scenario"])
		s.Pre = Text(meta["pre"])
		s.Seed = Text(meta["seed"])
		s.Reread, _ = meta["reread"].(bool)
	}
	if sample, ok := top["sample"].(map[string]any); ok {
		for _, name := range sortedKeys(sample) {
			m, _ := sample[name].(map[string]any)
			set := SampleSet{Name: name, LeftOut: map[string]int{}}
			set.Candidates = atoi(m["candidates"])
			if lo, ok := m["left out"].(map[string]any); ok {
				for k, n := range lo {
					set.LeftOut[k] = atoi(n)
				}
			}
			if picked, ok := m["picked"].([]any); ok {
				for _, p := range picked {
					if pm, ok := p.(map[string]any); ok {
						set.Picked = append(set.Picked, pm)
					}
				}
			}
			s.Sample = append(s.Sample, set)
		}
	}
	steps, _ := top["steps"].([]any)
	for i, raw := range steps {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("step %d: a step is a mapping", i+1)
		}
		sh := shotOf(Text(m["id"]), m)
		if sh.ID == "" {
			return nil, fmt.Errorf("step %d: \"id\" is missing", i+1)
		}
		s.Steps = append(s.Steps, sh)
	}
	return s, nil
}

// shotOf reads the answer of a step: status, headers, body and what is
// volatile.
func shotOf(id string, m map[string]any) *Shot {
	sh := &Shot{
		ID:         id,
		Op:         Text(m["op"]),
		Request:    Text(m["request"]),
		Status:     atoi(m["status"]),
		DurationMs: int64(atoi(m["durationMs"])),
		Error:      Text(m["error"]),
	}
	if h, ok := m["headers"].(map[string]any); ok {
		sh.Headers = map[string]string{}
		for k, x := range h {
			sh.Headers[k] = Text(x)
		}
	}
	if b, ok := m["body"]; ok {
		sh.Body, sh.HasBody = b, true
	}
	sh.Volatile = texts(m["volatile"])
	sh.Problems = texts(m["problems"])
	if l, ok := m["lists"].(map[string]any); ok {
		sh.Lists = map[string]string{}
		for k, x := range l {
			sh.Lists[k] = Text(x)
		}
	}
	return sh
}

func texts(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		out = append(out, Text(x))
	}
	return out
}

func atoi(v any) int {
	n, _ := strconv.Atoi(Text(v))
	return n
}

// Shot returns the step with the id, or nil.
func (s *Snapshot) Shot(id string) *Shot {
	for _, sh := range s.Steps {
		if sh.ID == id {
			return sh
		}
	}
	return nil
}

// Encode writes the snapshot; head heads the file as a comment.
func (s *Snapshot) Encode(head string) ([]byte, error) {
	opt := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	var reread any
	if s.Reread {
		reread = true
	}
	meta := mapping("kind", s.Kind, "spec", opt(s.Spec), "specVersion", opt(s.SpecVersion), "baseURL", opt(s.BaseURL),
		"created", opt(s.Created), "scenario", opt(s.Scenario), "pre", opt(s.Pre), "seed", opt(s.Seed), "reread", reread)
	root := mapping("snapshot", meta)
	if len(s.Sample) > 0 {
		sample := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, set := range s.Sample {
			var leftOut any
			if len(set.LeftOut) > 0 {
				lo := map[string]any{}
				for k, n := range set.LeftOut {
					lo[k] = n
				}
				leftOut = lo
			}
			picked := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			for _, p := range set.Picked {
				picked.Content = append(picked.Content, node(p))
			}
			sample.Content = append(sample.Content, str(set.Name), mapping("candidates", set.Candidates, "left out", leftOut, "picked", picked))
		}
		root.Content = append(root.Content, str("sample"), sample)
	}
	steps := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, sh := range s.Steps {
		steps.Content = append(steps.Content, shotNode(sh, true))
	}
	root.Content = append(root.Content, str("steps"), steps)
	return encode(head, root)
}

// shotNode writes an answer; withID for a snapshot file, which also keeps
// the operation and the duration. In a scenario file the step holds the id
// and the duration would change every run.
func shotNode(sh *Shot, withID bool) *yaml.Node {
	opt := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	var headers, body, volatile, lists, problems any
	if len(sh.Headers) > 0 {
		headers = sh.Headers
	}
	if sh.HasBody {
		body = sh.Body
		if body == nil {
			body = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
		}
	}
	if len(sh.Volatile) > 0 {
		volatile = sh.Volatile
	}
	if len(sh.Lists) > 0 {
		lists = sh.Lists
	}
	if len(sh.Problems) > 0 {
		problems = sh.Problems
	}
	var status any
	if sh.Status != 0 {
		status = sh.Status
	}
	if !withID {
		return mapping("request", sh.Request, "status", status, "headers", headers, "body", body,
			"volatile", volatile, "lists", lists, "problems", problems, "error", opt(sh.Error))
	}
	var dur any
	if sh.DurationMs > 0 {
		dur = sh.DurationMs
	}
	return mapping("id", sh.ID, "op", opt(sh.Op), "request", sh.Request, "status", status,
		"headers", headers, "body", body, "volatile", volatile, "lists", lists, "problems", problems,
		"durationMs", dur, "error", opt(sh.Error))
}

// Save writes the snapshot atomically.
func (s *Snapshot) Save(path, head string) error {
	b, err := s.Encode(head)
	if err != nil {
		return err
	}
	return WriteFile(path, b)
}

// WriteFile writes a file atomically: into a temporary file next to it,
// then renamed. Missing directories are created.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Templated replaces the values in v that equal a saved value by its
// placeholder, so a golden snapshot holds "{{book1.bookId}}" instead of
// the id one instance happened to give. A string of four or more
// characters is replaced wherever it is; a shorter value (a number like 1)
// only in a field whose name ends in "id", "code" or "key" or matches the
// name of the value.
func Templated(v any, vars map[string]any) any {
	type cand struct {
		name, text, suffix string
	}
	var cands []cand
	for _, name := range sortedKeys(vars) {
		t := Text(vars[name])
		if t == "" {
			continue
		}
		suffix := name
		if i := strings.LastIndex(name, "."); i >= 0 {
			suffix = name[i+1:]
		}
		cands = append(cands, cand{name, t, strings.ToLower(suffix)})
	}
	// longer values first: "12" must not win over "123"
	slices.SortStableFunc(cands, func(a, b cand) int { return len(b.text) - len(a.text) })
	var tmpl func(v any, field string) any
	tmpl = func(v any, field string) any {
		switch x := v.(type) {
		case map[string]any:
			m := make(map[string]any, len(x))
			for k, c := range x {
				m[k] = tmpl(c, k)
			}
			return m
		case []any:
			l := make([]any, len(x))
			for i, c := range x {
				l[i] = tmpl(c, field)
			}
			return l
		case string, json.Number:
			t := Text(x)
			f := strings.ToLower(field)
			keyish := strings.HasSuffix(f, "id") || strings.HasSuffix(f, "code") || strings.HasSuffix(f, "key")
			for _, c := range cands {
				if t != c.text {
					continue
				}
				_, isString := x.(string)
				if (isString && len(t) >= 4) || keyish || f == c.suffix {
					return "{{" + c.name + "}}"
				}
			}
		}
		return v
	}
	return tmpl(v, "")
}

// TemplatedHeader replaces a saved value in a header: the whole value, or
// the last path segment of a Location.
func TemplatedHeader(name, value string, vars map[string]any) string {
	for _, k := range sortedKeys(vars) {
		t := Text(vars[k])
		if t == "" {
			continue
		}
		if value == t {
			return "{{" + k + "}}"
		}
		if strings.EqualFold(name, "Location") {
			if i := strings.LastIndex(value, "/"); i >= 0 && value[i+1:] == t {
				return value[:i+1] + "{{" + k + "}}"
			}
		}
	}
	return value
}

// TemplatedTarget replaces saved values in the path and query of a
// request target: "/books/7?author=3" → "/books/{{book1.bookId}}?author=…".
func TemplatedTarget(target string, vars map[string]any) string {
	path, query, hasQuery := strings.Cut(target, "?")
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if seg != "" {
			segs[i] = segment(seg, vars)
		}
	}
	out := strings.Join(segs, "/")
	if !hasQuery {
		return out
	}
	pairs := strings.Split(query, "&")
	for i, p := range pairs {
		if k, v, ok := strings.Cut(p, "="); ok && v != "" {
			pairs[i] = k + "=" + segment(v, vars)
		}
	}
	return out + "?" + strings.Join(pairs, "&")
}

func segment(seg string, vars map[string]any) string {
	for _, k := range sortedKeys(vars) {
		if t := Text(vars[k]); t != "" && t == seg {
			return "{{" + k + "}}"
		}
	}
	return seg
}

// Generalize turns the array indexes of a JSON pointer into "*", so a
// volatile field is found in every element: "/items/3/updatedAt" →
// "/items/*/updatedAt".
func Generalize(ptr string) string {
	parts := strings.Split(ptr, "/")
	for i, p := range parts {
		if _, err := strconv.Atoi(p); err == nil && i > 0 {
			parts[i] = "*"
		}
	}
	return strings.Join(parts, "/")
}
