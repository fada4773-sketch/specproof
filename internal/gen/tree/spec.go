package tree

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

var placeholderRe = regexp.MustCompile(`\{[^}]+\}`)

// lookupOp finds an operation by operationId or "METHOD /path".
func lookupOp(s *spec.Spec, call string) *spec.Operation {
	if op := s.Op(call); op != nil {
		return op
	}
	if f := strings.Fields(call); len(f) == 2 {
		return s.Op(strings.ToUpper(f[0]) + " " + f[1])
	}
	return nil
}

// callOf is the "METHOD /path" of an operation.
func callOf(op *spec.Operation) string { return op.Method + " " + op.Path }

// pathParams are the names of the path parameters of an operation, in the
// order of the path.
func pathParams(op *spec.Operation) []string {
	if v, ok := pathParamCache.Load(op.Path); ok {
		return slices.Clone(v.([]string))
	}
	var out []string
	for _, m := range placeholderRe.FindAllString(op.Path, -1) {
		out = append(out, m[1:len(m)-1])
	}
	pathParamCache.Store(op.Path, slices.Clone(out))
	return out
}

// pathParamCache holds the parameters of a path: proposing for a large
// spec asks for them millions of times.
var pathParamCache sync.Map

// lowestSuccess is the lowest documented 2xx status, 0 without.
func lowestSuccess(op *spec.Operation) int {
	best := 0
	if op.Op.Responses == nil {
		return 0
	}
	for code := range op.Op.Responses.Map() {
		n, err := strconv.Atoi(code)
		if err == nil && n/100 == 2 && (best == 0 || n < best) {
			best = n
		}
	}
	if best == 0 {
		for code := range op.Op.Responses.Map() {
			if strings.EqualFold(code, "2XX") {
				return http.StatusOK
			}
		}
	}
	return best
}

// documents reports whether an operation documents a status exactly.
func documents(op *spec.Operation, status int) bool {
	return op.Op.Responses != nil && op.Op.Responses.Value(strconv.Itoa(status)) != nil
}

// responseRef is the JSON schema of the answer with the status.
func responseRef(op *spec.Operation, status int) *openapi3.SchemaRef {
	_, r := cases.ResponseFor(op, status)
	if r == nil {
		return nil
	}
	_, media := cases.PickMedia(r.Content)
	if media == nil {
		return nil
	}
	return media.Schema
}

func responseSchema(op *spec.Operation, status int) *openapi3.Schema {
	if ref := responseRef(op, status); ref != nil {
		return ref.Value
	}
	return nil
}

// requestRef is the JSON schema of the request body.
func requestRef(op *spec.Operation) *openapi3.SchemaRef {
	mt, media := cases.RequestMedia(op)
	if media == nil || !cases.IsJSONMedia(mt) {
		return nil
	}
	return media.Schema
}

// properties are the properties of a schema with those of its allOf parts,
// and the required ones.
func properties(s *openapi3.Schema) (openapi3.Schemas, []string) {
	if s == nil {
		return nil, nil
	}
	return dict.Properties(s)
}

// bodyField finds a field of a body by its path (pilotId, crew.pilotId),
// ignoring case, through the objects on the way: its schema and the path
// as the schema spells it.
func bodyField(props openapi3.Schemas, path string) (*openapi3.SchemaRef, string, bool) {
	var names []string
	var cur *openapi3.SchemaRef
	for i, seg := range strings.Split(path, ".") {
		if i > 0 {
			if cur == nil || cur.Value == nil {
				return nil, "", false
			}
			props, _ = properties(cur.Value)
		}
		name, ok := propertyFoldName(props, seg)
		if !ok {
			return nil, "", false
		}
		cur = props[name]
		names = append(names, name)
	}
	return cur, strings.Join(names, "."), true
}

// bodyRequires reports whether the request body of op requires a field:
// every part of its path (crew and crew.pilotId).
func bodyRequires(op *spec.Operation, path string) bool {
	s := schemaOf(requestRef(op))
	for _, seg := range strings.Split(path, ".") {
		if s == nil {
			return false
		}
		props, req := properties(s)
		name, ok := propertyFoldName(props, seg)
		if !ok || !slices.Contains(req, name) {
			return false
		}
		s = schemaOf(props[name])
	}
	return true
}

// setField sets a field of a body by its path, the objects on the way
// created when the body lacks them; keys match ignoring case.
func setField(body map[string]any, path string, v any) {
	segs := strings.Split(path, ".")
	cur := body
	for _, seg := range segs[:len(segs)-1] {
		k, next, ok := foldEntry(cur, seg)
		m, isMap := next.(map[string]any)
		if !ok || !isMap {
			k, m = seg, map[string]any{}
		}
		cur[k] = m
		cur = m
	}
	last := segs[len(segs)-1]
	if k, _, ok := foldEntry(cur, last); ok {
		last = k
	}
	cur[last] = v
}

// listPointer says where the list of a list answer is by the schema of the
// lowest 2xx answer: "" for an array, "/items" for a page with one list.
func listPointer(op *spec.Operation) (string, bool) {
	ref := responseRef(op, lowestSuccess(op))
	if ref == nil || ref.Value == nil {
		return "", false
	}
	s := ref.Value
	if s.Type != nil && s.Type.Is("array") {
		return "", true
	}
	props, _ := properties(s)
	var lists []string
	for _, k := range sortedKeys(props) {
		p := props[k]
		if p == nil || p.Value == nil || p.Value.Type == nil || !p.Value.Type.Is("array") {
			continue
		}
		// a page holds records: objects
		if items := p.Value.Items; items != nil && items.Value != nil {
			if fields, _ := properties(items.Value); len(fields) > 0 {
				lists = append(lists, k)
			}
		}
	}
	if len(lists) == 1 && (model.Page(dict.DTORef(ref), lists[0]) || len(props) <= 4 && len(props) > 1) {
		return "/" + lists[0], true
	}
	return "", false
}

// isList reports whether an operation answers with a list.
func isList(op *spec.Operation) bool {
	_, ok := listPointer(op)
	return ok
}

// Check verifies a tree against the spec: every call names an operation,
// a node reads (GET, or POST with read: true), every path parameter has a
// value, every "<node>.field" names an ancestor or a node fetched before,
// and every "$name" an entry of params. Nodes that are off are not
// checked.
func Check(s *spec.Spec, t *Tree) []string {
	problems, _ := check(s, t)
	return problems
}

// check finds the problems of a file and the fixes for those that have one.
func check(s *spec.Spec, t *Tree) ([]string, []Fix) {
	t.Complete(s)
	var problems []string
	var fixes []Fix
	names := map[string]bool{}
	t.Walk(func(n *Node) { names[n.Name] = true })
	// fetch order: a node may read a node that is not above it only if that
	// node is fetched before it
	pos := map[string]int{}
	var order func([]*Node)
	order = func(list []*Node) {
		for _, n := range list {
			pos[n.Name] = len(pos)
			order(n.Each)
		}
	}
	order(t.Roots)
	add := func(line int, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("line %d: %s", line, fmt.Sprintf(format, args...)))
	}
	// fix adds a problem with the change that solves it
	fix := func(f Fix, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("line %d: %s", f.Line, fmt.Sprintf(format, args...))+f.change())
		if f.apply != nil {
			fixes = append(fixes, f)
		}
	}
	// checkRefs: "<node>.field" must name a node of the tree; read by a node
	// (write false), a node that is not above it must come before it
	// "$name" counts as its value of params, where it is used; target is
	// the YAML the values stand in, for a fix (nil: none)
	scope := func(name string) bool { return names[name] }
	var checkRefs func(line int, where string, at *Node, write bool, target *yaml.Node, values ...any)
	checkRefs = func(line int, where string, at *Node, write bool, target *yaml.Node, values ...any) {
		for _, v := range values {
			walkStrings(v, func(whole string) {
				for _, str := range refsOf(whole, scope) {
					if prm, isParam := ParamRef(str); isParam {
						val, known := t.Params[prm]
						if !known {
							fix(Fix{Line: line, Change: fmt.Sprintf("add %s: <value> under params at the top", prm)},
								"%s: %q: params has no entry %s", where, str, prm)
							continue
						}
						if nestedParam(val, scope) {
							add(line, "%s: params.%s is %v; a value of params is a value or a reference to a node (books.id), no other $name", where, prm, val)
							continue
						}
						var pt *yaml.Node
						if t.doc != nil {
							pt = valueOf(valueOf(t.doc.Root, "params"), prm)
						}
						pline := line
						if pt != nil {
							pline = pt.Line
						}
						checkRefs(pline, where+" ($"+prm+")", at, write, pt, val)
						continue
					}
					if ref, ok := ParseRef(str, scope); ok && ref.Node == "" && target != nil {
						if f, ok := leadingDot(s, at, write, ref, line, target, scope); ok {
							fix(f, "%s: %q reads the field %q of %s, but %s is a node",
								where, str, strings.SplitN(ref.Field, ".", 2)[0], dotRecord(at, write), strings.SplitN(ref.Field, ".", 2)[0])
						}
						continue
					}
					name, _, ok := strings.Cut(str, ".")
					if !ok || !names[name] || at.Ancestor(name) != nil || write {
						continue
					}
					if p, known := pos[name]; known && p < pos[at.Name] {
						continue
					}
					fix(Fix{Line: line, Change: fmt.Sprintf("move the node %s above %s in the tree (or earlier), or give the value in params", name, at.Name)},
						"%s: %q names node %s, which is neither above it nor fetched before it", where, str, name)
				}
			})
		}
	}
	var walk func([]*Node)
	walk = func(list []*Node) {
		for _, n := range list {
			if !n.Enabled() {
				continue
			}
			op := lookupOp(s, n.Call)
			switch {
			case op == nil:
				add(n.Line, "%s: %q is no operation of the spec", n.Name, n.Call)
			case !reads(op, n):
				add(n.Line, "%s: %s would write; a node only reads (GET, or a POST that only reads with read: true)", n.Name, callOf(op))
			default:
				for _, p := range pathParams(op) {
					if _, ok := n.Path[p]; !ok {
						add(n.Line, "%s: no value for the path parameter {%s} (%s %s=…)", n.Name, p, callOf(op), p)
					}
				}
			}
			checkRefs(n.Line, n.Name, n, false, n.value, anyMap(n.Path), anyMap(n.Query), anyMap(n.Header), n.Body)
			for _, c := range slices.Concat(n.Where, n.WhereAny) {
				checkRefs(n.Line, n.Name+".where."+c.Field, n, false, nil, c.Test)
			}
			for _, r := range n.Require {
				if rop := lookupOp(s, r.Call); rop == nil || !reads(rop, r) {
					add(r.Line, "%s: require %s: %q is no reading operation of the spec", n.Name, r.Name, r.Call)
				}
			}
			for _, kind := range n.Kinds() {
				w := n.Write[kind]
				if w == nil || w.None || w.Off || w.Gone {
					continue
				}
				wop := lookupOp(s, w.Call)
				if wop == nil {
					add(w.Line, "%s.write.%s: %q is no operation of the spec", n.Name, kind, w.Call)
					continue
				}
				for _, p := range pathParams(wop) {
					if _, ok := w.Path[p]; !ok {
						add(w.Line, "%s.write.%s: no value for the path parameter {%s}", n.Name, kind, p)
					}
				}
				checkRefs(w.Line, n.Name+".write."+kind, n, true, w.value, anyMap(w.Path), anyMap(w.Query), anyMap(w.Header), w.Body)
				for _, name := range sortedKeys(w.Save) {
					if !savedName.MatchString(name) {
						add(w.Line, "%s.write.%s.save: %q: a name of letters, digits, _, - and . (receiptNo)", n.Name, kind, name)
					}
				}
				if w.From != "" && w.From != "." && t.Node(w.From) == nil {
					add(w.Line, "%s.write.%s: body from %q: no node of that name", n.Name, kind, w.From)
				}
			}
			walk(n.Each)
		}
	}
	walk(t.Roots)
	problems = append(problems, checkFilters(s, t)...)
	op, of := checkOrder(s, t)
	problems = append(problems, op...)
	fixes = append(fixes, of...)
	rp, rf := checkReferences(s, t)
	problems = append(problems, rp...)
	fixes = append(fixes, rf...)
	sp, sf := checkScope(s, t)
	problems = append(problems, sp...)
	fixes = append(fixes, sf...)
	wp, wf := checkStale(s, t)
	problems = append(problems, wp...)
	fixes = append(fixes, wf...)
	sortProblems(problems)
	slices.SortStableFunc(fixes, func(a, b Fix) int { return a.Line - b.Line })
	return problems, fixes
}

// dotRecord names the record ".field" reads at a node: in a write the
// record itself, else the record above.
func dotRecord(at *Node, write bool) string {
	if write || at.Parent == nil {
		return "the record of " + at.Name
	}
	return "the record of " + at.Parent.Name + " above"
}

// leadingDot finds ".<node>.field" where "<node>.field" is meant: the first
// part is a node of the tree and no field of the record "." reads. The fix
// changes it in target, the YAML the value stands in (a fix runs on the
// tree it was found in).
func leadingDot(s *spec.Spec, at *Node, write bool, ref Ref, line int, target *yaml.Node, scope func(string) bool) (Fix, bool) {
	first, rest, nested := strings.Cut(ref.Field, ".")
	if !nested || rest == "" || !scope(first) {
		return Fix{}, false
	}
	rec := at
	if !write {
		rec = at.Parent
	}
	if rec == nil {
		return Fix{}, false
	}
	op := lookupOp(s, rec.Call)
	if op == nil {
		return Fix{}, false
	}
	props, _ := properties((&proposer{}).recordSchema(op))
	if len(props) == 0 {
		return Fix{}, false
	}
	if _, has := propertyFoldIn(props, first); has {
		return Fix{}, false
	}
	from, to := "."+ref.Field, ref.Field
	return Fix{Line: line, Change: from + " → " + to, apply: func(*Tree) bool {
		return replaceRef(target, from, to)
	}}, true
}

// savedName is the form of a name a write saves: a placeholder name of the
// scenario.
var savedName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// nestedParam reports whether a value of params names another $name, as a
// whole or in a part it composes.
func nestedParam(v any, scope func(string) bool) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, part := range refsOf(s, scope) {
		if _, isParam := ParamRef(part); isParam {
			return true
		}
	}
	return false
}

// reads reports whether a node's operation only reads: a GET, or a POST
// marked read: true.
func reads(op *spec.Operation, n *Node) bool {
	return op.Method == http.MethodGet || op.Method == http.MethodPost && n.Read
}

func anyMap(m map[string]any) any {
	if m == nil {
		return nil
	}
	return m
}

func walkStrings(v any, fn func(string)) {
	switch x := v.(type) {
	case string:
		fn(x)
	case map[string]any:
		for _, c := range x {
			walkStrings(c, fn)
		}
	case []any:
		for _, c := range x {
			walkStrings(c, fn)
		}
	}
}
