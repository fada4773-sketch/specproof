package tree

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/dict"
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
// records its fields refer to. A read whose path parameters no node above
// gives hangs below the node that gives them, across the whole tree
// ({bookId} below the node of books, wherever it is). A value no node
// gives becomes "$name", listed under params at the top of the file; the
// read hangs below the nodes that give its other values, else at the top.
// Each resource gets the write block of its resource once, with the body
// taken from the read that covers its request best; every other write
// goes to the node whose records it writes.
func Propose(s *spec.Spec) *Tree {
	p := &proposer{s: s, m: model.Detect(s, nil), used: map[*spec.Operation]bool{}, names: map[string]int{},
		res: map[*Node]*model.Resource{}, written: map[*spec.Operation]bool{}}
	t := &Tree{Seed: 1, Budget: DefaultBudget, Params: map[string]any{}, ParamNotes: map[string]string{}}
	p.t = t
	for _, r := range p.m.Resources {
		if r.Parent != nil {
			continue
		}
		if list := p.freeList(r); list != nil {
			t.Roots = append(t.Roots, p.listNode(r, list, nil, nil))
		}
	}
	for _, op := range s.Ops {
		if op.Method == http.MethodGet && !p.used[op] && len(needs(op)) == 0 {
			// a read of its own (counters, settings): a root
			p.used[op] = true
			n := &Node{Name: p.name(viewName(op), op), Call: callOf(op), Comment: op.ID}
			if isList(op) {
				n.Pick = Pick{N: DefaultPick}
			}
			p.res[n] = p.resourceOf(op)
			t.Roots = append(t.Roots, n)
		}
	}
	p.placeRest()
	p.addWrites()
	p.attachWrites()
	for name, ops := range p.paramOps {
		t.ParamNotes[name] = usedBy(ops)
	}
	return t
}

// usedBy names the operations that use a value of params.
func usedBy(ops []string) string {
	if len(ops) > 4 {
		return fmt.Sprintf("%s and %d more", strings.Join(ops[:4], ", "), len(ops)-4)
	}
	return strings.Join(ops, ", ")
}

// param is the value "$name" of params for a parameter no node gives; it
// adds the entry.
func (p *proposer) param(name string, op *spec.Operation) string {
	if _, ok := p.t.Params[name]; !ok {
		p.t.Params[name] = nil
		p.t.ParamNames = append(p.t.ParamNames, name)
	}
	if p.paramOps == nil {
		p.paramOps = map[string][]string{}
	}
	if !slices.Contains(p.paramOps[name], op.ID) {
		p.paramOps[name] = append(p.paramOps[name], op.ID)
	}
	return "$" + name
}

type proposer struct {
	s     *spec.Spec
	m     *model.Model
	t     *Tree
	used  map[*spec.Operation]bool
	names map[string]int
	// res is the resource of each node, nil for a read of no resource.
	res map[*Node]*model.Resource
	// home is the node that holds the write block of a resource.
	home map[*model.Resource]*Node
	// written are the writes the tree has.
	written map[*spec.Operation]bool
	// paramOps are the operations that use a value of params.
	paramOps map[string][]string
}

func (p *proposer) resourceOf(op *spec.Operation) *model.Resource {
	return resourceOf(p.m, op)
}

// resourceOf is the resource of a request: the model's, else (a search, a
// POST that only reads) the resource whose schema its answer lists.
func resourceOf(m *model.Model, op *spec.Operation) *model.Resource {
	mo := m.Op(op)
	if !isSearch(mo, op) {
		if mo != nil {
			return mo.Resource
		}
		return nil
	}
	ref := responseRef(op, lowestSuccess(op))
	if ref == nil || ref.Value == nil {
		return nil
	}
	item := ref
	if ptr, ok := listPointer(op); ok {
		switch {
		case ptr == "" && ref.Value.Items != nil:
			item = ref.Value.Items
		case ptr != "":
			props, _ := dict.Properties(ref.Value)
			for k, v := range props {
				if strings.EqualFold(k, strings.TrimPrefix(ptr, "/")) && v != nil && v.Value != nil && v.Value.Items != nil {
					item = v.Value.Items
				}
			}
		}
	}
	dto := dict.DTORef(item)
	for _, r := range m.Resources {
		if slices.Contains(r.Schemas, dto) {
			return r
		}
	}
	return nil
}

// isSearch reports whether op is a POST that answers with a list and
// creates nothing.
func isSearch(mo *model.Op, op *spec.Operation) bool {
	return op.Method == http.MethodPost && isList(op) && (mo == nil || mo.Role != model.RoleCreate)
}

// name returns a node name unique in the file: base, else the operationId,
// else base with a number.
func (p *proposer) name(base string, op *spec.Operation) string {
	clean := func(s string) string {
		s = strings.Map(func(r rune) rune {
			if r == '_' || r == '-' || unicode.IsLetter(r) && r < unicode.MaxASCII || unicode.IsDigit(r) && r < unicode.MaxASCII {
				return r
			}
			return '-'
		}, s)
		s = strings.Trim(s, "-")
		if s == "" {
			s = "node"
		}
		return s
	}
	base = clean(base)
	if p.names[base] == 0 {
		p.names[base]++
		return base
	}
	if op != nil && op.HasOperationID {
		if id := clean(lowerFirst(op.ID)); p.names[id] == 0 {
			p.names[id]++
			return id
		}
	}
	p.names[base]++
	return fmt.Sprintf("%s%d", base, p.names[base])
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
	for _, n := range needs(op) {
		if n.in == openapi3.ParameterInQuery {
			return true
		}
	}
	return false
}

// need is a parameter a request needs a value for: a path parameter, or a
// required query parameter without example, default or enum (a generated
// value would find nothing on the stage).
type need struct{ name, in string }

func needs(op *spec.Operation) []need {
	var out []need
	for _, name := range pathParams(op) {
		out = append(out, need{name, openapi3.ParameterInPath})
	}
	for _, prm := range op.Params {
		if prm.In == openapi3.ParameterInQuery && prm.Required {
			if _, ok := documentedValue(prm); !ok {
				out = append(out, need{prm.Name, openapi3.ParameterInQuery})
			}
		}
	}
	return out
}

// set puts a value of a need into the path or query of a node.
func (n *Node) set(nd need, v any) {
	if nd.in == openapi3.ParameterInQuery {
		if n.Query == nil {
			n.Query = map[string]any{}
		}
		n.Query[nd.name] = v
		return
	}
	if n.Path == nil {
		n.Path = map[string]any{}
	}
	n.Path[nd.name] = v
}

// chain is the resources of the nodes from the root down to a node, with
// their names: the scope of references.
type chain struct {
	res  *model.Resource
	node *Node
	up   *chain
}

func (c *chain) depth() int {
	n := 0
	for x := c; x != nil; x = x.up {
		n++
	}
	return n
}

func (c *chain) has(r *model.Resource) bool {
	for x := c; x != nil; x = x.up {
		if x.res == r {
			return true
		}
	}
	return false
}

// listNode is the node of a resource read through a list.
func (p *proposer) listNode(r *model.Resource, list *spec.Operation, parent *Node, up *chain) *Node {
	p.used[list] = true
	n := &Node{Name: p.name(listName(list, r), list), Call: callOf(list), Comment: list.ID, Pick: Pick{N: DefaultPick}, Parent: parent}
	p.res[n] = r
	p.fill(n, list, up)
	if pg := pageOf(list); pg != nil {
		n.Page = pg
	}
	n.Key = keyPointers(r)
	here := &chain{res: r, node: n, up: up}
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
		c := &Node{Name: p.name(lowerFirst(r.Name), mo.Op), Call: callOf(mo.Op), Comment: mo.Op.ID, Parent: n}
		p.fill(c, mo.Op, here)
		p.res[c] = r
		n.Each = append(n.Each, c)
	}
	// other reads whose path parameters this node and those above it give
	for _, op := range p.s.Ops {
		if op.Method != http.MethodGet || p.used[op] || len(pathParams(op)) == 0 || requiredQuery(op) || !p.reachable(op, here) {
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
		c := &Node{Name: p.name(viewName(op), op), Call: callOf(op), Comment: op.ID, Parent: n}
		p.fill(c, op, here)
		if isList(op) {
			c.Pick = Pick{N: DefaultPick}
		}
		p.res[c] = p.resourceOf(op)
		n.Each = append(n.Each, c)
	}
	for _, child := range p.m.Resources {
		if child.Parent != r || here.has(child) {
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
		if t == nil || here.has(t) {
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
		c := &Node{Name: p.name(name, nil), Call: callOf(op), Comment: op.ID, Parent: n,
			Path: map[string]any{params[0]: "." + field}}
		c.Key = keyPointers(t)
		p.res[c] = t
		next := &chain{res: t, node: c, up: here}
		p.children(c, t, next)
		n.Each = append(n.Each, c)
	}
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

// reachable reports whether the chain gives every value op needs.
func (p *proposer) reachable(op *spec.Operation, here *chain) bool {
	_, missing := p.paramValues(op, here)
	return len(missing) == 0
}

// fill gives a node the values of op from the chain; a value it lacks
// becomes "$name" of params.
func (p *proposer) fill(n *Node, op *spec.Operation, here *chain) {
	vals, missing := p.paramValues(op, here)
	for _, nd := range needs(op) {
		if v, ok := vals[nd.name]; ok {
			n.set(nd, v)
		}
	}
	for _, nd := range missing {
		n.set(nd, p.param(nd.name, op))
	}
}

// takesKey reports whether one of op's path parameters is a key of r.
func (p *proposer) takesKey(op *spec.Operation, r *model.Resource) bool {
	for _, prm := range pathParams(op) {
		field, score := p.provides(op, prm, r)
		if score > 0 && slices.ContainsFunc(r.Keys, func(k string) bool { return strings.EqualFold(k, field) }) {
			return true
		}
	}
	seg := model.SegmentBefore(op.Path, pathParams(op)[len(pathParams(op))-1])
	return strings.EqualFold(singular(seg), r.Name) || strings.EqualFold(seg, r.Name)
}

// provides says which field of a record of resource r gives the path
// parameter of op, and how sure that is: the model maps the parameter to r
// (100), the parameter is the resource's name and a key ("bookId" = Book +
// id, 90), it ends in it ("originBookId", 60), or a field has its name
// ("isbn", 50; a generic name like "id" only with the model). 0: none.
func (p *proposer) provides(op *spec.Operation, param string, r *model.Resource) (string, int) {
	if r == nil {
		return "", 0
	}
	if mo := p.m.Op(op); mo != nil {
		if mp := mo.Param(param); mp != nil && mp.Resource == r && mp.Field != "" {
			return mp.Field, 100
		}
	}
	lp := strings.ToLower(param)
	keys := r.Keys
	if len(keys) == 0 {
		if f := r.Field("id"); f != "" {
			keys = []string{f}
		}
	}
	best, score := "", 0
	for _, name := range []string{r.Name, singular(r.Name)} {
		for _, k := range keys {
			whole := strings.ToLower(name + k)
			switch {
			case lp == whole && score < 90:
				best, score = k, 90
			case strings.HasSuffix(lp, whole) && score < 60:
				best, score = k, 60
			}
		}
	}
	if score == 0 && !generic(param) {
		if f := r.Field(param); f != "" {
			best, score = f, 50
		}
	}
	return best, score
}

func generic(name string) bool {
	switch strings.ToLower(name) {
	case "id", "uuid", "key", "code", "name", "number":
		return true
	}
	return false
}

// paramValues gives each value op needs a reference: to the record the
// node runs under (".field") or to the node above that gives it
// ("<node>.field"). missing are the values the chain does not give.
func (p *proposer) paramValues(op *spec.Operation, here *chain) (map[string]any, []need) {
	out := map[string]any{}
	var missing []need
	for _, nd := range needs(op) {
		name := nd.name
		var best *chain
		field, score := "", 0
		for c := here; c != nil; c = c.up {
			if f, s := p.provides(op, name, c.res); s > score {
				best, field, score = c, f, s
			}
		}
		switch {
		case best == here && best != nil:
			out[name] = "." + field
		case best != nil:
			out[name] = best.node.Name + "." + field
		case here != nil && here.res != nil && here.res.Field(name) != "":
			// a field of the record above with the parameter's name
			out[name] = "." + here.res.Field(name)
		default:
			missing = append(missing, nd)
		}
	}
	return out, missing
}

// order is the nodes of the tree in fetch order.
func (p *proposer) order() []*Node {
	var out []*Node
	var walk func([]*Node)
	walk = func(list []*Node) {
		for _, n := range list {
			out = append(out, n)
			walk(n.Each)
		}
	}
	walk(p.t.Roots)
	return out
}

// provider is the node that gives a path parameter, anywhere in the tree.
type provider struct {
	node  *Node
	field string
	score int
	at    int // position in fetch order
}

// providers finds per value op needs the node that gives it best; ties go
// to the node fetched first. missing lists the values no node gives.
func (p *proposer) providers(op *spec.Operation) (map[string]provider, []need) {
	nodes := p.order()
	out := map[string]provider{}
	var missing []need
	for _, nd := range needs(op) {
		prm := nd.name
		var best provider
		for i, n := range nodes {
			if n.Off {
				continue
			}
			if f, s := p.provides(op, prm, p.res[n]); s > best.score {
				best = provider{node: n, field: f, score: s, at: i}
			}
		}
		if best.node == nil {
			missing = append(missing, nd)
			continue
		}
		out[prm] = best
	}
	return out, missing
}

// placeRest hangs every read that is not in the tree yet below the node
// that gives its values, across the whole tree: below the provider fetched
// last, the others read by name ("books.id"). It repeats while nodes are
// added, since a new node gives values too. A read with values no node
// gives takes them from params ("$orderNo") and hangs below the nodes that
// give the others, else at the top.
func (p *proposer) placeRest() {
	for {
		for p.placeOnce(false) {
		}
		if !p.placeOnce(true) {
			return
		}
	}
}

// placeOnce places the reads whose values nodes give, or (withParams) the
// first read left, with the missing values from params.
func (p *proposer) placeOnce(withParams bool) bool {
	placed := false
	for _, op := range p.s.Ops {
		if op.Method != http.MethodGet || p.used[op] || len(needs(op)) == 0 {
			continue
		}
		provs, missing := p.providers(op)
		if len(missing) > 0 && !withParams {
			continue
		}
		var parent provider
		for _, pr := range provs {
			if parent.node == nil || pr.at > parent.at {
				parent = pr
			}
		}
		if parent.node != nil && depth(parent.node) >= maxDepth+2 {
			if !withParams {
				continue
			}
			// too deep: read the values by name from the top
			parent = provider{}
		}
		p.used[op] = true
		c := &Node{Name: p.name(viewName(op), op), Call: callOf(op), Comment: op.ID, Parent: parent.node}
		for _, nd := range needs(op) {
			pr, ok := provs[nd.name]
			switch {
			case !ok:
				c.set(nd, p.param(nd.name, op))
			case pr.node == parent.node:
				c.set(nd, "."+pr.field)
			default:
				c.set(nd, pr.node.Name+"."+pr.field)
			}
		}
		if isList(op) {
			c.Pick = Pick{N: DefaultPick}
		}
		res := p.resourceOf(op)
		p.res[c] = res
		if res != nil && len(res.Keys) > 0 {
			c.Key = keyPointers(res)
		}
		if parent.node == nil {
			p.t.Roots = append(p.t.Roots, c)
		} else {
			parent.node.Each = append(parent.node.Each, c)
		}
		placed = true
		if withParams {
			return true
		}
	}
	return placed
}

func depth(n *Node) int {
	d := 0
	for x := n; x != nil; x = x.Parent {
		d++
	}
	return d
}

// addWrites gives each resource one write block: at its list node, else at
// the first node that reads its records.
func (p *proposer) addWrites() {
	p.home = map[*model.Resource]*Node{}
	nodes := p.order()
	// list nodes first: they hold the records of a resource
	for _, pass := range []bool{true, false} {
		for _, n := range nodes {
			r := p.res[n]
			if r == nil || p.home[r] != nil || n.Off || len(n.Key) == 0 {
				continue
			}
			op := lookupOp(p.s, n.Call)
			if op == nil || isList(op) != pass {
				continue
			}
			if mo := p.m.Op(op); mo == nil || mo.Resource != r {
				continue
			}
			p.home[r] = n
			n.Write = p.writes(r, n)
		}
	}
}

// writes is the write block of a resource at node n: its create, update,
// patch and delete; create: none when the API has no create.
func (p *proposer) writes(r *model.Resource, n *Node) map[string]*Write {
	out := map[string]*Write{}
	add := func(kind string, mo *model.Op) {
		if _, ok := out[kind]; ok || mo == nil {
			return
		}
		out[kind] = p.write(kind, mo.Op, r, n)
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

// write is the write of a record of node n (resource r, nil if unknown):
// its values, and the body from the read that covers the request best.
func (p *proposer) write(kind string, op *spec.Operation, r *model.Resource, n *Node) *Write {
	p.written[op] = true
	w := &Write{Kind: kind, Call: callOf(op), Comment: op.ID}
	vals := p.writeParams(op, r, n, kind == WriteCreate)
	for _, nd := range needs(op) {
		v, ok := vals[nd.name]
		if !ok {
			v = p.param(nd.name, op)
		}
		if nd.in == openapi3.ParameterInQuery {
			if w.Query == nil {
				w.Query = map[string]any{}
			}
			w.Query[nd.name] = v
			continue
		}
		if w.Path == nil {
			w.Path = map[string]any{}
		}
		w.Path[nd.name] = v
	}
	if kind != WriteDelete && requestRef(op) != nil {
		w.From = p.bodySource(n, op)
	}
	return w
}

// writeParams gives the values of a write of a record of r at node n: a
// field of the record itself ("authorId" of a book, its key unless it is a
// create), the node above that gives it, else any node of the tree. A value
// none gives is left out.
func (p *proposer) writeParams(op *spec.Operation, r *model.Resource, n *Node, create bool) map[string]any {
	out := map[string]any{}
	isKey := func(f string) bool {
		return r != nil && slices.ContainsFunc(r.Keys, func(k string) bool { return strings.EqualFold(k, f) })
	}
	field := func(name string) string {
		if r != nil {
			return r.Field(name)
		}
		return ""
	}
	for _, nd := range needs(op) {
		prm := nd.name
		// the record itself: a field that names the value
		self, selfScore := p.provides(op, prm, r)
		if selfScore > 0 && create && isKey(self) {
			self, selfScore = "", 0
		}
		if selfScore == 0 {
			if f := field(prm); f != "" && (!create || !isKey(f)) {
				self, selfScore = f, 40
			}
		}
		// a node above that gives it more surely
		var best *Node
		bestField, score := "", selfScore
		for x := n.Parent; x != nil; x = x.Parent {
			if f, s := p.provides(op, prm, p.res[x]); s > score {
				best, bestField, score = x, f, s
			}
		}
		if best == nil && selfScore > 0 {
			out[prm] = "." + self
			continue
		}
		if best == nil {
			provs, _ := p.providers(op)
			if pr, ok := provs[prm]; ok && pr.node != n {
				best, bestField = pr.node, pr.field
			}
		}
		if best == nil {
			continue
		}
		out[prm] = best.Name + "." + bestField
	}
	return out
}

// bodySource proposes the read whose answer covers the request of op best:
// a read below n about its record ("from details") when it covers more than
// the record of n, else n itself.
func (p *proposer) bodySource(n *Node, op *spec.Operation) string {
	req := requestRef(op)
	if req == nil || req.Value == nil {
		return ""
	}
	want, _ := properties(req.Value)
	cover := func(o *spec.Operation, item bool) int {
		ref := responseRef(o, lowestSuccess(o))
		if ref == nil || ref.Value == nil {
			return 0
		}
		s := ref.Value
		if item {
			if ptr, ok := listPointer(o); ok {
				if ptr == "" {
					s = s.Items.Value
				} else if l, found := propertyFold(s, strings.TrimPrefix(ptr, "/")); found && l.Items != nil {
					s = l.Items.Value
				}
			}
		}
		have, _ := properties(s)
		n := 0
		for k, w := range want {
			if w == nil || w.Value == nil || w.Value.ReadOnly {
				continue
			}
			for h := range have {
				if strings.EqualFold(h, k) {
					n++
					break
				}
			}
		}
		return n
	}
	own := 0
	if o := lookupOp(p.s, n.Call); o != nil {
		own = cover(o, true)
	}
	best, score := n.Name, own
	for _, c := range n.Each {
		o := lookupOp(p.s, c.Call)
		if o == nil || isList(o) || !p.aboutRecord(c, n) {
			continue
		}
		if s := cover(o, false); s > score {
			best, score = c.Name, s
		}
	}
	return best
}

// aboutRecord reports whether child c reads something about the record of
// n: one of its path values is a key of n's record (".id").
func (p *proposer) aboutRecord(c, n *Node) bool {
	for _, v := range c.Path {
		s, ok := v.(string)
		if !ok || !strings.HasPrefix(s, ".") {
			continue
		}
		for _, k := range n.Key {
			if strings.EqualFold(PointerField(k), s[1:]) {
				return true
			}
		}
	}
	return false
}

func propertyFold(s *openapi3.Schema, name string) (*openapi3.Schema, bool) {
	props, _ := dict.Properties(s)
	for k, v := range props {
		if strings.EqualFold(k, name) && v != nil && v.Value != nil {
			return v.Value, true
		}
	}
	return nil, false
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

// attachWrites gives every write the tree has not yet to the node whose
// records it writes: a node of its resource, a node read at its path (PUT
// /docks/{dockId} to GET /docks/{dockId}), a node whose key its path takes
// (POST /docks/{dockId}/close), a node whose records cover its body. A
// POST that answers with a list and creates nothing is a search: a node
// with read: true. The rest goes to ignore, with the reason.
func (p *proposer) attachWrites() {
	for _, op := range p.s.Ops {
		if op.Method == http.MethodGet || p.written[op] || p.used[op] {
			continue
		}
		kind := writeKind(op.Method)
		if kind == "" {
			continue
		}
		mo := p.m.Op(op)
		if isSearch(mo, op) {
			p.search(op)
			continue
		}
		var best *Node
		score := 0
		for _, n := range p.order() {
			if n.Off || n.Write[kind] != nil {
				continue
			}
			if s := p.writesTo(op, mo, n); s > score {
				best, score = n, s
			}
		}
		if best == nil {
			p.t.Ignore = append(p.t.Ignore, callOf(op))
			if p.t.IgnoreNotes == nil {
				p.t.IgnoreNotes = map[string]string{}
			}
			p.t.IgnoreNotes[callOf(op)] = op.ID + " · no node reads the records it writes; give a node a write block with it"
			continue
		}
		if best.Write == nil {
			best.Write = map[string]*Write{}
		}
		if r := p.res[best]; r != nil && len(best.Key) == 0 {
			best.Key = keyPointers(r)
		}
		best.Write[kind] = p.write(kind, op, p.res[best], best)
	}
}

func writeKind(method string) string {
	switch method {
	case http.MethodPost:
		return WriteCreate
	case http.MethodPut:
		return WriteUpdate
	case http.MethodPatch:
		return WritePatch
	case http.MethodDelete:
		return WriteDelete
	}
	return ""
}

// writesTo scores how surely op writes the records of node n: n is of its
// resource (100), reads the path op writes (90), gives a key its path takes
// (60), lies on its path (50), or its records cover the body (40). 0: not.
func (p *proposer) writesTo(op *spec.Operation, mo *model.Op, n *Node) int {
	read := lookupOp(p.s, n.Call)
	if read == nil {
		return 0
	}
	r := p.res[n]
	score := 0
	if mo != nil && r == mo.Resource {
		score = 100
		if p.home[r] == n {
			score += 5
		}
	}
	if read.Path == op.Path {
		score = max(score, 90)
	}
	for _, prm := range pathParams(op) {
		if f, s := p.provides(op, prm, r); s >= 90 && r != nil && slices.ContainsFunc(r.Keys, func(k string) bool { return strings.EqualFold(k, f) }) {
			score = max(score, 60)
		}
	}
	if strings.HasPrefix(op.Path, read.Path+"/") {
		score = max(score, 50)
	}
	if req := requestRef(op); score == 0 && req != nil && req.Value != nil {
		want, _ := properties(req.Value)
		have, _ := properties(p.recordSchema(read))
		writable, covered := 0, 0
		for k, w := range want {
			if w == nil || w.Value == nil || w.Value.ReadOnly {
				continue
			}
			writable++
			if _, ok := propertyFoldIn(have, k); ok {
				covered++
			}
		}
		if covered >= 2 && covered*2 >= writable {
			score = 40
		}
	}
	return score
}

// recordSchema is the schema of one record of a read: the item of a list.
func (p *proposer) recordSchema(op *spec.Operation) *openapi3.Schema {
	ref := responseRef(op, lowestSuccess(op))
	if ref == nil || ref.Value == nil {
		return nil
	}
	s := ref.Value
	if ptr, ok := listPointer(op); ok {
		if ptr == "" {
			if s.Items != nil {
				return s.Items.Value
			}
			return nil
		}
		if l, found := propertyFold(s, strings.TrimPrefix(ptr, "/")); found && l.Items != nil {
			return l.Items.Value
		}
	}
	return s
}

func propertyFoldIn(props openapi3.Schemas, name string) (*openapi3.SchemaRef, bool) {
	for k, v := range props {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return nil, false
}

// search adds a POST that only reads (a search) as a node with read: true,
// below the nodes that give its path values, else at the top.
func (p *proposer) search(op *spec.Operation) {
	p.used[op] = true
	provs, _ := p.providers(op)
	var parent provider
	for _, pr := range provs {
		if parent.node == nil || pr.at > parent.at {
			parent = pr
		}
	}
	c := &Node{Name: p.name(viewName(op), op), Call: callOf(op), Comment: op.ID, Parent: parent.node, Read: true,
		Body: map[string]any{}, HasBody: true, Pick: Pick{N: DefaultPick}}
	for _, nd := range needs(op) {
		pr, ok := provs[nd.name]
		switch {
		case !ok:
			c.set(nd, p.param(nd.name, op))
		case pr.node == parent.node:
			c.set(nd, "."+pr.field)
		default:
			c.set(nd, pr.node.Name+"."+pr.field)
		}
	}
	res := p.resourceOf(op)
	p.res[c] = res
	if res != nil {
		c.Key = keyPointers(res)
	}
	if parent.node == nil {
		p.t.Roots = append(p.t.Roots, c)
		return
	}
	parent.node.Each = append(parent.node.Each, c)
}
