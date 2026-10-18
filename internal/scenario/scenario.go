package scenario

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	yaml "github.com/oasdiff/yaml3"
)

// Tokens a step can send instead of the default token.
const (
	TokenDefault   = ""          // the token of the run
	TokenNone      = "none"      // no token at all: the 401 case
	TokenInvalid   = "invalid"   // a manipulated copy of the token
	TokenForbidden = "forbidden" // a valid token with too few rights: the 403 case
)

// Scenario is the list of steps a scenario run sends, in this order.
type Scenario struct {
	// Order is the order of the resources (tags), for the reader.
	Order []string
	// Records are the records the scenario creates, for the reader.
	Records []Record
	Steps   []*Step
	// Golden are the answers of the last run with Update when the file
	// holds them (the golden section and an answer per step), else nil.
	Golden *Snapshot
}

// Record is one record the scenario creates.
type Record struct {
	Name     string
	Resource string
	// Refs names the records it refers to, by field.
	Refs map[string]string
}

// Step is one request and what its answer must be.
type Step struct {
	// ID names the step: "<record>.<action>[.<variant>]", e.g.
	// "book1.update.stale". The subtest is "<tag>/<id>".
	ID string
	// Call is the operation: "METHOD /path" or the operationId.
	Call string
	// Path, Query and Header are parameter values; they may hold
	// placeholders ("{{book1.bookId}}").
	Path, Query, Header map[string]any
	// Body is the request body; HasBody is set if the step has one.
	Body    any
	HasBody bool
	// BodyFrom sends the body an earlier step sent (after its placeholders
	// were filled in).
	BodyFrom string
	// Save keeps values for later steps: name → source. A source is a JSON
	// pointer into the answer ("/id"; a part "[code={{ship1.code}}]" finds
	// the element of a list with these values, see Lookup), "header <Name>"
	// ("header Location" gives its last path segment), "request /pointer",
	// "request path <name>" or "request query <name>".
	Save map[string]string
	// Token is TokenDefault, TokenNone, TokenInvalid or TokenForbidden.
	Token string
	// Expect is the status the answer must have.
	Expect int
	// Equals names a step whose body the answer must hold (read after
	// create or update).
	Equals string
	// Contains lists records a list answer must hold: the elements whose
	// field Key (a JSON pointer into the element) has one of the values.
	Contains *Contains
	// Unique are JSON pointers into the body whose values a run with
	// Unique makes new, so the step works on an instance that is not empty.
	Unique []string

	// Why explains the step; written as a comment.
	Why string
	// Notes explain single values, by JSON pointer into the step
	// ("/body/title"); written as comments.
	Notes map[string]string
	// Line is the line of the step in the file, 0 for a generated one.
	Line int
}

// Contains is the check of a list answer.
type Contains struct {
	Key    string
	Values []any
}

var stepKeys = []string{"id", "call", "path", "query", "header", "body", "bodyFrom", "save", "token", "expect", "check", "unique", "answer"}

// sourceForm is the form of a save source.
var sourceForm = regexp.MustCompile(`^(/.*|header \S+|request /.*|request (path|query) \S+)$`)

// idForm is the form of a step id: no "/" (it is part of a test name) and
// no spaces.
var idForm = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:-]*$`)

// Load reads a scenario file.
func Load(path string) (*Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sc, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return sc, nil
}

// Parse reads a scenario and checks it: unique ids, known keys, sources of
// the right form, and every placeholder saved by a step above it.
func Parse(b []byte) (*Scenario, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	v, err := plain(&doc)
	if err != nil {
		return nil, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("a scenario is a mapping with the key \"steps\"")
	}
	for k := range top {
		if !slices.Contains([]string{"order", "records", "golden", "steps"}, k) {
			return nil, fmt.Errorf("unknown key %q (known: order, records, golden, steps)", k)
		}
	}
	sc := &Scenario{}
	if o, ok := top["order"].([]any); ok {
		for _, x := range o {
			sc.Order = append(sc.Order, Text(x))
		}
	}
	if recs, ok := top["records"].(map[string]any); ok {
		for _, name := range sortedKeys(recs) {
			r := Record{Name: name}
			if m, ok := recs[name].(map[string]any); ok {
				r.Resource = Text(m["resource"])
				if refs, ok := m["refs"].(map[string]any); ok {
					r.Refs = map[string]string{}
					for k, x := range refs {
						r.Refs[k] = Text(x)
					}
				}
			}
			sc.Records = append(sc.Records, r)
		}
	}
	list, ok := top["steps"].([]any)
	if !ok {
		return nil, fmt.Errorf("\"steps\" must be a list")
	}
	lines := stepLines(&doc)
	for i, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("step %d: a step is a mapping", i+1)
		}
		st, err := ParseStep(m)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", i+1, err)
		}
		if i < len(lines) {
			st.Line = lines[i]
		}
		sc.Steps = append(sc.Steps, st)
	}
	sc.Golden = goldenOf(top, list)
	if err := sc.Check(); err != nil {
		return nil, err
	}
	return sc, nil
}

// stepLines returns the line of each step of the document.
func stepLines(doc *yaml.Node) []int {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "steps" {
			var out []int
			for _, c := range root.Content[i+1].Content {
				out = append(out, c.Line)
			}
			return out
		}
	}
	return nil
}

// ParseStep reads one step from its mapping.
func ParseStep(m map[string]any) (*Step, error) {
	st := &Step{}
	for k := range m {
		if !slices.Contains(stepKeys, k) {
			return nil, fmt.Errorf("unknown key %q (known: %s)", k, strings.Join(stepKeys, ", "))
		}
	}
	st.ID = Text(m["id"])
	if st.ID == "" {
		return nil, fmt.Errorf("\"id\" is missing")
	}
	if !idForm.MatchString(st.ID) {
		return nil, fmt.Errorf("id %q: letters, digits and . _ - : only", st.ID)
	}
	where := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s", st.ID, fmt.Sprintf(format, args...))
	}
	st.Call = strings.TrimSpace(Text(m["call"]))
	if st.Call == "" {
		return nil, where("\"call\" is missing: \"METHOD /path\" or an operationId")
	}
	for _, x := range []struct {
		key string
		dst *map[string]any
	}{{"path", &st.Path}, {"query", &st.Query}, {"header", &st.Header}} {
		raw, ok := m[x.key]
		if !ok || raw == nil {
			continue
		}
		mm, ok := raw.(map[string]any)
		if !ok {
			return nil, where("%q must be a mapping of parameter names to values", x.key)
		}
		*x.dst = mm
	}
	if b, ok := m["body"]; ok {
		st.Body, st.HasBody = b, true
	}
	st.BodyFrom = Text(m["bodyFrom"])
	if st.HasBody && st.BodyFrom != "" {
		return nil, where("\"body\" and \"bodyFrom\" exclude each other")
	}
	if raw, ok := m["save"]; ok && raw != nil {
		mm, ok := raw.(map[string]any)
		if !ok {
			return nil, where("\"save\" must be a mapping of names to sources")
		}
		st.Save = map[string]string{}
		for name, src := range mm {
			s := Text(src)
			if !sourceForm.MatchString(s) {
				return nil, where("save %s: %q is no source; write a JSON pointer into the answer (/id), \"header <Name>\", \"request /pointer\", \"request path <name>\" or \"request query <name>\"", name, s)
			}
			st.Save[name] = s
		}
	}
	st.Token = Text(m["token"])
	if st.Token == "default" {
		st.Token = TokenDefault
	}
	if !slices.Contains([]string{TokenDefault, TokenNone, TokenInvalid, TokenForbidden}, st.Token) {
		return nil, where("token %q: use none, invalid or forbidden (or leave it out)", st.Token)
	}
	exp, err := strconv.Atoi(Text(m["expect"]))
	if err != nil || exp < 100 || exp > 599 {
		return nil, where("\"expect\" must be an HTTP status, e.g. 201")
	}
	st.Expect = exp
	if raw, ok := m["check"]; ok && raw != nil {
		cm, ok := raw.(map[string]any)
		if !ok {
			return nil, where("\"check\" must be a mapping (equals, contains)")
		}
		for k := range cm {
			if k != "equals" && k != "contains" {
				return nil, where("check: unknown key %q (known: equals, contains)", k)
			}
		}
		st.Equals = Text(cm["equals"])
		if c, ok := cm["contains"]; ok && c != nil {
			ccm, ok := c.(map[string]any)
			if !ok {
				return nil, where("check.contains must be a mapping with key and values")
			}
			vals, _ := ccm["values"].([]any)
			st.Contains = &Contains{Key: Text(ccm["key"]), Values: vals}
			if !strings.HasPrefix(st.Contains.Key, "/") || len(vals) == 0 {
				return nil, where("check.contains needs key (a JSON pointer into an element, e.g. /id) and values")
			}
		}
	}
	if raw, ok := m["unique"]; ok && raw != nil {
		l, ok := raw.([]any)
		if !ok {
			return nil, where("\"unique\" must be a list of JSON pointers into the body")
		}
		for _, x := range l {
			p := Text(x)
			if !strings.HasPrefix(p, "/") {
				return nil, where("unique: %q is no JSON pointer", p)
			}
			st.Unique = append(st.Unique, p)
		}
	}
	return st, nil
}

// Check verifies the order of a scenario: ids are unique, bodyFrom and
// equals name a step above, and every placeholder is saved above.
func (sc *Scenario) Check() error {
	seen := map[string]*Step{}
	saved := map[string]string{}
	var errs []string
	for _, st := range sc.Steps {
		if seen[st.ID] != nil {
			errs = append(errs, fmt.Sprintf("%s: the id is used twice", st.ID))
		}
		for _, ref := range []struct{ key, id string }{{"bodyFrom", st.BodyFrom}, {"check.equals", st.Equals}} {
			if ref.id != "" && seen[ref.id] == nil {
				errs = append(errs, fmt.Sprintf("%s: %s %q names no step above", st.ID, ref.key, ref.id))
			}
		}
		if st.BodyFrom != "" && seen[st.BodyFrom] != nil && !seen[st.BodyFrom].HasBody && seen[st.BodyFrom].BodyFrom == "" {
			errs = append(errs, fmt.Sprintf("%s: bodyFrom %q: that step sends no body", st.ID, st.BodyFrom))
		}
		for _, name := range st.Uses() {
			if _, ok := saved[name]; !ok {
				errs = append(errs, fmt.Sprintf("%s: {{%s}} is saved by no step above", st.ID, name))
			}
		}
		seen[st.ID] = st
		for name := range st.Save {
			saved[name] = st.ID
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}

// Uses returns the placeholders a step uses.
func (st *Step) Uses() []string {
	var out []string
	for _, v := range []any{anyMap(st.Path), anyMap(st.Query), anyMap(st.Header), st.Body} {
		out = append(out, Names(v)...)
	}
	if st.Contains != nil {
		out = append(out, Names(st.Contains.Values)...)
	}
	for _, src := range st.Save {
		out = append(out, Names(src)...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func anyMap(m map[string]any) any {
	if m == nil {
		return nil
	}
	return m
}

// Step returns the step with the id, or nil.
func (sc *Scenario) Step(id string) *Step {
	for _, st := range sc.Steps {
		if st.ID == id {
			return st
		}
	}
	return nil
}

// Encode writes the scenario; head heads the file as a comment. The
// reasons of the steps and values are written as comments.
func (sc *Scenario) Encode(head string) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if len(sc.Order) > 0 {
		o := node(sc.Order)
		root.Content = append(root.Content, str("order"), o)
	}
	if len(sc.Records) > 0 {
		recs := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, r := range sc.Records {
			var refs any
			if len(r.Refs) > 0 {
				refs = r.Refs
			}
			v := mapping("resource", r.Resource, "refs", refs)
			v.Style = yaml.FlowStyle
			recs.Content = append(recs.Content, str(r.Name), v)
		}
		root.Content = append(root.Content, str("records"), recs)
	}
	if sc.Golden != nil {
		root.Content = append(root.Content, str("golden"), goldenMeta(sc.Golden))
	}
	steps := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, st := range sc.Steps {
		n := st.node()
		if sc.Golden != nil {
			if sh := sc.Golden.Shot(st.ID); sh != nil {
				n.Content = append(n.Content, str("answer"), shotNode(sh, false))
			}
		}
		steps.Content = append(steps.Content, n)
	}
	root.Content = append(root.Content, str("steps"), steps)
	return encode(head, root)
}

func (st *Step) node() *yaml.Node {
	var check *yaml.Node
	if st.Equals != "" || st.Contains != nil {
		var contains *yaml.Node
		if st.Contains != nil {
			contains = mapping("key", st.Contains.Key, "values", st.Contains.Values)
			contains.Style = yaml.FlowStyle
		}
		eq := any(nil)
		if st.Equals != "" {
			eq = st.Equals
		}
		check = mapping("equals", eq, "contains", contains)
		check.Style = yaml.FlowStyle
	}
	opt := func(m map[string]any) any {
		if len(m) == 0 {
			return nil
		}
		return m
	}
	var body any
	if st.HasBody {
		body = st.Body
		if body == nil {
			body = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
		}
	}
	var save any
	if len(st.Save) > 0 {
		save = st.Save
	}
	tok, from, uniq := any(nil), any(nil), any(nil)
	if st.Token != "" {
		tok = st.Token
	}
	if st.BodyFrom != "" {
		from = st.BodyFrom
	}
	if len(st.Unique) > 0 {
		uniq = st.Unique
	}
	n := mapping("id", st.ID, "call", st.Call, "path", opt(st.Path), "query", opt(st.Query), "header", opt(st.Header),
		"body", body, "bodyFrom", from, "save", save, "token", tok, "expect", st.Expect, "check", check, "unique", uniq)
	if st.Why != "" && len(n.Content) > 1 {
		n.Content[1].LineComment = "# " + st.Why
	}
	for ptr, note := range st.Notes {
		if target := at(n, ptr); target != nil {
			target.LineComment = "# " + note
		}
	}
	return n
}

// at finds the value node at a JSON pointer in a mapping node. The
// collections on the way are written as blocks, since a comment cannot
// stand inside a one-line collection.
func at(n *yaml.Node, ptr string) *yaml.Node {
	cur := n
	var path []*yaml.Node
	for _, seg := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		path = append(path, cur)
		switch cur.Kind {
		case yaml.MappingNode:
			var next *yaml.Node
			for i := 0; i+1 < len(cur.Content); i += 2 {
				if cur.Content[i].Value == seg {
					next = cur.Content[i+1]
				}
			}
			if next == nil {
				return nil
			}
			cur = next
		case yaml.SequenceNode:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(cur.Content) {
				return nil
			}
			cur = cur.Content[i]
		default:
			return nil
		}
	}
	for _, p := range path {
		p.Style &^= yaml.FlowStyle
	}
	if cur.Kind != yaml.ScalarNode {
		cur.Style &^= yaml.FlowStyle
	}
	return cur
}

// Map returns the step as plain values, the form ParseStep reads; the
// overrides of the generator merge into it.
func (st *Step) Map() map[string]any {
	m := map[string]any{"id": st.ID, "call": st.Call, "expect": json.Number(strconv.Itoa(st.Expect))}
	if len(st.Path) > 0 {
		m["path"] = st.Path
	}
	if len(st.Query) > 0 {
		m["query"] = st.Query
	}
	if len(st.Header) > 0 {
		m["header"] = st.Header
	}
	if st.HasBody {
		m["body"] = st.Body
	}
	if st.BodyFrom != "" {
		m["bodyFrom"] = st.BodyFrom
	}
	if len(st.Save) > 0 {
		s := map[string]any{}
		for k, v := range st.Save {
			s[k] = v
		}
		m["save"] = s
	}
	if st.Token != "" {
		m["token"] = st.Token
	}
	if st.Equals != "" || st.Contains != nil {
		c := map[string]any{}
		if st.Equals != "" {
			c["equals"] = st.Equals
		}
		if st.Contains != nil {
			c["contains"] = map[string]any{"key": st.Contains.Key, "values": st.Contains.Values}
		}
		m["check"] = c
	}
	if len(st.Unique) > 0 {
		var l []any
		for _, u := range st.Unique {
			l = append(l, u)
		}
		m["unique"] = l
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
