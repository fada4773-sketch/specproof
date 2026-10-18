package tree

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
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
// goes to the node whose records it writes, an action (POST
// /gardens/{gardenId}/open) under its own name. The operationIds add
// conditions: GetGardensByPersonName hangs below the persons, which then
// require gardens; reads that answer 404 and lists of records below make
// require, fields that name a record where present, actions and state
// words (GetOpenGardens) where on the state.
func Propose(s *spec.Spec) *Tree {
	return ProposeScope(s, Scope{})
}

// ProposeScope proposes the tree for the operations of the scope; with
// Deps also the reads of other tags that give values the scope needs.
func ProposeScope(s *spec.Spec, sc Scope) *Tree {
	in := inScope(s, sc)
	t := propose(s, in)
	if sc.Deps {
		for range 3 {
			more := deps(s, in, t, sc)
			if len(more) == 0 {
				break
			}
			in = append(in, more...)
			t = propose(s, in)
		}
	}
	t.Scope = sc
	return t
}

func propose(full *spec.Spec, ops []*spec.Operation) *Tree {
	fs := *full
	fs.Ops = ops
	s := &fs
	p := &proposer{s: s, m: model.Detect(s, nil), used: map[*spec.Operation]bool{}, names: map[string]int{},
		res: map[*Node]*model.Resource{}, written: map[*spec.Operation]bool{}, placedBy: map[*Node]hint{}, referenced: map[*Node]bool{}}
	p.all = p.m
	if len(ops) < len(full.Ops) {
		// the operationIds name resources of other tags too
		p.all = model.Detect(full, nil)
		// the keys of the whole spec: without the moons in scope, moonId
		// of /moons/{moonId}/gardens is no key of a garden
		for _, r := range p.m.Resources {
			for _, a := range p.all.Resources {
				if a.Name == r.Name && len(a.Keys) > 0 {
					r.Keys = a.Keys
				}
			}
		}
	}
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
		if op.Method == http.MethodGet && !p.used[op] && len(p.needs(op)) == 0 {
			// a read of its own (counters, settings): a root
			p.used[op] = true
			n := &Node{Name: p.name(p.viewName(op), op), Call: callOf(op), Comment: op.ID}
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
	p.conditions()
	addGeneratedKeys(p.s, t)
	p.t.Roots = p.sortSiblings(p.t.Roots, p.t.names())
	t.Order = proposeOrder(p.s, p.m, t, p.orderValues())
	t.HasOrder = len(t.Order) > 0
	for name, ops := range p.paramOps {
		t.ParamNotes[name] = usedBy(ops)
		if vals := p.enums[name]; len(vals) > 0 {
			t.ParamNotes[name] += " · one of " + strings.Join(vals, ", ")
		}
	}
	return t
}

// hinted reports whether the operationId of op names what a parameter
// holds.
func (p *proposer) hinted(op *spec.Operation, name string) bool {
	_, ok := p.hints(op)[name]
	return ok
}

// paramEnum notes the values a param of an enum can take.
func (p *proposer) paramEnum(name string, vals []string) {
	if p.enums == nil {
		p.enums = map[string][]string{}
	}
	p.enums[name] = vals
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
		// the example, default or first enum value of the spec; a value of
		// the stage is still yours to set
		p.t.Params[name] = nil
		for _, prm := range op.Params {
			if prm.Name != name {
				continue
			}
			if v, ok := documentedValue(prm); ok {
				p.t.Params[name] = v
				if prm.Schema != nil && prm.Schema.Value != nil && len(prm.Schema.Value.Enum) > 1 {
					var vals []string
					for _, e := range prm.Schema.Value.Enum {
						vals = append(vals, text(e))
					}
					p.paramEnum(name, vals)
				}
			}
		}
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
	// all is the model of the whole spec, for the resources operationIds
	// name outside the scope.
	all *model.Model
	// hintCache holds what the operationIds say about parameters.
	hintCache map[*spec.Operation]map[string]hint
	// enums are the values of params from an enum of the spec.
	enums map[string][]string
	// provided caches provides, refKeys the keys references agree on.
	provided map[provideKey]provided
	refKeys  map[*model.Resource]string
	ops      map[string]*spec.Operation
	// placedBy are the nodes placed below a node for a filter their
	// operationId names (GetGardensByPersonName below the persons).
	placedBy map[*Node]hint
	// referenced are the nodes that read the records a record refers to
	// (by a where on their key): no conditions of their own
	referenced map[*Node]bool
	// refBy is the reference a node reads its records by (pilotId of the
	// ship above it): the source of that field in order
	refBy map[*Node]reference
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
		// the operationId without its verb (getMechanics: mechanics,
		// listBedsByGardenCode: bedsByGardenCode), else as it is
		ids := []string{clean(lowerFirst(op.ID))}
		if v := parseOpName(op.ID).verb; v != "" && len(op.ID) > len(v) && strings.EqualFold(op.ID[:len(v)], v) {
			ids = append([]string{clean(lowerFirst(op.ID[len(v):]))}, ids...)
		}
		for _, id := range ids {
			if p.names[id] == 0 {
				p.names[id]++
				return id
			}
		}
	}
	p.names[base]++
	return fmt.Sprintf("%s%d", base, p.names[base])
}

// freeList is the list of a resource that needs no path parameter.
func (p *proposer) freeList(r *model.Resource) *spec.Operation {
	// a list whose operationId names a filter (GetPersonsByGardenName)
	// hangs below the node that gives it, not at the top
	for _, mo := range r.OpsWith(model.RoleList) {
		if len(p.needs(mo.Op)) == 0 {
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
	n := &Node{Name: p.name(p.listName(list, r), list), Call: callOf(list), Comment: list.ID, Pick: Pick{N: DefaultPick}, Parent: parent}
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
	reads := r.OpsWith(model.RoleRead)
	slices.SortStableFunc(reads, func(a, b *model.Op) int { return keyRank(r, a) - keyRank(r, b) })
	for _, mo := range reads {
		if p.used[mo.Op] || !p.reachable(mo.Op, here) {
			continue
		}
		p.used[mo.Op] = true
		// the first read is the record itself, a read by another key is
		// named after it (gardenByName), others are views (details)
		base := lowerFirst(r.Name)
		if by := p.byKeyName(mo.Op); by != "" {
			base = by
		} else if p.names[base] > 0 {
			base = viewName(mo.Op)
		}
		c := &Node{Name: p.name(base, mo.Op), Call: callOf(mo.Op), Comment: mo.Op.ID, Parent: n}
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
		c := &Node{Name: p.name(p.viewName(op), op), Call: callOf(op), Comment: op.ID, Parent: n}
		p.fill(c, op, here)
		if isList(op) {
			c.Pick = Pick{N: DefaultPick}
		}
		p.res[c] = p.resourceOf(op)
		if res := p.res[c]; res != nil && len(res.Keys) > 0 && isList(op) {
			c.Key = keyPointers(res)
		}
		n.Each = append(n.Each, c)
		p.createdReferences(c, op)
	}
	for _, child := range p.m.Resources {
		if child.Parent != r || here.has(child) {
			continue
		}
		// the list below the record (by its key in the path) before a list
		// a filter of the operationId gives values to
		lists := child.OpsWith(model.RoleList)
		slices.SortStableFunc(lists, func(a, b *model.Op) int { return len(p.hints(a.Op)) - len(p.hints(b.Op)) })
		for _, mo := range lists {
			if !p.used[mo.Op] && p.reachable(mo.Op, here) {
				n.Each = append(n.Each, p.listNode(child, mo.Op, n, here))
				break
			}
		}
	}
	p.references(n, r, here, false)
}

// reference is a field of a record that names records of another resource
// by a key: one (personId), or a list of them (docks: [D-1, D-2]).
type reference struct {
	field string
	to    *model.Resource
	key   string
	many  bool
}

// referencesOf are the references of r: its relations, also those inside
// an object field (crew.pilotId), and lists of keys named after a resource
// (docks, dockCodes, pilotIds).
func (p *proposer) referencesOf(r *model.Resource) []reference {
	var out []reference
	for _, f := range slices.Concat(r.Relations, r.Nested) {
		if t, key := r.Ref(f); t != nil {
			out = append(out, reference{field: f, to: t, key: key})
		}
	}
	for _, f := range r.FieldNames() {
		s := r.Schema(f)
		if s == nil || s.Value == nil || value.Type(s.Value) != "array" || s.Value.Items == nil || s.Value.Items.Value == nil {
			continue
		}
		typ := value.Type(s.Value.Items.Value)
		if typ != "string" && typ != "integer" {
			continue
		}
		if t, key := p.listedKeys(f, typ); t != nil && t != r {
			out = append(out, reference{field: f, to: t, key: key, many: true})
		}
	}
	return out
}

// listedKeys reads the name of a list of keys: docks or dockCodes are
// keys of Dock, the key of the item type (a text: its code, a number:
// its id).
func (p *proposer) listedKeys(field, typ string) (*model.Resource, string) {
	name := singular(field)
	var key string
	for _, suffix := range []string{"Ids", "Codes", "Numbers", "Keys", "Names"} {
		if base, ok := strings.CutSuffix(field, suffix); ok && base != "" {
			name, key = base, singular(suffix)
			break
		}
	}
	t := p.m.Resource(name)
	if t == nil {
		return nil, ""
	}
	if key != "" {
		for _, k := range append(slices.Clone(t.Keys), t.FieldNames()...) {
			if strings.EqualFold(k, key) || strings.EqualFold(k, t.Name+key) {
				return t, k
			}
		}
	}
	// a plain plural (docks) holds keys named after the resource
	// (dockCode); events of an installation are names, no ids of events
	for _, k := range t.Keys {
		if s := t.Schema(k); s != nil && s.Value != nil && value.Type(s.Value) == typ &&
			!generic(k) && strings.Contains(strings.ToLower(k), strings.ToLower(t.Name)) {
			return t, k
		}
	}
	return nil, ""
}

// references adds below a node of r the records its fields refer to, read
// by the key the field holds: the read by that key (personId by id, not by
// the first read of the spec), else a list of the resource with a where on
// the key (the pilot of a dock from the pilots). A scenario creates these
// records before the record that refers to them. lean: below such a record
// only its own references, not every read about it.
func (p *proposer) references(n *Node, r *model.Resource, here *chain, lean bool) {
	if here.depth() > maxDepth {
		return
	}
	if p.referenced == nil {
		p.referenced = map[*Node]bool{}
	}
	for _, ref := range p.referencesOf(r) {
		t := p.creatable(ref.to, ref.key)
		if here.has(t) || p.readsBelow(n, ref.field) {
			continue
		}
		var op *spec.Operation
		for _, mo := range t.OpsWith(model.RoleRead) {
			if len(pathParams(mo.Op)) != 1 {
				continue
			}
			if exact := ref.key != "" && strings.EqualFold(byField(mo), ref.key); op == nil || exact {
				op = mo.Op
				if exact {
					break
				}
			}
		}
		// the record the field names; it creates like the node of its
		// resource (no write block of its own)
		name := lowerFirst(t.Name)
		if p.names[name] > 0 {
			name = lowerFirst(singular(r.Name)) + t.Name
		}
		var c *Node
		switch {
		case op != nil:
			p.used[op] = true
			c = &Node{Name: p.name(name, nil), Call: callOf(op), Comment: op.ID, Parent: n,
				Path: map[string]any{pathParams(op)[0]: "." + ref.field}}
		case ref.key != "":
			list := p.listOf(t, here)
			if list == nil {
				continue
			}
			key := t.Field(ref.key)
			if key == "" {
				key = ref.key
			}
			c = &Node{Name: p.name(name, nil), Call: callOf(list), Comment: list.ID, Parent: n,
				Where: []Clause{{Field: key, Test: n.Name + "." + ref.field, Note: "the " + lowerFirst(t.Name) + " " + n.Name + " refers to"}}}
			p.fill(c, list, here)
			if pg := pageOf(list); pg != nil {
				c.Page = pg
			}
			p.referenced[c] = true
		default:
			continue
		}
		if ref.many {
			c.Pick = Pick{All: true}
		}
		c.Key = keyPointers(t)
		p.res[c] = t
		if p.refBy == nil {
			p.refBy = map[*Node]reference{}
		}
		key := ref.key
		if len(c.Where) > 0 && op == nil {
			key = c.Where[0].Field
		} else if f := t.Field(key); f != "" {
			key = f
		}
		p.refBy[c] = reference{field: ref.field, to: t, key: key, many: ref.many}
		next := &chain{res: t, node: c, up: here}
		if lean {
			p.referenced[c] = true
			p.references(c, t, next, true)
		} else {
			p.children(c, t, next)
		}
		n.Each = append(n.Each, c)
	}
}

// creatable is the resource a reference names, or, when the API cannot
// create its records, a resource it can create with the same key named
// after it (dockCode of a dock list and of the dock the create
// answers: one kind of record). A generic key (code, id) names no kind.
func (p *proposer) creatable(t *model.Resource, key string) *model.Resource {
	if len(t.OpsWith(model.RoleCreate)) > 0 || key == "" || generic(key) ||
		!strings.Contains(strings.ToLower(key), strings.ToLower(singular(t.Name))) {
		return t
	}
	for _, x := range p.m.Resources {
		if x != t && len(x.OpsWith(model.RoleCreate)) > 0 && slices.ContainsFunc(x.Keys, func(k string) bool { return strings.EqualFold(k, key) }) {
			return x
		}
	}
	return t
}

// orderValues are the sources of the bodies of the creates: a field of the
// body a node below reads its record by (pilotId: shipPilot.id).
func (p *proposer) orderValues() map[string]map[string]any {
	out := map[string]map[string]any{}
	p.t.Walk(func(n *Node) {
		w := n.Write[WriteCreate]
		if n.IsRequire || w == nil || w.None {
			return
		}
		op := p.lookup(w.Call)
		if op == nil {
			return
		}
		props, _ := properties(schemaOf(requestRef(op)))
		for _, c := range n.Each {
			ref, ok := p.refBy[c]
			if !ok || ref.key == "" {
				continue
			}
			if _, name, ok := bodyField(props, ref.field); ok {
				if out[n.Name] == nil {
					out[n.Name] = map[string]any{}
				}
				out[n.Name][name] = c.Name + "." + ref.key
			}
		}
	})
	return out
}

// createdReferences adds the references below a list node that was not
// built from its resource (placed by the values it needs): its records the
// scenario may create need the records they refer to.
func (p *proposer) createdReferences(c *Node, op *spec.Operation) {
	res := p.res[c]
	if res == nil || !isList(op) || len(res.OpsWith(model.RoleCreate)) == 0 {
		return
	}
	p.references(c, res, p.chainOf(c), true)
}

// bodyFilters are the fields of a search's body that a node of the tree
// gives, like a parameter: {planet: planets.code} for a field named
// after a resource, {pilotId: pilots.id}. Fields of the records the search
// answers with (filters on themselves) and paging fields are left out.
func (p *proposer) bodyFilters(op *spec.Operation, res *model.Resource) map[string]provider {
	props, _ := properties(schemaOf(requestRef(op)))
	out := map[string]provider{}
	nodes := p.order()
	for _, name := range sortedKeys(props) {
		s := props[name]
		if s == nil || s.Value == nil || slices.Contains(pagingNames, strings.ToLower(name)) || res != nil && res.Field(name) != "" {
			continue
		}
		var best provider
		for i, n := range nodes {
			r := p.res[n]
			if n.Off || r == nil || r == res || p.referenced[n] {
				continue
			}
			// the name names the record: pilotId, planet; a field of
			// another record with that name filters nothing of it
			f, score := nameAndKey(name, r)
			if score == 0 {
				f, score = namedKey(name, s.Value, r)
			}
			if score > best.score {
				best = provider{node: n, field: f, score: score, at: i}
			}
		}
		if best.node != nil {
			out[name] = best
		}
	}
	return out
}

// namedKey is the key of r a field named after r holds: a text its code, a
// number its id (planet: Planet.planetCode).
func namedKey(name string, s *openapi3.Schema, r *model.Resource) (string, int) {
	if !strings.EqualFold(name, r.Name) && !strings.EqualFold(singular(name), r.Name) {
		return "", 0
	}
	keys := slices.Clone(r.Keys)
	for _, g := range []string{"id", "code", "key"} {
		// a record without known keys: the usual names of a key
		if f := r.Field(g); f != "" && !slices.Contains(keys, f) {
			keys = append(keys, f)
		}
	}
	typ := value.Type(s)
	for _, k := range keys {
		if ks := r.Schema(k); ks != nil && ks.Value != nil && value.Type(ks.Value) == typ {
			return k, 75
		}
	}
	return "", 0
}

// sortSiblings orders nodes of one level (and below them, each level) by
// what they need: a node whose subtree reads "<node>.field" of another's
// subtree after it (the fetch needs it before), and a node whose records
// refer to the records another node lists after it (the pilots before the
// docks that name them, the docks before the bookings that list
// them). Without such a need the order stays; when the needs go round,
// only the reads by name count.
func (p *proposer) sortSiblings(list []*Node, scope func(string) bool) []*Node {
	for _, n := range list {
		n.Each = p.sortSiblings(n.Each, scope)
	}
	if len(list) < 2 {
		return list
	}
	type info struct {
		names, reads map[string]bool
		refers       []*model.Resource
	}
	infos := make([]info, len(list))
	for i, n := range list {
		in := info{names: map[string]bool{}, reads: map[string]bool{}}
		var walk func(x *Node)
		walk = func(x *Node) {
			in.names[x.Name] = true
			values := []any{anyMap(x.Path), anyMap(x.Query), anyMap(x.Header), x.Body}
			for _, c := range slices.Concat(x.Where, x.WhereAny) {
				values = append(values, c.Test)
			}
			for _, r := range x.Require {
				values = append(values, anyMap(r.Path), anyMap(r.Query))
			}
			for _, v := range values {
				walkStrings(v, func(s string) {
					for _, part := range refsOf(s, scope) {
						if name, _, ok := strings.Cut(part, "."); ok && name != "" {
							in.reads[name] = true
						}
					}
				})
			}
			if r := p.res[x]; r != nil {
				for _, ref := range p.referencesOf(r) {
					in.refers = append(in.refers, p.creatable(ref.to, ref.key))
				}
			}
			for _, c := range x.Each {
				walk(c)
			}
		}
		walk(n)
		infos[i] = in
	}
	needs := func(soft bool) [][]int {
		out := make([][]int, len(list))
		for b := range list {
			for a := range list {
				if a == b {
					continue
				}
				hard := false
				for name := range infos[b].reads {
					if infos[a].names[name] && !infos[b].names[name] {
						hard = true
						break
					}
				}
				refers := false
				if ra := p.res[list[a]]; soft && ra != nil {
					refers = slices.ContainsFunc(infos[b].refers, func(t *model.Resource) bool { return t == ra || sameRecord(t, ra) })
				}
				if hard || refers {
					out[b] = append(out[b], a)
				}
			}
		}
		return out
	}
	for _, soft := range []bool{true, false} {
		if order, ok := stableTopo(needs(soft)); ok {
			out := make([]*Node, len(list))
			for i, j := range order {
				out[i] = list[j]
			}
			return out
		}
	}
	return list
}

// stableTopo orders 0..n-1 so that each comes after what it needs, else
// in its own order; ok is false when the needs go round.
func stableTopo(needs [][]int) ([]int, bool) {
	done := make([]bool, len(needs))
	var out []int
	for len(out) < len(needs) {
		next := -1
		for i := range needs {
			if done[i] {
				continue
			}
			if !slices.ContainsFunc(needs[i], func(j int) bool { return !done[j] }) {
				next = i
				break
			}
		}
		if next < 0 {
			return nil, false
		}
		done[next] = true
		out = append(out, next)
	}
	return out, true
}

// listOf is a list of t whose values the chain gives, nil without.
func (p *proposer) listOf(t *model.Resource, here *chain) *spec.Operation {
	for _, mo := range t.OpsWith(model.RoleList) {
		if p.reachable(mo.Op, here) {
			return mo.Op
		}
	}
	return nil
}

// readsBelow reports whether a node below n reads a record by the field
// (".field").
func (p *proposer) readsBelow(n *Node, field string) bool {
	for _, c := range n.Each {
		for _, v := range c.Path {
			if v == "."+field {
				return true
			}
		}
		for _, w := range c.Where {
			if w.Test == n.Name+"."+field {
				return true
			}
		}
	}
	return false
}

// chainOf is the chain of a node placed below others: the resources of
// its ancestors.
func (p *proposer) chainOf(n *Node) *chain {
	var nodes []*Node
	for x := n; x != nil; x = x.Parent {
		nodes = append([]*Node{x}, nodes...)
	}
	var c *chain
	for _, x := range nodes {
		c = &chain{res: p.res[x], node: x, up: c}
	}
	return c
}

// refers reports whether a field of r refers to t.
func (p *proposer) refers(r, t *model.Resource) bool {
	for _, f := range slices.Concat(r.Relations, r.Nested) {
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
	for _, nd := range p.needs(op) {
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
	// the answer depends on the spec and the model alone: a large spec
	// asks it millions of times
	k := provideKey{op, param, r}
	if v, ok := p.provided[k]; ok {
		return v.field, v.score
	}
	f, s := p.providesFresh(op, param, r)
	if p.provided == nil {
		p.provided = map[provideKey]provided{}
	}
	p.provided[k] = provided{f, s}
	return f, s
}

// lookup is lookupOp for the spec of the proposal, by an index: a large
// spec looks up its calls millions of times.
func (p *proposer) lookup(call string) *spec.Operation {
	if op, ok := p.ops[call]; ok {
		return op
	}
	op := lookupOp(p.s, call)
	if p.ops == nil {
		p.ops = map[string]*spec.Operation{}
	}
	p.ops[call] = op
	return op
}

type provideKey struct {
	op    *spec.Operation
	param string
	r     *model.Resource
}

type provided struct {
	field string
	score int
}

func (p *proposer) providesFresh(op *spec.Operation, param string, r *model.Resource) (string, int) {
	if mo := p.m.Op(op); mo != nil {
		if mp := mo.Param(param); mp != nil && mp.Field != "" {
			if mp.Resource == r {
				return mp.Field, 100
			}
			// the same record listed below another parent (the docks of a
			// planet and of a moon): the model maps the parameter to one
			if sameRecord(mp.Resource, r) {
				if f := r.Field(mp.Field); f != "" {
					return f, 100
				}
			}
		}
	}
	if h, ok := p.hints(op)[param]; ok && h.res.Name == r.Name {
		// the operationId names it: GetGardensByPersonName → Person.name
		return h.field, 85
	}
	keys := r.Keys
	if len(keys) == 0 {
		if f := r.Field("id"); f != "" {
			keys = []string{f}
		}
	}
	best, score := nameAndKey(param, r)
	if score == 0 && !generic(param) {
		if f := r.Field(param); f != "" {
			best, score = f, 50
			// named exactly like a key of the record (repairNo of a repair state)
			if slices.ContainsFunc(keys, func(k string) bool { return strings.EqualFold(k, f) }) {
				score = 90
			}
		}
	}
	if score == 0 {
		// GET /docks/{dockNumber} read with Ship.DockId, which refers to
		// Dock.Id: dockNumber holds the Id
		if k := p.relationKey(op, param, r); k != "" {
			best, score = k, 80
		}
	}
	if score == 0 && generic(param) {
		// /docks/{id}/permits: the word before a generic name names the
		// record, and the record has a field of that name
		if seg := model.SegmentBefore(op.Path, param); seg != "" && namesRecord(seg, r) {
			if f := r.Field(param); f != "" {
				best, score = f, 70
			}
		}
	}
	return best, score
}

// nameAndKey is the key a name made of the resource's name and a key holds:
// bookId (90), originBookId (60); 0 for none.
func nameAndKey(param string, r *model.Resource) (string, int) {
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
	return best, score
}

// sameRecord reports whether two resources are one kind of record: the
// same main schema, listed below different parents.
func sameRecord(a, b *model.Resource) bool {
	return a != nil && b != nil && len(a.Schemas) > 0 && len(b.Schemas) > 0 && a.Schemas[0] == b.Schemas[0]
}

// namesRecord reports whether a path word names the resource or its main
// schema (docks, Dock).
func namesRecord(seg string, r *model.Resource) bool {
	names := []string{r.Name}
	if len(r.Schemas) > 0 {
		names = append(names, r.Schemas[0])
	}
	for _, n := range names {
		if strings.EqualFold(seg, n) || strings.EqualFold(singular(seg), n) {
			return true
		}
	}
	return false
}

// relationKey is the key of r a path parameter of r's own path holds when
// a field of another resource refers to r by that key: the parameter is
// the last of the path, the word before it names r, and the references to
// r agree on one key.
func (p *proposer) relationKey(op *spec.Operation, param string, r *model.Resource) string {
	params := pathParams(op)
	if len(params) == 0 || params[len(params)-1] != param {
		return ""
	}
	seg := model.SegmentBefore(op.Path, param)
	if !strings.EqualFold(seg, r.Name) && !strings.EqualFold(singular(seg), r.Name) {
		return ""
	}
	if p.refKeys == nil {
		// per resource the key the references to it agree on, "" when
		// they disagree
		p.refKeys = map[*model.Resource]string{}
		mixed := map[*model.Resource]bool{}
		for _, x := range p.m.Resources {
			for _, f := range x.Relations {
				to, k := x.Ref(f)
				if to == nil || k == "" || mixed[to] {
					continue
				}
				if old, ok := p.refKeys[to]; ok && !strings.EqualFold(old, k) {
					mixed[to] = true
					p.refKeys[to] = ""
					continue
				}
				p.refKeys[to] = k
			}
		}
	}
	return p.refKeys[r]
}

// namesOther reports whether a path word names a resource other than r
// (pilots in /pilots/{id}/license below the docks), by its name or its
// main schema (Dock for the docks of a moon).
func (p *proposer) namesOther(seg string, r *model.Resource) bool {
	if r != nil && namesRecord(seg, r) {
		return false
	}
	m := p.all
	if m == nil {
		m = p.m
	}
	if m == nil {
		return false
	}
	for _, x := range m.Resources {
		if x != r && namesRecord(seg, x) {
			return true
		}
	}
	return false
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
	for _, nd := range p.needs(op) {
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
		case nd.in == openapi3.ParameterInPath && here != nil && here.res != nil && here.res.Field(name) != "" && !p.hinted(op, name) &&
			!p.namesOther(model.SegmentBefore(op.Path, name), here.res):
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
	for _, nd := range p.needs(op) {
		prm := nd.name
		var best provider
		for i, n := range nodes {
			if n.Off {
				continue
			}
			// on a tie a node of the main chain wins over a node placed for
			// a filter (the gardens of a moon over the gardens of a person)
			_, side := p.placedBy[n]
			_, bestSide := p.placedBy[best.node]
			if f, s := p.provides(op, prm, p.res[n]); s > best.score || s > 0 && s == best.score && bestSide && !side {
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
		if op.Method != http.MethodGet || p.used[op] || len(p.needs(op)) == 0 {
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
		base := p.byKeyName(op)
		if base == "" {
			base = p.viewName(op)
		}
		c := &Node{Name: p.name(base, op), Call: callOf(op), Comment: op.ID, Parent: parent.node}
		for _, nd := range p.needs(op) {
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
			// placed there for a filter its operationId names
			for name, pr := range provs {
				if h, ok := p.hints(op)[name]; ok && pr.node == parent.node && p.res[parent.node] != nil && h.res.Name == p.res[parent.node].Name {
					p.placedBy[c] = h
				}
			}
		}
		p.createdReferences(c, op)
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
	// list nodes first: they hold the records of a resource; a list by the
	// key of its parent before a list a filter of the operationId gives
	// values to (the gardens of a moon before the gardens of a person),
	// a list of one state last (getOpenGardens: the actions start from
	// other states)
	for pass := range 4 {
		for _, n := range nodes {
			r := p.res[n]
			if r == nil || p.home[r] != nil || n.Off || len(n.Key) == 0 {
				continue
			}
			op := p.lookup(n.Call)
			if op == nil {
				continue
			}
			tier := 3
			// a node that reads the records another refers to is home
			// only when no other node holds the resource
			if isList(op) && !p.referenced[n] {
				tier = 0
				if len(p.hints(op)) > 0 {
					tier = 1
				}
				if _, vals := states(r, parseOpName(op.ID).subject); op.HasOperationID && len(vals) > 0 {
					tier = 2
				}
			}
			if tier != pass {
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
	// actions (POST /gardens/{gardenId}/open) go to attachWrites
	creates := slices.DeleteFunc(r.OpsWith(model.RoleCreate), func(mo *model.Op) bool { return actionName(p.s, mo.Op) != "" })
	if len(creates) == 0 {
		out[WriteCreate] = &Write{Kind: WriteCreate, None: true, Note: "no create of " + r.Name + " in the spec: the instance must hold these records"}
	} else {
		// the create at the path the node reads (POST /gardens/{gardenId}/beds
		// for GET /gardens/{gardenId}/beds), else the first; the others go
		// to the nodes that read their paths
		first := creates[0]
		if read := p.lookup(n.Call); read != nil {
			if i := slices.IndexFunc(creates, func(mo *model.Op) bool { return mo.Op.Path == read.Path }); i >= 0 {
				first = creates[i]
			}
		}
		add(WriteCreate, first)
	}
	// a write by another key than the first (PUT /gardens/name/{name} next
	// to PUT /gardens/{gardenId}) is a variant: update-by-name
	variant := func(base string, ops []*model.Op) {
		ops = slices.DeleteFunc(ops, func(mo *model.Op) bool { return actionName(p.s, mo.Op) != "" })
		slices.SortStableFunc(ops, func(a, b *model.Op) int { return keyRank(r, a) - keyRank(r, b) })
		for _, mo := range ops {
			kind := base
			if _, ok := out[base]; ok {
				kind = variantName(base, mo, out)
			}
			add(kind, mo)
		}
	}
	var puts, patches []*model.Op
	for _, mo := range r.OpsWith(model.RoleUpdate) {
		if mo.Op.Method == http.MethodPatch {
			patches = append(patches, mo)
		} else {
			puts = append(puts, mo)
		}
	}
	variant(WriteUpdate, puts)
	variant(WritePatch, patches)
	variant(WriteDelete, r.OpsWith(model.RoleDelete))
	return out
}

// variantName names a write by another key: by the field its path takes
// (update-by-name), when that is taken by the filter of its operationId
// (UpdateGardenByMoon: update-by-moon), else numbered.
func variantName(base string, mo *model.Op, taken map[string]*Write) string {
	kind := base + "-by-" + kebab(byField(mo))
	if taken[kind] == nil {
		return kind
	}
	if by := parseOpName(mo.Op.ID).by; mo.Op.HasOperationID && len(by) > 0 {
		var parts []string
		for _, p := range by {
			parts = append(parts, strings.Join(p, "-"))
		}
		if k := base + "-by-" + strings.Join(parts, "-and-"); taken[k] == nil {
			return k
		}
	}
	for i := 2; ; i++ {
		if k := fmt.Sprintf("%s-%d", kind, i); taken[k] == nil {
			return k
		}
	}
}

// keyRank orders the operations of a resource by the key their path takes:
// the first key of the record (keyPointers: its id) first.
func keyRank(r *model.Resource, mo *model.Op) int {
	f := byField(mo)
	for i, k := range keyPointers(r) {
		if strings.EqualFold(PointerField(k), f) {
			return i
		}
	}
	return len(r.Keys) + 1
}

// byField is the field of the record the last path parameter of an
// operation takes ("name" of PUT /gardens/name/{name}).
func byField(mo *model.Op) string {
	if len(mo.Params) == 0 {
		return ""
	}
	pm := mo.Params[len(mo.Params)-1]
	if pm.Field != "" {
		return pm.Field
	}
	return pm.Name
}

// kebab writes a camelCase name in lower case with dashes: personId →
// person-id; an abbreviation stays one word: URL → url, HTTPPort →
// http-port.
func kebab(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && !unicode.IsUpper(rs[i-1])
			// the last capital of an abbreviation starts the next word
			endsAbbr := i > 0 && unicode.IsUpper(rs[i-1]) && i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if i > 0 && (prevLower || endsAbbr) {
				b.WriteByte('-')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "key"
	}
	return b.String()
}

// write is the write of a record of node n (resource r, nil if unknown):
// its values, and the body from the read that covers the request best.
func (p *proposer) write(kind string, op *spec.Operation, r *model.Resource, n *Node) *Write {
	p.written[op] = true
	w := &Write{Kind: kind, Call: callOf(op), Comment: op.ID}
	vals := p.writeParams(op, r, n, kind == WriteCreate)
	for _, nd := range p.needs(op) {
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
	for _, nd := range p.needs(op) {
		prm := nd.name
		// the record itself: a field that names the value
		self, selfScore := p.provides(op, prm, r)
		// a create takes no key of the record (the API gives it), unless the
		// model maps the parameter to it: a key the client chooses
		// (POST /bookings/{berth}/{slot})
		if selfScore > 0 && selfScore < 100 && create && isKey(self) {
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
		// any node of the tree, but not for the parameter that names the
		// record an update or delete writes: a record of another node is a
		// guess (the pilot of a sibling node for a dock); $name says so
		own := false
		if ps := pathParams(op); len(ps) > 0 && ps[len(ps)-1] == prm && r != nil {
			seg := model.SegmentBefore(op.Path, prm)
			own = strings.EqualFold(seg, r.Name) || strings.EqualFold(singular(seg), r.Name)
		}
		if best == nil && (create || !own) {
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
	if o := p.lookup(n.Call); o != nil {
		own = cover(o, true)
	}
	best, score := n.Name, own
	for _, c := range n.Each {
		o := p.lookup(c.Call)
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

// keyPointers are the key fields of a resource as JSON pointers, its id
// first: a resource read by id, name and code picks by id.
func keyPointers(r *model.Resource) []string {
	var out []string
	for _, k := range r.Keys {
		out = append(out, "/"+k)
	}
	idRank := func(ptr string) int {
		switch f := strings.ToLower(PointerField(ptr)); {
		case f == "id":
			return 0
		case strings.HasSuffix(f, "id"):
			return 1
		}
		return 2
	}
	slices.SortStableFunc(out, func(a, b string) int { return idRank(a) - idRank(b) })
	if len(out) == 0 {
		if f := r.Field("id"); f != "" {
			out = []string{"/" + f}
		}
	}
	return out
}

// listName names a list node after the last word of its path ("docks"),
// else after the resource.
func (p *proposer) listName(op *spec.Operation, r *model.Resource) string {
	seg := lastLiteral(op.Path)
	switch {
	case seg == "" || slices.Contains([]string{"list", "all", "search"}, strings.ToLower(seg)):
	case strings.EqualFold(seg, r.Name):
		// GET /Planet lists planets: the singular names the read of one
	case p.namesOther(seg, r):
		// GET /mechanics/dock/{dockNumber} lists mechanics, not docks
	default:
		return lowerFirst(seg)
	}
	return lowerFirst(pluralOf(r.Name))
}

// pluralOf is the plural of an English word: dock → docks, entry →
// entries, box → boxes.
func pluralOf(w string) string {
	lw := strings.ToLower(w)
	switch {
	case strings.HasSuffix(lw, "y") && len(lw) > 1 && !strings.ContainsRune("aeiou", rune(lw[len(lw)-2])):
		return w[:len(w)-1] + "ies"
	case strings.HasSuffix(lw, "s"), strings.HasSuffix(lw, "x"), strings.HasSuffix(lw, "ch"), strings.HasSuffix(lw, "sh"):
		return w + "es"
	}
	return w + "s"
}

// viewName names a read after the last word of its path, or after its
// resource when that word names another resource (GET
// /reports/gardens/{gardenId} answers a GardenReport: gardenReport).
func (p *proposer) viewName(op *spec.Operation) string {
	name := viewName(op)
	res := p.resourceOf(op)
	if res == nil || isList(op) {
		return name
	}
	for _, r := range p.m.Resources {
		if r != res && sameName(r.Name, []string{name}) {
			return lowerFirst(res.Name)
		}
	}
	return name
}

// byKeyName names the read of a record by a key other than its first
// (GET /gardens/name/{name}: gardenByName); "" for any other operation.
func (p *proposer) byKeyName(op *spec.Operation) string {
	mo := p.m.Op(op)
	if mo == nil || mo.Role != model.RoleRead || len(mo.Params) == 0 || keyRank(mo.Resource, mo) == 0 {
		return ""
	}
	f := byField(mo)
	if !slices.ContainsFunc(mo.Resource.Keys, func(k string) bool { return strings.EqualFold(k, f) }) {
		return ""
	}
	return lowerFirst(mo.Resource.Name) + "By" + strings.ToUpper(f[:1]) + f[1:]
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
		kind := actionName(p.s, op)
		if kind == "" {
			kind = writeKind(op.Method)
		}
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
		action := IsAction(kind)
		for _, n := range p.order() {
			// an action goes to its node even when the name is taken there:
			// both get names of their own below
			if n.Off || n.Write[kind] != nil && !action {
				continue
			}
			// a create never goes to the read of one record below a node of
			// the same resource: the scenario creates the records of that node
			if kind == WriteCreate && n.Parent != nil && p.res[n] != nil && p.res[n.Parent] == p.res[n] {
				if o := p.lookup(n.Call); o != nil && !isList(o) {
					continue
				}
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
		if old := best.Write[kind]; action && old != nil {
			// two actions of one name at a node (link to a person, link to a
			// moon): each named by its operationId
			if oop := p.lookup(old.Call); oop != nil {
				delete(best.Write, kind)
				old.Kind = actionLabel(oop, p.res[best], kind, best.Write)
				best.Write[old.Kind] = old
			}
			kind = actionLabel(op, p.res[best], kind, best.Write)
		}
		best.Write[kind] = p.write(kind, op, p.res[best], best)
	}
}

// actionLabel names an action by its operationId without the words of its
// resource (linkGardenToPerson of a garden: link-to-person); a name taken
// gets a number.
func actionLabel(op *spec.Operation, r *model.Resource, kind string, taken map[string]*Write) string {
	label := ""
	if op.HasOperationID {
		ws := words(op.ID)
		if r != nil {
			for _, w := range words(r.Name) {
				for _, x := range []string{w, w + "s"} {
					if i := slices.Index(ws[1:], x); i >= 0 {
						ws = slices.Delete(ws, i+1, i+2)
						break
					}
				}
			}
		}
		label = strings.Join(ws, "-")
	}
	if label == "" || label == kind {
		label = kind + "-" + kebab(lastParam(op))
	}
	if taken[label] == nil {
		return label
	}
	for i := 2; ; i++ {
		if k := fmt.Sprintf("%s-%d", label, i); taken[k] == nil {
			return k
		}
	}
}

// lastParam is the last path parameter of an operation, "" without.
func lastParam(op *spec.Operation) string {
	if ps := pathParams(op); len(ps) > 0 {
		return ps[len(ps)-1]
	}
	return ""
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
// resource (100, +10 when it reads the path op writes), reads the path op
// writes (90), gives the key of the first path parameter (70) or of
// another (60), lies on its path (50), or its records cover the body (40).
// 0: not.
func (p *proposer) writesTo(op *spec.Operation, mo *model.Op, n *Node) int {
	read := p.lookup(n.Call)
	if read == nil {
		return 0
	}
	r := p.res[n]
	score := 0
	if mo != nil && r == mo.Resource {
		score = 100
	}
	if read.Path == op.Path {
		// a node of the resource that reads the path the write writes beats
		// the other nodes of the resource (the beds of a garden by code for
		// POST /gardens/code/{code}/beds)
		if score >= 100 {
			score += 10
		}
		score = max(score, 90)
	}
	for i, prm := range pathParams(op) {
		if f, s := p.provides(op, prm, r); s >= 90 && r != nil && slices.ContainsFunc(r.Keys, func(k string) bool { return strings.EqualFold(k, f) }) {
			// the first parameter names the record the write is about
			// (/gardens/{gardenId}/persons/{personId}/link: the garden)
			if i == 0 {
				score = max(score, 70)
			}
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
	if score > 0 && r != nil && p.home[r] == n {
		// the node that holds the write block of the resource
		score += 5
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
	res := p.resourceOf(op)
	provs, _ := p.providers(op)
	filters := p.bodyFilters(op, res)
	var parent provider
	for _, pr := range slices.Concat(slices.Collect(maps.Values(provs)), slices.Collect(maps.Values(filters))) {
		if parent.node == nil || pr.at > parent.at {
			parent = pr
		}
	}
	ref := func(pr provider) string {
		if pr.node == parent.node {
			return "." + pr.field
		}
		return pr.node.Name + "." + pr.field
	}
	body := map[string]any{}
	for name, pr := range filters {
		body[name] = ref(pr)
	}
	c := &Node{Name: p.name(p.viewName(op), op), Call: callOf(op), Comment: op.ID, Parent: parent.node, Read: true,
		Body: body, HasBody: true, Pick: Pick{N: DefaultPick}}
	for _, nd := range p.needs(op) {
		pr, ok := provs[nd.name]
		switch {
		case !ok:
			c.set(nd, p.param(nd.name, op))
		default:
			c.set(nd, ref(pr))
		}
	}
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
