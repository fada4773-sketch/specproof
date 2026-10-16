package tree

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// OrderEntry is a line of order: a node whose create runs at that place,
// and where values of its body come from.
//
//	order:
//	  - pilots
//	  - docks
//	  - ships: {pilotId: shipPilot.id}
type OrderEntry struct {
	Node string
	// Values sets fields of the body of the create: field → a reference to
	// the record below the created one ("shipPilot.id": the pilot read
	// below the ship) or above it.
	Values map[string]any
	// Note is the comment after the line ("new").
	Note string
	Line int
}

// orderHead explains the order section.
const orderHead = `Creates top down, deletes bottom up; fetch, test and run follow it.
"field: node.key": where a value of the body comes from.`

// noteNew marks an entry a merge added.
const noteNew = "new: check its place"

// Strict reports whether the file has an order: then the test follows it
// and the values the file gives, and nothing is guessed.
func (t *Tree) Strict() bool { return t.HasOrder }

// OrderIndex is the place of a node in order, -1 when it has none.
func (t *Tree) OrderIndex(name string) int {
	return slices.IndexFunc(t.Order, func(e OrderEntry) bool { return e.Node == name })
}

// Entry is the order entry of a node, nil without.
func (t *Tree) Entry(name string) *OrderEntry {
	if i := t.OrderIndex(name); i >= 0 {
		return &t.Order[i]
	}
	return nil
}

func (p *parser) order(v *yaml.Node) []OrderEntry {
	if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
		return nil
	}
	if v.Kind != yaml.SequenceNode {
		p.errf(v, "order must be a list of node names (- docks, - ships: {pilotId: shipPilot.id})")
		return nil
	}
	var out []OrderEntry
	for _, item := range v.Content {
		e := OrderEntry{Line: item.Line, Note: noteOf(item)}
		switch item.Kind {
		case yaml.ScalarNode:
			e.Node = item.Value
		case yaml.MappingNode:
			if len(item.Content) != 2 {
				p.errf(item, "order: one node per line (- ships: {pilotId: shipPilot.id})")
				continue
			}
			e.Node = item.Content[0].Value
			if e.Note == "" {
				e.Note = noteOf(item.Content[1])
			}
			if val := item.Content[1]; val.Kind != yaml.ScalarNode || val.Tag != "!!null" {
				m, ok := p.valueMap(val, "order."+e.Node)
				if !ok {
					continue
				}
				e.Values = m
			}
		default:
			p.errf(item, "order: a node name, or a node with the sources of its body")
			continue
		}
		if !nameForm(e.Node) {
			p.errf(item, "order: %q is no node name", e.Node)
			continue
		}
		out = append(out, e)
	}
	return out
}

func orderKey() *yaml.Node {
	k := scalar("order")
	k.HeadComment = orderHead
	return k
}

func orderYAML(list []OrderEntry) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, e := range list {
		seq.Content = append(seq.Content, entryYAML(e))
	}
	return seq
}

func entryYAML(e OrderEntry) *yaml.Node {
	var n *yaml.Node
	if len(e.Values) == 0 {
		n = scalar(e.Node)
	} else {
		v := valueYAML(e.Values, true)
		v.Style = yaml.FlowStyle
		n = &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar(e.Node), v}}
	}
	n.LineComment = comment(e.Note)
	return n
}

// models caches the model of a spec: a check runs often.
var models sync.Map

func modelOf(s *spec.Spec) *model.Model {
	if m, ok := models.Load(s); ok {
		return m.(*model.Model)
	}
	m := model.Detect(s, nil)
	models.Store(s, m)
	return m
}

// ownCreate is the create of a node's own write block, nil without.
func ownCreate(s *spec.Spec, n *Node) (*Write, *spec.Operation) {
	w := n.Write[WriteCreate]
	if w == nil || w.None || w.Off || w.Gone {
		return nil, nil
	}
	op := lookupOp(s, w.Call)
	if op == nil {
		return nil, nil
	}
	return w, op
}

// createOwner is the node whose create rebuilds the records of n: n itself,
// else another node of the same resource (the pilot below a ship creates
// like the list of pilots), if that create names no node above it that n
// lacks; nil when none does.
func createOwner(s *spec.Spec, m *model.Model, t *Tree, n *Node) *Node {
	if w, _ := ownCreate(s, n); w != nil || n.Write[WriteCreate] != nil {
		if w == nil {
			return nil
		}
		return n
	}
	op := lookupOp(s, n.Call)
	if op == nil {
		return nil
	}
	res := resourceOf(m, op)
	if res == nil {
		return nil
	}
	var found *Node
	t.Walk(func(o *Node) {
		if found != nil || o == n || o.IsRequire || o.Gone {
			return
		}
		oop := lookupOp(s, o.Call)
		if oop == nil || resourceOf(m, oop) != res {
			return
		}
		w, _ := ownCreate(s, o)
		if w == nil {
			return
		}
		ok := true
		walkStrings(anyMap(w.Path), func(str string) {
			if ref, isRef := ParseRef(str, func(name string) bool { return o.Ancestor(name) != nil }); isRef && ref.Node != "" && n.Ancestor(ref.Node) == nil {
				ok = false
			}
		})
		if ok {
			found = o
		}
	})
	return found
}

// below reports whether x is n or a node below it.
func below(x, n *Node) bool {
	for c := x; c != nil; c = c.Parent {
		if c == n {
			return true
		}
	}
	return false
}

// createRefs are the nodes a create of n reads by name: its path, query,
// header and body values ("planets.code"), and the sources of order.
func createRefs(t *Tree, n *Node, w *Write, e *OrderEntry) []string {
	scope := t.names()
	var out []string
	add := func(s string) {
		for _, part := range refsOf(s, scope) {
			if ref, ok := ParseRef(part, scope); ok && ref.Node != "" && ref.Node != n.Name && !slices.Contains(out, ref.Node) {
				out = append(out, ref.Node)
			}
		}
	}
	if w != nil {
		for _, v := range []any{anyMap(w.Path), anyMap(w.Query), anyMap(w.Header), w.Body} {
			walkStrings(v, add)
		}
	}
	if e != nil {
		walkStrings(anyMap(e.Values), add)
	}
	return out
}

// checkOrder verifies the order of a file: it names nodes of the tree with
// a create, once each; every node with a create has a place; a create comes
// after the creates of the records it reads by name; every source of a
// value is a node below or above the created one, with that field, and the
// body of the create has the field.
func checkOrder(s *spec.Spec, t *Tree) []string {
	if !t.HasOrder {
		return nil
	}
	var out []string
	add := func(line int, format string, args ...any) {
		out = append(out, fmt.Sprintf("line %d: %s", line, fmt.Sprintf(format, args...)))
	}
	m := modelOf(s)
	seen := map[string]int{}
	for i, e := range t.Order {
		if j, dup := seen[e.Node]; dup {
			add(e.Line, "order: %s is listed twice (also line %d)", e.Node, t.Order[j].Line)
			continue
		}
		seen[e.Node] = i
		n := t.Node(e.Node)
		if n == nil {
			add(e.Line, "order: %s is no node of the tree", e.Node)
			continue
		}
		if n.Gone {
			continue
		}
		w, op := ownCreate(s, n)
		if w == nil {
			add(e.Line, "order: %s creates nothing (its write block has no create); remove the line", e.Node)
			continue
		}
		for _, ref := range createRefs(t, n, w, &e) {
			r := t.Node(ref)
			if r == nil {
				continue
			}
			owner := createOwner(s, m, t, r)
			if owner == nil || owner == n {
				continue
			}
			if j := t.OrderIndex(owner.Name); j > i {
				what := ref
				if owner.Name != ref {
					what = ref + " (created like " + owner.Name + ")"
				}
				add(e.Line, "order: %s comes before %s, but the create of %s takes a value of %s; move %s above %s",
					e.Node, owner.Name, e.Node, what, owner.Name, e.Node)
			}
		}
		props, _ := properties(schemaOf(requestRef(op)))
		for _, field := range sortedKeys(e.Values) {
			if len(props) > 0 {
				if _, ok := propertyFoldIn(props, field); !ok {
					add(e.Line, "order: %s: the body of %s has no field %s", e.Node, callOf(op), field)
					continue
				}
			}
			ref, ok := ParseRef(e.Values[field], t.names())
			if !ok || ref.Node == "" {
				add(e.Line, "order: %s.%s: %v is no reference to a node (shipPilot.id)", e.Node, field, e.Values[field])
				continue
			}
			src := t.Node(ref.Node)
			if src == nil || !below(src, n) && n.Ancestor(ref.Node) == nil {
				add(e.Line, "order: %s.%s: %s is neither below nor above %s", e.Node, field, ref.Node, e.Node)
				continue
			}
			if sop := lookupOp(s, src.Call); sop != nil && ref.Field != "" {
				if sprops, _ := properties((&proposer{}).recordSchema(sop)); len(sprops) > 0 {
					if _, ok := propertyFoldIn(sprops, strings.SplitN(ref.Field, ".", 2)[0]); !ok {
						add(e.Line, "order: %s.%s: the records of %s have no field %s", e.Node, field, ref.Node, ref.Field)
					}
				}
			}
		}
	}
	t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone {
			return
		}
		if w, _ := ownCreate(s, n); w != nil {
			if _, ok := seen[n.Name]; !ok {
				add(w.Line, "%s: its create has no place in order; add it to order", n.Name)
			}
		}
	})
	return out
}

// proposeOrder gives the creates of the tree their order: each after the
// creates of the records it reads by name, else in the order of the tree.
// values are the sources of the body found while proposing.
func proposeOrder(s *spec.Spec, m *model.Model, t *Tree, values map[string]map[string]any) []OrderEntry {
	t.Complete(s)
	var nodes []*Node
	t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone {
			return
		}
		if w, _ := ownCreate(s, n); w != nil {
			nodes = append(nodes, n)
		}
	})
	index := map[*Node]int{}
	for i, n := range nodes {
		index[n] = i
	}
	needs := make([][]int, len(nodes))
	for i, n := range nodes {
		w, _ := ownCreate(s, n)
		e := &OrderEntry{Node: n.Name, Values: values[n.Name]}
		for _, ref := range createRefs(t, n, w, e) {
			if r := t.Node(ref); r != nil {
				if owner := createOwner(s, m, t, r); owner != nil && owner != n {
					if j, ok := index[owner]; ok {
						needs[i] = append(needs[i], j)
					}
				}
			}
		}
	}
	idx, ok := stableTopo(needs)
	if !ok {
		idx = make([]int, len(nodes))
		for i := range idx {
			idx[i] = i
		}
	}
	var out []OrderEntry
	for _, i := range idx {
		out = append(out, OrderEntry{Node: nodes[i].Name, Values: values[nodes[i].Name]})
	}
	return out
}

// mergeOrder gives a file the order of the proposal: the whole section when
// the file has none, else a place for each create the order lacks (marked
// new), after the creates it reads by name. Entries of the file stay as
// they are.
func (m *merger) mergeOrder() {
	if len(m.proposal.Order) == 0 {
		return
	}
	m.t.Complete(m.s)
	root := m.t.doc.Root
	section := valueOf(root, "order")
	if !m.t.HasOrder || section == nil || section.Kind != yaml.SequenceNode {
		// the whole order: the creates of the file in the order of what
		// they need, with the sources the proposal gives them
		sources := map[string]map[string]any{}
		for _, e := range m.proposal.Order {
			if n := m.fileNode(e.Node); n != nil {
				sources[n.Name] = m.fileValues(e.Values)
			}
		}
		list := proposeOrder(m.s, modelOf(m.s), m.t, sources)
		if len(list) == 0 {
			return
		}
		key, val := orderKey(), orderYAML(list)
		if len(root.Content) > 0 && root.Content[0].HeadComment != "" {
			key.HeadComment = root.Content[0].HeadComment + "\n\n" + key.HeadComment
			root.Content[0].HeadComment = ""
		}
		if section != nil {
			for i := 0; i+1 < len(root.Content); i += 2 {
				if root.Content[i+1] == section {
					root.Content = slices.Delete(root.Content, i, i+2)
					break
				}
			}
		}
		root.Content = append([]*yaml.Node{key, val}, root.Content...)
		m.changed("order", "proposed: %d creates in the order of what they need; yours from now on", len(list))
		m.reload()
		return
	}
	for _, e := range m.missingCreates(m.t.Order) {
		n := m.t.Node(e.Node)
		at := m.placeOf(n, e)
		e.Note = noteNew
		section.Content = slices.Insert(section.Content, at, entryYAML(e))
		after := "at the top"
		if at > 0 {
			after = "after " + m.t.Order[at-1].Node
		}
		m.changed("order."+e.Node, "new, %s; check its place", after)
		m.reload()
		m.t.Complete(m.s)
		section = valueOf(m.t.doc.Root, "order")
	}
}

// placeOf is the place in order for a new create: after the creates it
// takes values of, before the entries that take values of it; else last.
func (m *merger) placeOf(n *Node, e OrderEntry) int {
	mod := modelOf(m.s)
	w, _ := ownCreate(m.s, n)
	after := -1
	for _, ref := range createRefs(m.t, n, w, &e) {
		if r := m.t.Node(ref); r != nil {
			if owner := createOwner(m.s, mod, m.t, r); owner != nil && owner != n {
				after = max(after, m.t.OrderIndex(owner.Name))
			}
		}
	}
	before := len(m.t.Order)
	for i, x := range m.t.Order {
		xn := m.t.Node(x.Node)
		if xn == nil {
			continue
		}
		xw, _ := ownCreate(m.s, xn)
		for _, ref := range createRefs(m.t, xn, xw, &x) {
			if r := m.t.Node(ref); r != nil && createOwner(m.s, mod, m.t, r) == n {
				before = min(before, i)
			}
		}
	}
	if after+1 > before {
		return before // the needs go round: Check names them
	}
	if before < len(m.t.Order) {
		return before
	}
	return max(after+1, len(m.t.Order))
}

// missingCreates are the nodes of the file with a create that list lacks,
// with the sources the proposal gives them.
func (m *merger) missingCreates(list []OrderEntry) []OrderEntry {
	sources := map[string]map[string]any{}
	for _, e := range m.proposal.Order {
		if n := m.fileNode(e.Node); n != nil {
			sources[n.Name] = m.fileValues(e.Values)
		}
	}
	var out []OrderEntry
	m.t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone {
			return
		}
		if w, _ := ownCreate(m.s, n); w == nil || slices.ContainsFunc(list, func(e OrderEntry) bool { return e.Node == n.Name }) {
			return
		}
		out = append(out, OrderEntry{Node: n.Name, Values: sources[n.Name]})
	})
	return out
}

// fileValues are the sources of the proposal with the names of the nodes
// of the file; a source the file does not have is left out.
func (m *merger) fileValues(vals map[string]any) map[string]any {
	scope := m.proposal.names()
	out := map[string]any{}
	for field, v := range vals {
		ref, ok := ParseRef(v, scope)
		if !ok || ref.Node == "" {
			continue
		}
		if n := m.fileNode(ref.Node); n != nil {
			out[field] = n.Name + "." + ref.Field
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fileNode is the node of the file a node of the proposal became.
func (m *merger) fileNode(name string) *Node {
	if p := m.byName[name]; p != nil {
		if n := m.at[p]; n != nil {
			return n
		}
	}
	return m.t.Node(name)
}
