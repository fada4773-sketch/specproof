package tree

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// DefaultPick is how many records a proposed list node picks.
const DefaultPick = 3

// maxDepth limits how deep the proposal nests nodes.
const maxDepth = 5

// Propose builds the tree the spec suggests from its resources: a root
// per resource with a list of its own, below it the read of a record, the
// other reads that take its key, the resources that belong to it and the
// records its fields refer to. Each resource node gets the write block of
// its resource. Reads the generator cannot hang anywhere go to unplaced,
// switched off.
func Propose(s *spec.Spec) *Tree {
	p := &proposer{s: s, m: model.Detect(s, nil), used: map[*spec.Operation]bool{}, names: map[string]int{}}
	t := &Tree{Seed: 1, Budget: DefaultBudget}
	for _, r := range p.m.Resources {
		if r.Parent != nil {
			continue
		}
		list := p.freeList(r)
		if list == nil {
			continue
		}
		t.Roots = append(t.Roots, p.listNode(r, list, nil, nil))
	}
	for _, r := range p.m.Resources {
		if r.Parent != nil || p.freeList(r) != nil {
			continue
		}
		// a resource without a list of its own: its reads, unplaced
		for _, mo := range r.OpsWith(model.RoleRead) {
			if !p.used[mo.Op] {
				t.Unplaced = append(t.Unplaced, p.unplaced(mo.Op, "no list of "+r.Name+" without path parameters, and no node above it; give its parameters values and move it into the tree"))
			}
		}
	}
	for _, op := range s.Ops {
		if op.Method != http.MethodGet || p.used[op] {
			continue
		}
		if len(pathParams(op)) == 0 && !requiredQuery(op) {
			// a read of its own (counters, settings): a root
			p.used[op] = true
			n := &Node{Name: p.name(viewName(op)), Call: callOf(op), Comment: op.ID}
			if isList(op) {
				n.Pick = Pick{N: DefaultPick}
			}
			t.Roots = append(t.Roots, n)
			continue
		}
		t.Unplaced = append(t.Unplaced, p.unplaced(op, "no node gives its path parameters; give them values and move it into the tree"))
	}
	return t
}

// refers reports whether a field of r refers to t.
func (p *proposer) refers(r, t *model.Resource) bool {
	for _, f := range r.Relations {
		if x, _ := r.Ref(f); x == t {
			return true
		}
	}
	return false
}

type proposer struct {
	s     *spec.Spec
	m     *model.Model
	used  map[*spec.Operation]bool
	names map[string]int
}

// name returns a node name unique in the file.
func (p *proposer) name(base string) string {
	base = strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || unicode.IsLetter(r) && r < unicode.MaxASCII || unicode.IsDigit(r) && r < unicode.MaxASCII {
			return r
		}
		return '-'
	}, base)
	base = strings.Trim(base, "-")
	if base == "" {
		base = "node"
	}
	p.names[base]++
	if n := p.names[base]; n > 1 {
		return fmt.Sprintf("%s%d", base, n)
	}
	return base
}

// freeList is the list of a resource that needs no path parameter.
func (p *proposer) freeList(r *model.Resource) *spec.Operation {
	for _, mo := range r.OpsWith(model.RoleList) {
		if len(pathParams(mo.Op)) == 0 && !requiredQuery(mo.Op) {
			return mo.Op
		}
	}
	return nil
}

func requiredQuery(op *spec.Operation) bool {
	for _, prm := range op.Params {
		if prm.In == openapi3.ParameterInQuery && prm.Required {
			if _, ok := paramExample(prm); !ok {
				return true
			}
		}
	}
	return false
}

// chain is the resources of the nodes from the root down to a node, with
// their names: the scope of references.
type chain struct {
	res  *model.Resource
	node *Node
	up   *chain
}

func (c *chain) find(r *model.Resource) *chain {
	for x := c; x != nil; x = x.up {
		if x.res == r {
			return x
		}
	}
	return nil
}

func (c *chain) depth() int {
	n := 0
	for x := c; x != nil; x = x.up {
		n++
	}
	return n
}

// listNode is the node of a resource read through a list.
func (p *proposer) listNode(r *model.Resource, list *spec.Operation, parent *Node, up *chain) *Node {
	p.used[list] = true
	n := &Node{Name: p.name(listName(list, r)), Call: callOf(list), Comment: list.ID, Pick: Pick{N: DefaultPick}, Parent: parent}
	n.Path, n.Off, n.Note = p.paramValues(list, up, nil)
	if pg := pageOf(list); pg != nil {
		n.Page = pg
	}
	n.Key = keyPointers(r)
	here := &chain{res: r, node: n, up: up}
	n.Write = p.writes(r, here)
	p.children(n, r, here)
	return n
}

// children adds below a node of resource r: the read of a record, other
// reads that take its key, the resources below it and the records its
// fields refer to.
func (p *proposer) children(n *Node, r *model.Resource, here *chain) {
	if here.depth() > maxDepth {
		return
	}
	for _, mo := range r.OpsWith(model.RoleRead) {
		if p.used[mo.Op] || !p.reachable(mo.Op, here) {
			continue
		}
		p.used[mo.Op] = true
		c := &Node{Name: p.name(lowerFirst(r.Name)), Call: callOf(mo.Op), Comment: mo.Op.ID, Parent: n}
		c.Path, c.Off, c.Note = p.paramValues(mo.Op, here, nil)
		n.Each = append(n.Each, c)
	}
	// other reads whose path parameters this node and those above it give
	for _, op := range p.s.Ops {
		if op.Method != http.MethodGet || p.used[op] || len(pathParams(op)) == 0 || !p.reachable(op, here) {
			continue
		}
		mo := p.m.Op(op)
		if mo != nil && mo.Resource != r && mo.Resource.Parent == r && mo.Role == model.RoleList {
			continue // a resource below r, added as its own node
		}
		if mo != nil && mo.Resource != r && mo.Role == model.RoleRead && p.refers(r, mo.Resource) {
			continue // the record of another resource, added by its reference
		}
		if !p.takesKey(op, r) {
			continue
		}
		p.used[op] = true
		c := &Node{Name: p.name(viewName(op)), Call: callOf(op), Comment: op.ID, Parent: n}
		c.Path, c.Off, c.Note = p.paramValues(op, here, nil)
		if isList(op) {
			c.Pick = Pick{N: DefaultPick}
		}
		n.Each = append(n.Each, c)
	}
	for _, child := range p.m.Resources {
		if child.Parent != r || here.find(child) != nil {
			continue
		}
		for _, mo := range child.OpsWith(model.RoleList) {
			if !p.used[mo.Op] && p.reachable(mo.Op, here) {
				n.Each = append(n.Each, p.listNode(child, mo.Op, n, here))
				break
			}
		}
	}
	for _, field := range r.Relations {
		t, _ := r.Ref(field)
		if t == nil || here.find(t) != nil {
			continue
		}
		reads := t.OpsWith(model.RoleRead)
		if len(reads) == 0 {
			continue
		}
		op := reads[0].Op
		params := pathParams(op)
		if len(params) != 1 {
			continue
		}
		p.used[op] = true
		// the record the field names; it creates like the node of its
		// resource (no write block of its own)
		name := lowerFirst(t.Name)
		if p.names[name] > 0 {
			name = lowerFirst(singular(r.Name)) + t.Name
		}
		c := &Node{Name: p.name(name), Call: callOf(op), Comment: op.ID, Parent: n,
			Path: map[string]any{params[0]: "." + field}}
		c.Key = keyPointers(t)
		next := &chain{res: t, node: c, up: here}
		p.children(c, t, next)
		n.Each = append(n.Each, c)
	}
}

// reachable reports whether every path parameter of op is a key of a
// resource on the chain.
func (p *proposer) reachable(op *spec.Operation, here *chain) bool {
	_, off, _ := p.paramValues(op, here, nil)
	return !off
}

// takesKey reports whether one of op's path parameters is a key of r.
func (p *proposer) takesKey(op *spec.Operation, r *model.Resource) bool {
	if mo := p.m.Op(op); mo != nil {
		for _, prm := range mo.Params {
			if prm.Resource == r {
				return true
			}
		}
	}
	seg := model.SegmentBefore(op.Path, pathParams(op)[len(pathParams(op))-1])
	return strings.EqualFold(singular(seg), r.Name) || strings.EqualFold(seg, r.Name)
}

// paramValues gives each path parameter of op a reference: to the record
// the node runs under (".field") or to the node of the resource above it
// ("<node>.field"). off is set when a parameter has none.
func (p *proposer) paramValues(op *spec.Operation, here *chain, self *model.Resource) (map[string]any, bool, string) {
	params := pathParams(op)
	if len(params) == 0 {
		return nil, false, ""
	}
	out := map[string]any{}
	mo := p.m.Op(op)
	var missing []string
	for _, name := range params {
		var res *model.Resource
		field := ""
		if mo != nil {
			if mp := mo.Param(name); mp != nil {
				res, field = mp.Resource, mp.Field
			}
		}
		switch c := here.find(res); {
		case res != nil && self != nil && res == self:
			out[name] = "." + field
		case c != nil && here != nil && c == here && self == nil:
			out[name] = "." + field
		case c != nil:
			out[name] = c.node.Name + "." + field
		default:
			// a field of the record above with the parameter's name
			if here != nil && here.res != nil && here.res.Field(name) != "" && self == nil {
				out[name] = "." + here.res.Field(name)
				continue
			}
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return out, true, fmt.Sprintf("guess: no record gives {%s}", strings.Join(missing, "}, {"))
	}
	return out, false, ""
}

// writes is the write block of a resource: its create, update, patch and
// delete; create: none when the API has no create.
func (p *proposer) writes(r *model.Resource, here *chain) map[string]*Write {
	out := map[string]*Write{}
	add := func(kind string, mo *model.Op) {
		if _, ok := out[kind]; ok || mo == nil {
			return
		}
		w := &Write{Kind: kind, Call: callOf(mo.Op), Comment: mo.Op.ID}
		var off bool
		var why string
		w.Path, off, why = p.paramValues(mo.Op, here.up, r)
		if kind == WriteCreate {
			// the key of the record does not exist yet; others come from above
			w.Path, off, why = p.paramValues(mo.Op, here.up, nil)
			if here.up != nil {
				for k, v := range w.Path {
					if s, ok := v.(string); ok && strings.HasPrefix(s, ".") {
						w.Path[k] = here.up.node.Name + s
					}
				}
			}
		}
		if off {
			w.Off, w.Note = true, why
		}
		out[kind] = w
	}
	creates := r.OpsWith(model.RoleCreate)
	if len(creates) == 0 {
		out[WriteCreate] = &Write{Kind: WriteCreate, None: true, Note: "no create of " + r.Name + " in the spec: the instance must hold these records"}
	} else {
		add(WriteCreate, creates[0])
	}
	for _, mo := range r.OpsWith(model.RoleUpdate) {
		switch mo.Op.Method {
		case http.MethodPatch:
			add(WritePatch, mo)
		default:
			add(WriteUpdate, mo)
		}
	}
	if d := r.OpsWith(model.RoleDelete); len(d) > 0 {
		add(WriteDelete, d[0])
	}
	return out
}

func (p *proposer) unplaced(op *spec.Operation, why string) *Node {
	p.used[op] = true
	n := &Node{Name: p.name(viewName(op)), Call: callOf(op), Comment: op.ID, Off: true, Note: why}
	if params := pathParams(op); len(params) > 0 {
		n.Path = map[string]any{}
		for _, prm := range params {
			n.Path[prm] = "." + prm
		}
	}
	return n
}

// pageOf proposes the paging of a list: a page or offset query parameter.
func pageOf(op *spec.Operation) *Page {
	for _, prm := range op.Params {
		if prm.In != openapi3.ParameterInQuery {
			continue
		}
		switch strings.ToLower(prm.Name) {
		case "page", "pagenumber", "pageindex", "offset", "skip":
			pg := &Page{Param: prm.Name, In: "query", Max: 1, Step: 1}
			for _, sz := range op.Params {
				switch strings.ToLower(sz.Name) {
				case "pagesize", "size", "limit", "per_page", "perpage", "page_size":
					n := 100.0
					if sz.Schema != nil && sz.Schema.Value != nil && sz.Schema.Value.Max != nil && *sz.Schema.Value.Max < 500 {
						n = *sz.Schema.Value.Max
					}
					pg.Size = map[string]any{sz.Name: int(n)}
					if l := strings.ToLower(prm.Name); l == "offset" || l == "skip" {
						pg.Step = int(n)
					}
				}
			}
			return pg
		}
	}
	return nil
}

// keyPointers are the key fields of a resource as JSON pointers.
func keyPointers(r *model.Resource) []string {
	var out []string
	for _, k := range r.Keys {
		out = append(out, "/"+k)
	}
	if len(out) == 0 {
		if f := r.Field("id"); f != "" {
			out = []string{"/" + f}
		}
	}
	return out
}

// listName names a list node after the last word of its path ("docks"),
// else after the resource.
func listName(op *spec.Operation, r *model.Resource) string {
	if seg := lastLiteral(op.Path); seg != "" && !slices.Contains([]string{"list", "all", "search"}, strings.ToLower(seg)) {
		return lowerFirst(seg)
	}
	return lowerFirst(r.Name) + "s"
}

// viewName names a read after the last word of its path.
func viewName(op *spec.Operation) string {
	if seg := lastLiteral(op.Path); seg != "" {
		return lowerFirst(seg)
	}
	return lowerFirst(op.ID)
}

func lastLiteral(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if s := segs[i]; s != "" && !strings.HasPrefix(s, "{") && !slices.Contains([]string{"id", "ids", "code", "key", "uuid", "by-id", "byid", "name"}, strings.ToLower(s)) {
			return s
		}
	}
	return ""
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}
