package tree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Options configure the requests of a fetch.
type Options struct {
	// BaseURL is the instance, with base path.
	BaseURL string
	// Token is sent as "Authorization: Bearer <token>" if set.
	Token   string
	Headers map[string]string
	Client  *http.Client
	Timeout time.Duration
}

// Record is one record a fetch took: an element of a list answer, or a
// single answer.
type Record struct {
	// ID names the record: the node name and its number, "docks2".
	ID     string
	Node   *Node
	Parent *Record
	Value  any
	// Saved are the values of the node's save.
	Saved map[string]any
	// Request is the request that answered with the record.
	Request string
}

// Data is what a fetch took.
type Data struct {
	Source   string
	Records  []*Record
	Notes    []string
	Requests int
}

// Of returns the records of a node, in fetch order.
func (d *Data) Of(n *Node) []*Record {
	var out []*Record
	for _, r := range d.Records {
		if r.Node == n {
			out = append(out, r)
		}
	}
	return out
}

// Children returns the records of a node fetched under a parent record.
func (d *Data) Children(parent *Record, n *Node) []*Record {
	var out []*Record
	for _, r := range d.Records {
		if r.Node == n && r.Parent == parent {
			out = append(out, r)
		}
	}
	return out
}

// errBudget stops a fetch that reached the budget.
var errBudget = errors.New("budget reached")

type fetcher struct {
	s      *spec.Spec
	t      *Tree
	opt    Options
	data   *Data
	cache  map[string]*answer
	count  map[*Node]int
	budget int
	// lastTarget is the request sent last, for notes.
	lastTarget string
	scope      func(string) bool
	// unset are the nodes left out for a "$name" without value, noted once.
	unset map[*Node]bool
}

// first is the first record of a node fetched so far, nil without.
func (f *fetcher) first(name string) *Record {
	for _, r := range f.data.Records {
		if r.Node.Name == name {
			return r
		}
	}
	return nil
}

// cand is an element of an answer with the request that gave it.
type cand struct {
	value  any
	target string
	header http.Header
}

type answer struct {
	status int
	header http.Header
	body   any
}

// Fetch sends the requests of the tree to the instance and takes the
// records. Only GET requests are sent, and POSTs marked read: true. A
// request that fails is noted and its branch left out; the fetch goes on.
func Fetch(ctx context.Context, s *spec.Spec, t *Tree, opt Options) (*Data, error) {
	if opt.BaseURL == "" {
		return nil, errors.New("no base URL")
	}
	if opt.Client == nil {
		opt.Client = &http.Client{}
	}
	if opt.Timeout == 0 {
		opt.Timeout = 10 * time.Second
	}
	if problems := Check(s, t); len(problems) > 0 {
		return nil, &FindingError{Problems: problems}
	}
	f := &fetcher{s: s, t: t, opt: opt, data: &Data{Source: opt.BaseURL}, cache: map[string]*answer{}, count: map[*Node]int{}, budget: t.Budget, scope: t.names(), unset: map[*Node]bool{}}
	if f.budget <= 0 {
		f.budget = DefaultBudget
	}
	for _, root := range t.Roots {
		if err := f.node(ctx, root, nil); err != nil {
			if errors.Is(err, errBudget) {
				f.note("the budget of %d requests is used up; the rest of the tree was not fetched (raise budget or lower pick)", f.budget)
				break
			}
			return f.data, err
		}
	}
	return f.data, nil
}

func (f *fetcher) note(format string, args ...any) {
	f.data.Notes = append(f.data.Notes, fmt.Sprintf(format, args...))
}

// where names a node and the record it runs under, for notes.
func where(n *Node, parent *Record) string {
	if parent == nil {
		return n.Name
	}
	return n.Name + " under " + parent.ID
}

func (f *fetcher) node(ctx context.Context, n *Node, parent *Record) error {
	if !n.Enabled() {
		return nil
	}
	op := lookupOp(f.s, n.Call)
	if op == nil {
		f.note("%s: %s is not in the spec; left out", n.Name, n.Call)
		return nil
	}
	if missing := unsetParams(f.t, n); len(missing) > 0 {
		if !f.unset[n] {
			f.unset[n] = true
			f.note("%s: left out, $%s has no value; set it under params at the top of tree.yaml", n.Name, strings.Join(missing, ", $"))
		}
		return nil
	}
	candidates, single, err := f.candidates(ctx, n, op, parent)
	if err != nil {
		return err
	}
	if candidates == nil {
		return nil
	}
	total := len(candidates)
	var kept []cand
	for _, c := range candidates {
		if n.Holds(c.value) {
			kept = append(kept, c)
		}
	}
	whereOut := total - len(kept)
	ordered, missing := f.order(n, kept)
	if len(missing) > 0 {
		f.note("%s: no record with key %s", where(n, parent), strings.Join(missing, ", "))
	}
	// want is what pick asks for, limit what can be taken
	want := len(ordered)
	if !single && !n.Pick.All && len(n.Pick.Keys) == 0 {
		want = max(n.Pick.N, 1)
	}
	limit := min(want, len(ordered))
	checks := n.Candidates
	if checks == 0 {
		checks = len(ordered)
	}
	var picked []cand
	requireOut := 0
	for i, c := range ordered {
		if len(picked) >= limit {
			break
		}
		if len(n.Require) > 0 {
			if i >= checks {
				break
			}
			ok, err := f.required(ctx, n, c.value, parent)
			if err != nil {
				return err
			}
			if !ok {
				requireOut++
				continue
			}
		}
		picked = append(picked, c)
	}
	// fewer records than asked for is noted when it is not just the data:
	// none at all, or candidates left out by where or require
	if !single && len(picked) < want && (len(picked) == 0 || whereOut > 0 || requireOut > 0) {
		var why []string
		if whereOut > 0 {
			why = append(why, fmt.Sprintf("%d left out by where", whereOut))
		}
		if requireOut > 0 {
			why = append(why, fmt.Sprintf("%d by require", requireOut))
		}
		f.note("%s: %d of %d picked from %d candidates%s", where(n, parent), len(picked), want, total, paren(why))
	}
	for _, c := range picked {
		f.count[n]++
		rec := &Record{ID: fmt.Sprintf("%s%d", n.Name, f.count[n]), Node: n, Parent: parent, Value: c.value, Request: c.target}
		if len(n.Save) > 0 {
			rec.Saved = map[string]any{}
			for name, src := range n.Save {
				if h, ok := strings.CutPrefix(src, "header "); ok {
					if v := c.header.Get(h); v != "" {
						rec.Saved[name] = v
					}
					continue
				}
				if v, ok := Pointer(c.value, src); ok {
					rec.Saved[name] = v
				}
			}
		}
		f.data.Records = append(f.data.Records, rec)
		for _, child := range n.Each {
			if err := f.node(ctx, child, rec); err != nil {
				return err
			}
		}
	}
	return nil
}

// unsetParams lists the "$name" values of a node (and of its requires) that
// params does not set.
func unsetParams(t *Tree, n *Node) []string {
	var out []string
	visit := func(x *Node) {
		for _, v := range []any{anyMap(x.Path), anyMap(x.Query), anyMap(x.Header), x.Body} {
			walkStrings(v, func(s string) {
				if name, ok := ParamRef(s); ok {
					if _, set := t.param(name); !set && !slices.Contains(out, name) {
						out = append(out, name)
					}
				}
			})
		}
	}
	visit(n)
	for _, r := range n.Require {
		visit(r)
	}
	return out
}

func paren(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// candidates sends the request of a node, page by page and once per value
// of a list-valued parameter, and returns the elements; single is set for
// an answer that is one record. nil means the request failed (noted).
func (f *fetcher) candidates(ctx context.Context, n *Node, op *spec.Operation, parent *Record) ([]cand, bool, error) {
	reqs, err := f.requests(n, op, parent)
	if err != nil {
		f.note("%s: %v", where(n, parent), err)
		return nil, false, nil
	}
	var out []cand
	seen := map[string]bool{}
	single := false
	for _, rq := range reqs {
		cursor := any(nil)
		for page := 0; page < pages(n); page++ {
			q, body := rq.query, rq.body
			if n.Page != nil {
				var v any
				switch {
				case n.Page.Next != "" && page == 0:
					v = nil
				case n.Page.Next != "":
					v = cursor
				default:
					v = f.pageValue(n.Page, op) + page*n.Page.Step
				}
				if v != nil {
					q, body = withPage(n.Page, q, body, v)
				}
			}
			a, err := f.send(ctx, op.Method, rq.path, q, rq.header, body, rq.hasBody)
			if err != nil {
				return nil, false, err
			}
			if a.status/100 != 2 {
				f.note("%s: %s answered %d; the branch is left out", where(n, parent), f.lastTarget, a.status)
				return nil, false, nil
			}
			list, isList := listOf(n, op, a.body)
			if !isList {
				single = true
				if a.body != nil {
					out = append(out, cand{a.body, f.lastTarget, a.header})
				}
				break
			}
			if len(list) == 0 {
				break
			}
			fresh := 0
			for _, el := range list {
				k := KeyText(el, keyOf(n))
				if k == "" {
					k = encodeJSON(el)
				}
				if seen[k] {
					continue
				}
				seen[k] = true
				fresh++
				out = append(out, cand{el, f.lastTarget, a.header})
			}
			if fresh == 0 {
				break // the instance ignores the page parameter
			}
			if n.Page != nil && n.Page.Next != "" {
				c, ok := Pointer(a.body, n.Page.Next)
				if !ok || c == nil || text(c) == "" {
					break
				}
				cursor = c
			}
		}
	}
	if out == nil {
		out = []cand{}
	}
	return out, single, nil
}

func pages(n *Node) int {
	if n.Page == nil {
		return 1
	}
	return n.Page.Max
}

// pageValue is the first page number or offset: start, else the default or
// minimum of the parameter, else 0 for an offset parameter and 1 otherwise.
func (f *fetcher) pageValue(pg *Page, op *spec.Operation) int {
	if pg.Start != nil {
		return *pg.Start
	}
	for _, p := range op.Params {
		if p.Name != pg.Param || p.Schema == nil || p.Schema.Value == nil {
			continue
		}
		if d := p.Schema.Value.Default; d != nil {
			if n, err := strconv.Atoi(text(spec.Normalize(d))); err == nil {
				return n
			}
		}
		if m := p.Schema.Value.Min; m != nil {
			return int(*m)
		}
	}
	if strings.Contains(strings.ToLower(pg.Param), "offset") || strings.Contains(strings.ToLower(pg.Param), "skip") {
		return 0
	}
	return 1
}

func withPage(pg *Page, query map[string]any, body any, v any) (map[string]any, any) {
	if pg.In == "body" {
		b := scenario.Clone(body)
		m, ok := b.(map[string]any)
		if !ok {
			m = map[string]any{}
		}
		setPointer(m, pg.Param, v)
		return query, m
	}
	q := map[string]any{}
	for k, x := range query {
		q[k] = x
	}
	q[pg.Param] = v
	return q, body
}

// setPointer sets a value at a JSON pointer of objects, creating them.
func setPointer(m map[string]any, ptr string, v any) {
	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	cur := m
	for i, p := range parts {
		if i == len(parts)-1 {
			cur[p] = v
			return
		}
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
}

type request struct {
	path    string
	query   map[string]any
	header  map[string]any
	body    any
	hasBody bool
}

// requests builds the requests of a node under a parent record: one, or one
// per value of the parameter that holds a list.
func (f *fetcher) requests(n *Node, op *spec.Operation, parent *Record) ([]request, error) {
	res := func(v any) (any, error) { return resolveFetch(v, parent, f.t, f.scope, f.first) }
	path, err := resolveMap(n.Path, res)
	if err != nil {
		return nil, err
	}
	query, err := resolveMap(n.Query, res)
	if err != nil {
		return nil, err
	}
	if n.Page != nil && n.Page.In != "body" {
		for k, v := range n.Page.Size {
			if query == nil {
				query = map[string]any{}
			}
			query[k] = v
		}
	}
	header, err := resolveMap(n.Header, res)
	if err != nil {
		return nil, err
	}
	var body any
	if n.HasBody {
		if body, err = res(n.Body); err != nil {
			return nil, err
		}
		if n.Page != nil && n.Page.In == "body" && len(n.Page.Size) > 0 {
			m, _ := scenario.Clone(body).(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			for k, v := range n.Page.Size {
				setPointer(m, k, v)
			}
			body = m
		}
	}
	target, err := fillPath(op.Path, path)
	if err != nil {
		return nil, err
	}
	// one request per value of a list in the query
	var listKey string
	for _, k := range sortedKeys(query) {
		if _, ok := query[k].([]any); ok && isPlainList(n.Query[k]) {
			if listKey != "" {
				return nil, fmt.Errorf("query parameters %s and %s both hold a list; give only one a list", listKey, k)
			}
			listKey = k
		}
	}
	if listKey == "" {
		return []request{{path: target, query: query, header: header, body: body, hasBody: n.HasBody}}, nil
	}
	var out []request
	for _, v := range query[listKey].([]any) {
		q := map[string]any{}
		for k, x := range query {
			q[k] = x
		}
		q[listKey] = v
		out = append(out, request{path: target, query: q, header: header, body: body, hasBody: n.HasBody})
	}
	return out, nil
}

// isPlainList reports whether a value of the file is a literal list (one
// request per value), not a reference that answers with a list.
func isPlainList(v any) bool {
	_, ok := v.([]any)
	return ok
}

func resolveMap(m map[string]any, res func(any) (any, error)) (map[string]any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := map[string]any{}
	for k, v := range m {
		x, err := res(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = x
	}
	return out, nil
}

// resolveFetch fills the references of a value with the values of the
// record the node runs under (".field"), of an ancestor, or of the first
// record of another node fetched before ("<node>.field"), and of params
// ("$name").
func resolveFetch(v any, at *Record, t *Tree, scope func(string) bool, first func(string) *Record) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, c := range x {
			r, err := resolveFetch(c, at, t, scope, first)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, c := range x {
			r, err := resolveFetch(c, at, t, scope, first)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	if name, ok := ParamRef(v); ok {
		val, set := t.param(name)
		if !set {
			return nil, fmt.Errorf("$%s has no value; set it under params at the top of tree.yaml", name)
		}
		return val, nil
	}
	ref, ok := ParseRef(v, scope)
	if !ok {
		return v, nil
	}
	rec := at
	if ref.Node != "" {
		rec = at.Ancestor(ref.Node)
		if rec == nil {
			rec = first(ref.Node)
		}
	}
	if rec == nil {
		return nil, fmt.Errorf("%s: no record to read it from", ref)
	}
	val, ok := rec.Get(ref.Field)
	if !ok {
		return nil, fmt.Errorf("%s: %s has no field %q", ref, rec.ID, ref.Field)
	}
	return val, nil
}

// Ancestor returns the record of the node with the name: r itself or a
// record above it.
func (r *Record) Ancestor(name string) *Record {
	for x := r; x != nil; x = x.Parent {
		if x.Node.Name == name {
			return x
		}
	}
	return nil
}

// Get returns a saved value or a field of the record.
func (r *Record) Get(field string) (any, bool) {
	if v, ok := r.Saved[field]; ok {
		return v, true
	}
	v, ok := Field(r.Value, field)
	if ok && v == nil {
		return nil, false
	}
	return v, ok
}

// fillPath puts the path parameters into the path of the spec.
func fillPath(path string, values map[string]any) (string, error) {
	var missing []string
	out := placeholderRe.ReplaceAllStringFunc(path, func(m string) string {
		name := m[1 : len(m)-1]
		v, ok := values[name]
		if !ok || v == nil {
			missing = append(missing, name)
			return m
		}
		return url.PathEscape(text(v))
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("no value for the path parameter {%s}", strings.Join(missing, "}, {"))
	}
	return out, nil
}

// required checks a candidate against the requires of a node.
func (f *fetcher) required(ctx context.Context, n *Node, cand any, parent *Record) (bool, error) {
	probe := &Record{ID: n.Name + "?", Node: n, Parent: parent, Value: cand}
	for _, r := range n.Require {
		op := lookupOp(f.s, r.Call)
		if op == nil {
			return false, nil
		}
		reqs, err := f.requests(r, op, probe)
		if err != nil {
			f.note("%s: require %s: %v", where(n, parent), r.Name, err)
			return false, nil
		}
		for _, rq := range reqs {
			a, err := f.send(ctx, op.Method, rq.path, rq.query, rq.header, rq.body, rq.hasBody)
			if err != nil {
				return false, err
			}
			if a.status/100 != 2 || !r.Holds(a.body) {
				return false, nil
			}
		}
	}
	return true, nil
}

// order puts the candidates in the order they are picked: the keys of pick
// (and the keys not found), else at random by the seed, else as answered.
func (f *fetcher) order(n *Node, cands []cand) ([]cand, []string) {
	if len(n.Pick.Keys) > 0 {
		key := keyOf(n)
		var out []cand
		var missing []string
		for _, want := range n.Pick.Keys {
			i := slices.IndexFunc(cands, func(c cand) bool {
				v, ok := Pointer(c.value, key[0])
				return ok && text(v) == text(want)
			})
			if i < 0 {
				missing = append(missing, text(want))
				continue
			}
			out = append(out, cands[i])
		}
		return out, missing
	}
	if n.Pick.First || n.Pick.All || len(cands) < 2 {
		return cands, nil
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(n.PathName()))
	rnd := rand.New(rand.NewPCG(f.t.Seed, h.Sum64()))
	out := slices.Clone(cands)
	rnd.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out, nil
}

// keyOf is the key of a node: its key, else /id.
func keyOf(n *Node) []string {
	if len(n.Key) > 0 {
		return n.Key
	}
	return []string{"/id"}
}

// listOf finds the list in an answer: at items, else by the schema of the
// operation, else by the shape of the answer.
func listOf(n *Node, op *spec.Operation, body any) ([]any, bool) {
	if n.HasItems {
		v, ok := Pointer(body, n.Items)
		l, isList := v.([]any)
		return l, ok && isList
	}
	if ptr, ok := listPointer(op); ok {
		v, found := Pointer(body, ptr)
		if l, isList := v.([]any); found && isList {
			return l, true
		}
		return nil, false
	}
	l, ok := body.([]any)
	return l, ok
}

func (f *fetcher) send(ctx context.Context, method, path string, query, header map[string]any, body any, hasBody bool) (*answer, error) {
	target := path
	if len(query) > 0 {
		v := url.Values{}
		for _, k := range sortedKeys(query) {
			if l, ok := query[k].([]any); ok {
				for _, x := range l {
					v.Add(k, text(x))
				}
				continue
			}
			v.Set(k, text(query[k]))
		}
		target += "?" + v.Encode()
	}
	f.lastTarget = method + " " + target
	cacheKey := f.lastTarget
	if hasBody {
		cacheKey += " " + encodeJSON(body)
	}
	if a, ok := f.cache[cacheKey]; ok {
		return a, nil
	}
	if f.data.Requests >= f.budget {
		return nil, errBudget
	}
	f.data.Requests++
	ctx, cancel := context.WithTimeout(ctx, f.opt.Timeout)
	defer cancel()
	var rd io.Reader
	if hasBody {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(f.opt.BaseURL, "/")+target, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range f.opt.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range header {
		req.Header.Set(k, text(v))
	}
	if f.opt.Token != "" {
		req.Header.Set("Authorization", "Bearer "+f.opt.Token)
	}
	resp, err := f.opt.Client.Do(req)
	if err != nil {
		// the error holds the URL, never the token
		return nil, fmt.Errorf("%s: %w", f.lastTarget, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	a := &answer{status: resp.StatusCode, header: resp.Header}
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) == nil {
			a.body = v
		}
	}
	f.cache[cacheKey] = a
	return a, nil
}
