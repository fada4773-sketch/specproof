// Package tree reads, proposes and runs the tree file (tree.yaml): which
// records apitest-gen fetches from a running instance and how they hang
// together. A node names a request; the records it answers with are fetched,
// filtered (where, require) and picked, and every child node runs once per
// picked record. The write block of a node says how a record of the node is
// created, updated, patched and deleted. From the fetched records the
// package builds the scenario that rebuilds them in an empty instance.
//
//	tree:
//	  planets:
//	    call: GET /planets                    # listPlanets
//	    pick: [mars, venus]
//	    key: [/code]
//	    write:
//	      create: none                        # the instance holds them (seed)
//	    each:
//	      docks:
//	        call: GET /planets/{code}/docks   # listDocks
//	        path: {code: .code}
//	        pick: 2
//	        write:
//	          create: POST /planets/{code}/docks code=planets.code
//	          delete: DELETE /docks/{dockId} dockId=.id
//	        each:
//	          dock: GET /docks/{dockId} dockId=.id
//
// A value of the form ".field" reads the record the node runs under (in a
// write block: the record itself), "<node>.field" the record of that
// ancestor node (or of a node fetched before), "$name" the value of params
// at the top of the file; every other value is used as it is.
package tree

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
)

// Write kinds, in the order they are written.
const (
	WriteCreate = "create"
	WriteUpdate = "update"
	WritePatch  = "patch"
	WriteDelete = "delete"
)

var writeKinds = []string{WriteCreate, WriteUpdate, WritePatch, WriteDelete}

// terminalVerbs start actions that end what a record can do.
var terminalVerbs = []string{"archive", "cancel", "close", "complete", "reject", "lock", "disable", "suspend", "deactivate", "expire", "finish", "end", "retire"}

func terminal(kind string) bool {
	ws := words(kind)
	return len(ws) > 0 && slices.Contains(terminalVerbs, ws[0])
}

// IsAction reports whether a write kind is an action: any other name of a
// write block (publish: POST /books/{bookId}/publish). The scenario runs it
// once on a record it created, after update and patch.
func IsAction(kind string) bool { return !slices.Contains(writeKinds, kind) }

// Kinds are the write kinds of a node in the order they run: create,
// update, patch, delete, then the actions by name, those that end a record's
// life (archive, cancel, close …) last: after them other actions fail.
func (n *Node) Kinds() []string { return kindsOf(n.Write) }

func kindsOf(ws map[string]*Write) []string {
	var out, actions []string
	for _, k := range writeKinds {
		if ws[k] != nil {
			out = append(out, k)
		}
	}
	for k := range ws {
		if IsAction(k) {
			actions = append(actions, k)
		}
	}
	slices.SortFunc(actions, func(a, b string) int {
		ta, tb := terminal(a), terminal(b)
		switch {
		case ta && !tb:
			return 1
		case tb && !ta:
			return -1
		}
		return strings.Compare(a, b)
	})
	return append(out, actions...)
}

// DefaultBudget is the number of requests a fetch sends at most.
const DefaultBudget = 2000

// Tree is the tree file.
type Tree struct {
	// Params are values no record of the tree gives (a parameter no node
	// answers with), read as "$name"; nil until someone sets them.
	Params map[string]any
	// ParamNames is the order of Params in the file; ParamNotes says where
	// each is used.
	ParamNames []string
	ParamNotes map[string]string
	// Seed makes random picks repeatable.
	Seed uint64
	// Budget limits the requests of a fetch.
	Budget int
	// Roots are the nodes under "tree".
	Roots []*Node
	// Ignore are operations ("METHOD /path" or operationId) the generator
	// never proposes; IgnoreNotes says why the generator put one there.
	Ignore      []string
	IgnoreNotes map[string]string
	// Scope limits what the generator proposes: tags and operations in,
	// tags and operations out.
	Scope Scope

	// legacy are the nodes of an "unplaced" section of an earlier version.
	legacy []*Node
	doc    *yamldoc.Doc
}

// Scope is the part of the spec the generator proposes: only operations of
// Tags (all without), none of ExcludeTags or ExcludeOps (operationIds or
// "METHOD /path"; * matches any text). Deps also proposes the reads of
// other tags that give values the scope needs (a list of authors for
// {authorId}).
type Scope struct {
	Tags, ExcludeTags, ExcludeOps []string
	Deps                          bool
}

// Node is one request of the tree and the records it answers with.
type Node struct {
	Name string
	// Call is "METHOD /path" or an operationId.
	Call                string
	Path, Query, Header map[string]any
	Body                any
	HasBody             bool
	// Read marks a POST that only reads (a search); only such a POST is
	// sent by a fetch.
	Read bool
	// Items is the JSON pointer of the list in the answer; "" with HasItems
	// is the answer itself. Without, the list is found by itself.
	Items    string
	HasItems bool
	Page     *Page
	Pick     Pick
	// Candidates limits the elements require checks.
	Candidates int
	// Min is, for a node of require, the number of elements its answer
	// must list at least.
	Min   int
	Where []Clause
	// WhereAny holds alternatives: one of them must hold.
	WhereAny []Clause
	Require  []*Node
	// Key are JSON pointers into a record that identify it.
	Key []string
	// Save keeps values of the answer under a name: a JSON pointer or
	// "header <Name>".
	Save  map[string]string
	Write map[string]*Write
	Each  []*Node
	Off   bool
	// IsRequire marks a node of require: it checks a candidate and keeps
	// no record.
	IsRequire bool
	// Gone marks a call the spec no longer has ("# gone: …" after it): the
	// node is kept in the file but not checked or fetched.
	Gone bool
	// Comment is written after the call (the operationId); Note explains a
	// node that is off, or a guess.
	Comment, Note string

	Parent *Node
	Line   int
	// value is the node's value in the file: a mapping, or a scalar in the
	// short form; key is its name and owner the mapping that holds it.
	value, key, owner *yaml.Node
}

// Write is how a record of a node is created, updated, patched or deleted.
type Write struct {
	Kind string
	// None says the API cannot do it (create: none): the instance must hold
	// the records.
	None                bool
	Call                string
	Path, Query, Header map[string]any
	// Body is sent as it is when set; else it is built from the record (or
	// from the record of the node From names).
	Body    any
	HasBody bool
	From    string
	// Change sets fields of an update or patch: field → value, or "auto"
	// for one changed field.
	Change map[string]any
	Expect int
	Off    bool
	// Gone marks a call the spec no longer has.
	Gone bool
	// Comment is written after the call (the operationId); Note explains
	// none or off.
	Comment, Note string

	Line  int
	value *yaml.Node
}

// Page says how a node reads further pages.
type Page struct {
	// Param is the query parameter (or, with In "body", the JSON pointer
	// into the body) of the page number or offset.
	Param string
	In    string
	// Start is the first value (default: the default or minimum of the
	// parameter, else 1 for a number and 0 for an offset); Step the
	// increase per page (default 1).
	Start *int
	Step  int
	// Size sets the page size: parameter → value.
	Size map[string]any
	// Next is the JSON pointer of the cursor in the answer; the cursor is
	// sent in Param.
	Next string
	// Max is the number of pages read at most (default 1).
	Max int
}

// Pick says which records of a node are taken.
type Pick struct {
	// Keys are the values of the first key of the records to take.
	Keys []any
	// N takes N records: at random (seeded), or the first N with First.
	N     int
	First bool
	All   bool
}

// Clause is one condition of where: the field (a dot path) and the test.
type Clause struct {
	Field string
	Test  any
	// Note says why the generator proposed it.
	Note string
}

// FindingError lists the problems of a file.
type FindingError struct{ Problems []string }

func (e *FindingError) Error() string { return strings.Join(e.Problems, "\n") }

// Parse reads a tree file.
func Parse(b []byte) (*Tree, error) {
	doc, err := yamldoc.Parse(b)
	if err != nil {
		return nil, err
	}
	return fromDoc(doc)
}

func fromDoc(doc *yamldoc.Doc) (*Tree, error) {
	t := &Tree{doc: doc, Budget: DefaultBudget}
	p := &parser{}
	root := doc.Root
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		switch k.Value {
		case "params":
			t.Params = map[string]any{}
			if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
				continue
			}
			if v.Kind != yaml.MappingNode {
				p.errf(v, "params must be a mapping of names to values")
				continue
			}
			for j := 0; j+1 < len(v.Content); j += 2 {
				name := v.Content[j].Value
				if !nameForm(name) {
					p.errf(v.Content[j], "params: %q: letters, digits, _ and - only", name)
				}
				x, err := yamldoc.Decode(v.Content[j+1])
				if err != nil {
					p.errf(v.Content[j+1], "params.%s: %v", name, err)
				}
				t.Params[name] = x
				t.ParamNames = append(t.ParamNames, name)
			}
		case "scope":
			t.Scope = p.scope(v)
		case "seed":
			n, err := strconv.ParseUint(v.Value, 10, 64)
			if err != nil {
				p.errf(v, "seed must be a number")
			}
			t.Seed = n
		case "budget":
			n, err := strconv.Atoi(v.Value)
			if err != nil || n <= 0 {
				p.errf(v, "budget must be a positive number of requests")
			}
			t.Budget = n
		case "tree":
			t.Roots = p.nodes(v, nil)
		case "unplaced":
			// files of an earlier version: a merge moves these reads into
			// the tree and their missing values into params
			t.legacy = p.nodes(v, nil)
		case "ignore":
			if v.Kind != yaml.SequenceNode {
				p.errf(v, "ignore must be a list of operations")
				continue
			}
			for _, c := range v.Content {
				t.Ignore = append(t.Ignore, strings.TrimSpace(c.Value))
			}
		default:
			p.errf(k, "unknown key %q (known: scope, params, seed, budget, tree, ignore)", k.Value)
		}
	}
	p.uniqueNames(t)
	if len(p.problems) > 0 {
		return t, &FindingError{Problems: p.problems}
	}
	return t, nil
}

type parser struct{ problems []string }

func (p *parser) errf(n *yaml.Node, format string, args ...any) {
	p.problems = append(p.problems, fmt.Sprintf("line %d: %s", n.Line, fmt.Sprintf(format, args...)))
}

var nodeKeys = []string{"call", "path", "query", "header", "body", "read", "items", "page", "pick", "candidates",
	"min", "where", "require", "key", "save", "write", "each", "off"}

func (p *parser) scope(v *yaml.Node) Scope {
	var sc Scope
	if v.Kind != yaml.MappingNode {
		p.errf(v, "scope must be a mapping (tags, exclude, deps)")
		return sc
	}
	list := func(n *yaml.Node, where string) []string {
		switch n.Kind {
		case yaml.ScalarNode:
			if n.Tag == "!!null" {
				return nil
			}
			return []string{n.Value}
		case yaml.SequenceNode:
			var out []string
			for _, c := range n.Content {
				out = append(out, strings.TrimSpace(c.Value))
			}
			return out
		}
		p.errf(n, "%s must be a list", where)
		return nil
	}
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, val := v.Content[i], v.Content[i+1]
		switch k.Value {
		case "tags":
			sc.Tags = list(val, "scope.tags")
		case "deps":
			sc.Deps = p.boolean(val, "scope.deps")
		case "exclude":
			if val.Kind != yaml.MappingNode {
				p.errf(val, "scope.exclude must be a mapping (tags, ops)")
				continue
			}
			for j := 0; j+1 < len(val.Content); j += 2 {
				switch val.Content[j].Value {
				case "tags":
					sc.ExcludeTags = list(val.Content[j+1], "scope.exclude.tags")
				case "ops":
					sc.ExcludeOps = list(val.Content[j+1], "scope.exclude.ops")
				default:
					p.errf(val.Content[j], "scope.exclude: unknown key %q (known: tags, ops)", val.Content[j].Value)
				}
			}
		default:
			p.errf(k, "scope: unknown key %q (known: tags, exclude, deps)", k.Value)
		}
	}
	return sc
}

func (p *parser) nodes(m *yaml.Node, parent *Node) []*Node {
	if m.Kind == yaml.ScalarNode && m.Tag == "!!null" {
		return nil
	}
	if m.Kind != yaml.MappingNode {
		p.errf(m, "expected a mapping of node names to nodes")
		return nil
	}
	var out []*Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if n := p.node(k.Value, v, parent); n != nil {
			n.Line, n.key, n.owner = k.Line, k, m
			out = append(out, n)
		}
	}
	return out
}

func (p *parser) node(name string, v *yaml.Node, parent *Node) *Node {
	n := &Node{Name: name, Parent: parent, value: v}
	if !nameForm(name) {
		p.errf(v, "node name %q: letters, digits, _ and - only", name)
	}
	if v.Kind == yaml.ScalarNode {
		call, params, err := shortForm(v.Value)
		if err != nil {
			p.errf(v, "%s: %v", name, err)
			return nil
		}
		n.Call, n.Path, n.Gone = call, params, gone(v)
		return n
	}
	if v.Kind != yaml.MappingNode {
		p.errf(v, "%s: a node is a request (\"GET /path p=.field\") or a mapping", name)
		return nil
	}
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, val := v.Content[i], v.Content[i+1]
		switch k.Value {
		case "call":
			call, params, err := shortForm(val.Value)
			if err != nil {
				p.errf(val, "%s.call: %v", name, err)
				continue
			}
			n.Call, n.Gone = call, gone(val)
			if len(params) > 0 {
				n.Path = merged(n.Path, params)
			}
		case "path", "query", "header":
			m, ok := p.valueMap(val, name+"."+k.Value)
			if !ok {
				continue
			}
			switch k.Value {
			case "path":
				n.Path = merged(n.Path, m)
			case "query":
				n.Query = m
			default:
				n.Header = m
			}
		case "body":
			b, err := yamldoc.Decode(val)
			if err != nil {
				p.errf(val, "%s.body: %v", name, err)
			}
			n.Body, n.HasBody = b, true
		case "read":
			n.Read = p.boolean(val, name+".read")
		case "off":
			n.Off = p.boolean(val, name+".off")
		case "items":
			n.Items, n.HasItems = val.Value, true
			if n.Items != "" && !strings.HasPrefix(n.Items, "/") {
				p.errf(val, "%s.items: a JSON pointer (/content), or \"\" for the answer itself", name)
			}
		case "page":
			n.Page = p.page(val, name)
		case "pick":
			n.Pick = p.pick(val, name)
		case "candidates":
			c, err := strconv.Atoi(val.Value)
			if err != nil || c <= 0 {
				p.errf(val, "%s.candidates must be a positive number", name)
			}
			n.Candidates = c
		case "min":
			c, err := strconv.Atoi(val.Value)
			if err != nil || c < 0 {
				p.errf(val, "%s.min must be a number of elements", name)
			}
			n.Min = c
		case "where":
			n.Where, n.WhereAny = p.where(val, name)
		case "require":
			for _, r := range p.nodes(val, n) {
				r.IsRequire = true
				n.Require = append(n.Require, r)
			}
		case "key":
			switch val.Kind {
			case yaml.ScalarNode:
				n.Key = []string{val.Value}
			case yaml.SequenceNode:
				for _, c := range val.Content {
					n.Key = append(n.Key, c.Value)
				}
			}
			for _, k := range n.Key {
				if !strings.HasPrefix(k, "/") {
					p.errf(val, "%s.key: %q is no JSON pointer (/id)", name, k)
				}
			}
		case "save":
			n.Save = map[string]string{}
			for j := 0; j+1 < len(val.Content); j += 2 {
				src := val.Content[j+1].Value
				if !strings.HasPrefix(src, "/") && !strings.HasPrefix(src, "header ") {
					p.errf(val.Content[j+1], "%s.save.%s: a JSON pointer (/id) or \"header <Name>\"", name, val.Content[j].Value)
				}
				n.Save[val.Content[j].Value] = src
			}
		case "write":
			n.Write = p.writes(val, name)
		case "each":
			n.Each = p.nodes(val, n)
		default:
			p.errf(k, "%s: unknown key %q (known: %s)", name, k.Value, strings.Join(nodeKeys, ", "))
		}
	}
	if n.Call == "" {
		p.errf(v, "%s: call is missing (\"GET /path\")", name)
	}
	return n
}

func merged(a, b map[string]any) map[string]any {
	if a == nil {
		a = map[string]any{}
	}
	for k, v := range b {
		a[k] = v
	}
	return a
}

func (p *parser) valueMap(v *yaml.Node, where string) (map[string]any, bool) {
	if v.Kind != yaml.MappingNode {
		p.errf(v, "%s must be a mapping of names to values", where)
		return nil, false
	}
	x, err := yamldoc.Decode(v)
	if err != nil {
		p.errf(v, "%s: %v", where, err)
		return nil, false
	}
	m, _ := x.(map[string]any)
	return m, true
}

func (p *parser) boolean(v *yaml.Node, where string) bool {
	switch v.Value {
	case "true":
		return true
	case "false":
		return false
	}
	p.errf(v, "%s must be true or false", where)
	return false
}

func (p *parser) page(v *yaml.Node, name string) *Page {
	if v.Kind != yaml.MappingNode {
		p.errf(v, "%s.page must be a mapping (param, max, size, next, start, step, in)", name)
		return nil
	}
	pg := &Page{In: "query", Max: 1, Step: 1}
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, val := v.Content[i], v.Content[i+1]
		num := func() int {
			n, err := strconv.Atoi(val.Value)
			if err != nil {
				p.errf(val, "%s.page.%s must be a number", name, k.Value)
			}
			return n
		}
		switch k.Value {
		case "param":
			pg.Param = val.Value
		case "in":
			pg.In = val.Value
			if pg.In != "query" && pg.In != "body" {
				p.errf(val, "%s.page.in: query or body", name)
			}
		case "start":
			n := num()
			pg.Start = &n
		case "step":
			pg.Step = num()
		case "max":
			pg.Max = num()
		case "next":
			pg.Next = val.Value
		case "size":
			pg.Size, _ = p.valueMap(val, name+".page.size")
		default:
			p.errf(k, "%s.page: unknown key %q (known: param, in, start, step, size, next, max)", name, k.Value)
		}
	}
	if pg.Param == "" {
		p.errf(v, "%s.page.param is missing: the parameter of the page number, offset or cursor", name)
	}
	if pg.Max < 1 {
		p.errf(v, "%s.page.max must be at least 1", name)
	}
	return pg
}

func (p *parser) pick(v *yaml.Node, name string) Pick {
	switch v.Kind {
	case yaml.ScalarNode:
		if v.Value == "all" {
			return Pick{All: true}
		}
		n, err := strconv.Atoi(v.Value)
		if err != nil || n < 1 {
			p.errf(v, "%s.pick: a number, all, {first: n} or a list of keys", name)
		}
		return Pick{N: n}
	case yaml.SequenceNode:
		var keys []any
		for _, c := range v.Content {
			x, _ := yamldoc.Decode(c)
			keys = append(keys, x)
		}
		return Pick{Keys: keys}
	case yaml.MappingNode:
		if f := yamldoc.Get(v, "first"); f != nil && len(v.Content) == 2 {
			n, err := strconv.Atoi(f.Value)
			if err == nil && n > 0 {
				return Pick{N: n, First: true}
			}
		}
	}
	p.errf(v, "%s.pick: a number, all, {first: n} or a list of keys", name)
	return Pick{}
}

func (p *parser) where(v *yaml.Node, name string) (all, anyOf []Clause) {
	if v.Kind != yaml.MappingNode {
		p.errf(v, "%s.where must be a mapping of fields to conditions", name)
		return nil, nil
	}
	clauses := func(m *yaml.Node) []Clause {
		var out []Clause
		for i := 0; i+1 < len(m.Content); i += 2 {
			x, err := yamldoc.Decode(m.Content[i+1])
			if err != nil {
				p.errf(m.Content[i+1], "%s.where: %v", name, err)
				continue
			}
			out = append(out, Clause{Field: m.Content[i].Value, Test: x, Note: noteOf(m.Content[i+1])})
		}
		return out
	}
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, val := v.Content[i], v.Content[i+1]
		if k.Value == "any" && val.Kind == yaml.MappingNode {
			anyOf = append(anyOf, clauses(val)...)
			continue
		}
		x, err := yamldoc.Decode(val)
		if err != nil {
			p.errf(val, "%s.where: %v", name, err)
			continue
		}
		all = append(all, Clause{Field: k.Value, Test: x, Note: noteOf(val)})
	}
	return all, anyOf
}

var writeKeys = []string{"call", "path", "query", "header", "body", "change", "expect", "off"}

func (p *parser) writes(v *yaml.Node, name string) map[string]*Write {
	if v.Kind != yaml.MappingNode {
		p.errf(v, "%s.write must be a mapping (create, update, patch, delete)", name)
		return nil
	}
	out := map[string]*Write{}
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, val := v.Content[i], v.Content[i+1]
		if !nameForm(k.Value) {
			p.errf(k, "%s.write: %q: create, update, patch, delete, or the name of an action (letters, digits, _ and -)", name, k.Value)
			continue
		}
		w := &Write{Kind: k.Value, Line: k.Line, value: val}
		out[k.Value] = w
		where := name + ".write." + k.Value
		if val.Kind == yaml.ScalarNode {
			if val.Value == "none" && k.Value != WriteCreate {
				p.errf(val, "%s: only create can be none; leave the write out, or set off: true", where)
				continue
			}
			if val.Value == "none" {
				w.None = true
				continue
			}
			call, params, err := shortForm(val.Value)
			if err != nil {
				p.errf(val, "%s: %v", where, err)
				continue
			}
			w.Call, w.Path, w.Gone = call, params, gone(val)
			continue
		}
		if val.Kind != yaml.MappingNode {
			p.errf(val, "%s: none, a request (\"POST /path p=.field\") or a mapping", where)
			continue
		}
		for j := 0; j+1 < len(val.Content); j += 2 {
			wk, wv := val.Content[j], val.Content[j+1]
			switch wk.Value {
			case "call":
				call, params, err := shortForm(wv.Value)
				if err != nil {
					p.errf(wv, "%s.call: %v", where, err)
					continue
				}
				w.Call, w.Gone = call, gone(wv)
				if len(params) > 0 {
					w.Path = merged(w.Path, params)
				}
			case "path", "query", "header":
				m, ok := p.valueMap(wv, where+"."+wk.Value)
				if !ok {
					continue
				}
				switch wk.Value {
				case "path":
					w.Path = merged(w.Path, m)
				case "query":
					w.Query = m
				default:
					w.Header = m
				}
			case "body":
				if wv.Kind == yaml.ScalarNode && strings.HasPrefix(wv.Value, "from ") {
					w.From = strings.TrimSpace(strings.TrimPrefix(wv.Value, "from "))
					continue
				}
				b, err := yamldoc.Decode(wv)
				if err != nil {
					p.errf(wv, "%s.body: %v", where, err)
				}
				w.Body, w.HasBody = b, true
			case "change":
				if wv.Kind == yaml.ScalarNode && wv.Value == "auto" {
					w.Change = map[string]any{}
					continue
				}
				w.Change, _ = p.valueMap(wv, where+".change")
			case "expect":
				n, err := strconv.Atoi(wv.Value)
				if err != nil || n < 100 || n > 599 {
					p.errf(wv, "%s.expect must be an HTTP status", where)
				}
				w.Expect = n
			case "off":
				w.Off = p.boolean(wv, where+".off")
			default:
				p.errf(wk, "%s: unknown key %q (known: %s)", where, wk.Value, strings.Join(writeKeys, ", "))
			}
		}
		if w.Call == "" {
			p.errf(val, "%s: call is missing", where)
		}
	}
	return out
}

// uniqueNames checks that node names are unique in the whole file: a
// reference "<node>.field" and the names of the records rely on it.
func (p *parser) uniqueNames(t *Tree) {
	seen := map[string]int{}
	t.Walk(func(n *Node) {
		if n.IsRequire {
			return
		}
		if line, ok := seen[n.Name]; ok {
			p.problems = append(p.problems, fmt.Sprintf("line %d: node name %q is used twice (first in line %d); names must be unique in the file", n.Line, n.Name, line))
			return
		}
		seen[n.Name] = n.Line
	})
}

func nameForm(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '_' && r != '-' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// shortForm reads "GET /docks/{dockId} dockId=.id": the call and the
// parameter values after it.
func shortForm(s string) (string, map[string]any, error) {
	f := strings.Fields(s)
	switch {
	case len(f) == 0:
		return "", nil, errors.New("empty request")
	case len(f) == 1:
		return f[0], nil, nil // an operationId
	}
	call := strings.ToUpper(f[0]) + " " + f[1]
	if !strings.HasPrefix(f[1], "/") {
		return "", nil, fmt.Errorf("%q: write \"METHOD /path name=value …\" or an operationId", s)
	}
	var params map[string]any
	for _, kv := range f[2:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return "", nil, fmt.Errorf("%q: %q is no name=value", s, kv)
		}
		if params == nil {
			params = map[string]any{}
		}
		params[k] = v
	}
	return call, params, nil
}

// Walk visits every node of the tree, parents first; requires are visited
// after their node.
func (t *Tree) Walk(fn func(*Node)) {
	var walk func([]*Node)
	walk = func(list []*Node) {
		for _, n := range list {
			fn(n)
			for _, r := range n.Require {
				fn(r)
			}
			walk(n.Each)
		}
	}
	walk(t.Roots)
}

// Node returns the node of the tree with the name.
func (t *Tree) Node(name string) *Node {
	var out *Node
	var walk func([]*Node)
	walk = func(list []*Node) {
		for _, n := range list {
			if out != nil {
				return
			}
			if n.Name == name {
				out = n
				return
			}
			walk(n.Each)
		}
	}
	walk(t.Roots)
	return out
}

// names reports whether a name is a node of the tree (no require): the
// scope of "<node>.field".
func (t *Tree) names() func(string) bool {
	set := map[string]bool{}
	var walk func([]*Node)
	walk = func(list []*Node) {
		for _, n := range list {
			set[n.Name] = true
			walk(n.Each)
		}
	}
	walk(t.Roots)
	return func(name string) bool { return set[name] }
}

// Ancestor returns the nearest ancestor of n (n itself included) with the
// name, or nil.
func (n *Node) Ancestor(name string) *Node {
	for x := n; x != nil; x = x.Parent {
		if x.Name == name {
			return x
		}
	}
	return nil
}

// PathName is the names from the root down to n, joined by "/".
func (n *Node) PathName() string {
	var parts []string
	for x := n; x != nil; x = x.Parent {
		parts = append([]string{x.Name}, parts...)
	}
	return strings.Join(parts, "/")
}

// Enabled reports whether a fetch runs the node: neither it nor a parent is
// switched off or gone.
func (n *Node) Enabled() bool {
	for x := n; x != nil; x = x.Parent {
		if x.Off || x.Gone {
			return false
		}
	}
	return true
}

// noteOf is the comment after a value, without "#".
func noteOf(v *yaml.Node) string {
	return strings.TrimSpace(strings.TrimPrefix(v.LineComment, "#"))
}

// gone reports whether the comment after a call marks it as gone.
func gone(v *yaml.Node) bool {
	return strings.HasPrefix(v.LineComment, "# gone")
}

// Method is the method of a "METHOD /path" call, "" for an operationId.
func Method(call string) string {
	m, _, ok := strings.Cut(call, " ")
	if !ok {
		return ""
	}
	return m
}

// Load reads a tree file.
func Load(path string) (*Tree, error) {
	doc, err := yamldoc.Load(path)
	if err != nil {
		return nil, err
	}
	return fromDoc(doc)
}

// Bytes renders the file as it was read, with the changes of a merge.
func (t *Tree) Bytes() ([]byte, error) {
	if t.doc == nil {
		return t.Encode()
	}
	return t.doc.Bytes()
}
