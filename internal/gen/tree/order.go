package tree

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// OrderEntry is a line of order: a node whose create runs at that place,
// and where values of its body come from; or a step of a node (its read,
// list, update, patch or action) that runs at that place.
//
//	order:
//	  - pilots
//	  - pilots.read
//	  - docks
//	  - ships: {pilotId: shipPilot.id}
//	  - ships.update
type OrderEntry struct {
	Node string
	// Step is the step of the node the line places: read, list, a write
	// kind (update, patch, update-by-name, an action); empty for its create.
	Step string
	// Values sets fields of the body of the create: field → a reference to
	// the record below the created one ("shipPilot.id": the pilot read
	// below the ship) or above it.
	Values map[string]any
	// Note is the comment after the line ("new").
	Note string
	Line int
}

// orderHead heads order, the top of the file: the syntax in short, for
// someone who reads the file the first time.
const orderHead = `How to read this file (all of it: apitest-gen tree -help):
  a node is one request; the nodes under each run once per record it picked
  .id            the field id of the record one step up (in write: of the record itself)
  docks.id       the field id of the node docks above (else of its first record)
  .route.id      a field inside a field
  /id            a place in the answer: only in key, save, items, page
  $season        a value you write under params
  '{{a}}-{{b}}'  values glued into one text
order: what the test creates, top down; it deletes bottom up
  - ships: {pilotId: shipPilot.id}   pilotId of the new ship = id of the shipPilot below it
  - docks.update                     this step of docks runs here
A problem says "change line N: old → new"; apitest-gen tree -fix makes it.`

// oldHeads are the texts above order of earlier versions: a merge puts
// orderHead in their place.
var oldHeads = []string{`Creates top down, deletes bottom up; fetch, test and run follow it.
"field: node.key": where a value of the body comes from.
"node.read", "node.list", "node.update", "node.patch", "node.<action>":
that step runs at that place; the steps not named run after the list.`}

// Step names of order besides the write kinds.
const (
	StepRead = "read"
	StepList = "list"
)

// Key is the name of the line: the node for its create, else node.step.
func (e OrderEntry) Key() string {
	if e.Step == "" {
		return e.Node
	}
	return e.Node + "." + e.Step
}

// noteNew marks an entry a merge added.
const noteNew = "new: check its place"

// Strict reports whether the file has an order: then the test follows it
// and the values the file gives, and nothing is guessed.
func (t *Tree) Strict() bool { return t.HasOrder }

// OrderIndex is the place of a node's create in order, -1 when it has none.
func (t *Tree) OrderIndex(name string) int {
	return slices.IndexFunc(t.Order, func(e OrderEntry) bool { return e.Node == name && e.Step == "" })
}

// StepIndex is the place of a line of order by its key (ships.update), -1
// when there is none.
func (t *Tree) StepIndex(key string) int {
	return slices.IndexFunc(t.Order, func(e OrderEntry) bool { return e.Key() == key })
}

// HasSteps reports whether order places steps besides the creates.
func (t *Tree) HasSteps() bool {
	return slices.ContainsFunc(t.Order, func(e OrderEntry) bool { return e.Step != "" })
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
		if node, step, ok := strings.Cut(e.Node, "."); ok {
			e.Node, e.Step = node, step
			if !nameForm(step) {
				p.errf(item, "order: %q is no step of a node (ships.read, ships.update)", e.Key())
				continue
			}
			if e.Values != nil {
				p.errf(item, "order: %s: the sources of a body stand at the create (- %s: {…})", e.Key(), node)
				continue
			}
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
		n = scalar(e.Key())
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
func checkOrder(s *spec.Spec, t *Tree) ([]string, []Fix) {
	if !t.HasOrder {
		return nil, nil
	}
	var out []string
	var fixes []Fix
	add := func(line int, format string, args ...any) {
		out = append(out, fmt.Sprintf("line %d: %s", line, fmt.Sprintf(format, args...)))
	}
	fix := func(f Fix, format string, args ...any) {
		out = append(out, fmt.Sprintf("line %d: %s", f.Line, fmt.Sprintf(format, args...))+f.change())
		if f.apply != nil {
			fixes = append(fixes, f)
		}
	}
	m := modelOf(s)
	seen := map[string]int{}
	for i, e := range t.Order {
		if j, dup := seen[e.Key()]; dup {
			fix(Fix{Line: e.Line, Change: "remove the line (- " + e.Key() + ")", apply: removeOrderLine(e.Line)},
				"order: %s is listed twice (also line %d)", e.Key(), t.Order[j].Line)
			continue
		}
		seen[e.Key()] = i
		n := t.Node(e.Node)
		if n == nil || n.IsRequire {
			// a node removed from the tree: its line goes
			f := Fix{Line: e.Line, Change: "remove the line (- " + e.Key() + ")", apply: removeOrderLine(e.Line)}
			if like := nodeLike(t, e.Node); like != "" {
				f = Fix{Line: e.Line, Change: e.Node + " → " + like, apply: renameOrderLine(e.Line, like)}
			}
			fix(f, "order: %s is no node of the tree", e.Node)
			continue
		}
		if n.Gone {
			continue
		}
		if e.Step != "" {
			checkStep(s, m, t, i, e, n, fix)
			continue
		}
		w, op := ownCreate(s, n)
		if w == nil {
			fix(Fix{Line: e.Line, Change: "remove the line (- " + e.Node + ")", apply: removeOrderLine(e.Line)},
				"order: %s creates nothing (its write block has no create)", e.Node)
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
				fix(Fix{Line: t.Order[j].Line, Change: fmt.Sprintf("move - %s above - %s (line %d)", owner.Name, e.Node, e.Line), apply: moveOrderLine(t.Order[j].Line, e.Line)},
					"order: %s comes before %s, but the create of %s takes a value of %s", e.Node, owner.Name, e.Node, what)
			}
		}
		props, _ := properties(schemaOf(requestRef(op)))
		for _, field := range sortedKeys(e.Values) {
			if len(props) > 0 {
				if _, _, ok := bodyField(props, field); !ok {
					fix(Fix{Line: e.Line, Change: fmt.Sprintf("remove %s: %v (the body has no such field)", field, e.Values[field]), apply: setOrderSource(e.Line, field, "")},
						"order: %s: the body of %s has no field %s", e.Node, callOf(op), field)
					continue
				}
			}
			ref, ok := ParseRef(e.Values[field], t.names())
			if !ok || ref.Node == "" {
				f := Fix{Line: e.Line, Change: fmt.Sprintf("%s: %v → %s: <node>.<field> (a node below or above %s)", field, e.Values[field], field, e.Node)}
				if unknownNodeRef(t, e.Values[field]) {
					// the node is gone: the source goes, the check names a new one
					f = Fix{Line: e.Line, Change: fmt.Sprintf("remove %s: %v (no such node)", field, e.Values[field]), apply: setOrderSource(e.Line, field, "")}
				}
				fix(f, "order: %s.%s: %v is no reference to a node", e.Node, field, e.Values[field])
				continue
			}
			src := t.Node(ref.Node)
			if src == nil || !below(src, n) && n.Ancestor(ref.Node) == nil {
				fix(Fix{Line: e.Line, Change: fmt.Sprintf("remove %s: %s, or name a node below or above %s", field, ref, e.Node), apply: setOrderSource(e.Line, field, "")},
					"order: %s.%s: %s is neither below nor above %s", e.Node, field, ref.Node, e.Node)
				continue
			}
			sop := lookupOp(s, src.Call)
			if sop == nil || ref.Field == "" {
				continue
			}
			sprops, _ := properties((&proposer{}).recordSchema(sop))
			if len(sprops) == 0 {
				continue
			}
			want := fieldType(props, field)
			got, has := propertyFoldIn(sprops, strings.SplitN(ref.Field, ".", 2)[0])
			switch {
			case !has:
				fix(sourceFix(e, field, ref, src, sprops, want), "order: %s.%s: the records of %s have no field %s", e.Node, field, ref.Node, ref.Field)
			case !strings.Contains(ref.Field, ".") && want != "" && got != nil && value.Type(got.Value) != "" && value.Type(got.Value) != want:
				fix(sourceFix(e, field, ref, src, sprops, want), "order: %s.%s: the body takes %s, %s is %s", e.Node, field, article(want), ref, article(value.Type(got.Value)))
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
	return out, fixes
}

// fieldType is the type of a body field (a path for one inside an object),
// of its items for a list: the type a source must have.
func fieldType(props openapi3.Schemas, field string) string {
	p, _, ok := bodyField(props, field)
	if !ok || p == nil || p.Value == nil {
		return ""
	}
	if value.Type(p.Value) == "array" && p.Value.Items != nil {
		return value.Type(p.Value.Items.Value)
	}
	return value.Type(p.Value)
}

// sourceFix is the change for a source of order with no such field, or a
// field of another type: the key of the source node of the type the body
// takes, else the fields to choose from.
func sourceFix(e OrderEntry, field string, ref Ref, src *Node, sprops openapi3.Schemas, want string) Fix {
	keys := slices.Clone(src.Key)
	keys = append(keys, "/id")
	for _, k := range keys {
		name, ok := propertyFoldName(sprops, strings.TrimPrefix(k, "/"))
		if !ok || strings.Contains(strings.TrimPrefix(k, "/"), "/") {
			continue
		}
		if p := sprops[name]; p != nil && p.Value != nil && (want == "" || value.Type(p.Value) == want) && name != ref.Field {
			to := ref.Node + "." + name
			return Fix{Line: e.Line, Change: fmt.Sprintf("%s: %s → %s: %s", field, ref, field, to), apply: setOrderSource(e.Line, field, to)}
		}
	}
	return Fix{Line: e.Line, Change: fmt.Sprintf("%s: %s → %s: %s.<one of %s>", field, ref, field, ref.Node, strings.Join(sortedKeys(sprops), ", "))}
}

// article puts a or an before a type name.
func article(typ string) string {
	if strings.ContainsAny(typ[:1], "aeiou") {
		return "an " + typ
	}
	return "a " + typ
}

// nodeLike is the node whose name differs from name only in case, "" when
// there is none or several.
func nodeLike(t *Tree, name string) string {
	var found []string
	t.Walk(func(n *Node) {
		if !n.IsRequire && strings.EqualFold(n.Name, name) && n.Name != name {
			found = append(found, n.Name)
		}
	})
	if len(found) != 1 {
		return ""
	}
	return found[0]
}

// renameOrderLine sets the node a line of order names.
func renameOrderLine(line int, name string) func(*Tree) bool {
	return func(t *Tree) bool {
		section, i := orderItem(t, line)
		if section == nil {
			return false
		}
		item := section.Content[i]
		if item.Kind == yaml.MappingNode {
			item = item.Content[0]
		}
		_, step, _ := strings.Cut(item.Value, ".")
		item.Value = name
		if step != "" {
			item.Value += "." + step
		}
		return true
	}
}

// stepsOf are the steps of a node order can place: its read or list, and
// its updates, patches and actions.
func stepsOf(s *spec.Spec, n *Node) []string {
	var out []string
	if op := lookupOp(s, n.Call); op != nil {
		if n.HasItems || isList(op) {
			out = append(out, StepList)
		} else {
			out = append(out, StepRead)
		}
	}
	for _, kind := range n.Kinds() {
		if base := KindBase(kind); base != WriteUpdate && base != WritePatch && !IsAction(kind) {
			continue
		}
		if w := n.Write[kind]; w != nil && !w.None && !w.Off && !w.Gone && lookupOp(s, w.Call) != nil {
			out = append(out, kind)
		}
	}
	return out
}

// checkStep verifies a step line of order: the node has the step, and the
// creates of its records and of the records above them come before it.
func checkStep(s *spec.Spec, m *model.Model, t *Tree, i int, e OrderEntry, n *Node, fix func(Fix, string, ...any)) {
	steps := stepsOf(s, n)
	if !slices.Contains(steps, e.Step) {
		remove := Fix{Line: e.Line, Change: "remove the line (- " + e.Key() + ")", apply: removeOrderLine(e.Line)}
		switch {
		case e.Step == WriteCreate && t.OrderIndex(e.Node) >= 0:
			fix(remove, "order: %s: the node alone stands for its create (- %s)", e.Key(), e.Node)
		case e.Step == WriteCreate:
			fix(Fix{Line: e.Line, Change: e.Key() + " → " + e.Node, apply: renameOrderLine(e.Line, e.Node)},
				"order: %s: the node alone stands for its create (- %s)", e.Key(), e.Node)
		case KindBase(e.Step) == WriteDelete:
			fix(remove, "order: %s: the deletes run at the end, bottom up", e.Key())
		case len(steps) == 0:
			fix(remove, "order: %s: %s has no step order can place", e.Key(), e.Node)
		default:
			to := e.Node + "." + steps[0]
			if len(steps) > 1 {
				to = e.Node + ".<one of " + strings.Join(steps, ", ") + ">"
			}
			fix(Fix{Line: e.Line, Change: e.Key() + " → " + to},
				"order: %s: %s has no step %s", e.Key(), e.Node, e.Step)
		}
		return
	}
	for c := n; c != nil; c = c.Parent {
		owner := createOwner(s, m, t, c)
		if owner == nil {
			continue
		}
		if j := t.OrderIndex(owner.Name); j > i {
			fix(Fix{Line: t.Order[j].Line, Change: fmt.Sprintf("move - %s above - %s (line %d)", owner.Name, e.Key(), e.Line), apply: moveOrderLine(t.Order[j].Line, e.Line)},
				"order: %s comes before %s, but it needs the records %s creates", e.Key(), owner.Name, owner.Name)
		}
	}
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
	created := createdResources(s, m, t)
	for i, n := range nodes {
		index[n] = i
		// the sources of the fields that name a record the test creates,
		// from a node below or above (the check asks for them)
		w, op := ownCreate(s, n)
		for _, need := range refNeeds(s, m, t, n, w, op, created) {
			if need.source == "" {
				continue
			}
			if values[n.Name] == nil {
				values[n.Name] = map[string]any{}
			}
			if _, _, has := foldEntry(values[n.Name], need.field); !has {
				values[n.Name][need.field] = need.source
			}
		}
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
	m.tidyOrderHead()
	section := valueOf(root, "order")
	if !m.t.HasOrder || section == nil || section.Kind != yaml.SequenceNode {
		// the whole order: the creates of the file in the order of what
		// they need, with the sources the proposal gives them
		sources := map[string]map[string]any{}
		for _, e := range m.proposal.Order {
			if n := m.fileNode(e.Node); n != nil {
				sources[n.Name] = related(m.t, n, m.fileValues(e.Values))
			}
		}
		list := proposeOrder(m.s, modelOf(m.s), m.t, sources)
		if len(list) == 0 {
			return
		}
		key, val := orderKey(), orderYAML(list)
		if len(root.Content) > 0 && root.Content[0].HeadComment != "" {
			if old := withoutHead(root.Content[0].HeadComment); old != "" {
				key.HeadComment = old + "\n\n" + key.HeadComment
			}
			root.Content[0].HeadComment = ""
			m.t.doc.SetHead(withoutHead(m.t.doc.Head()))
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
			after = "after " + m.t.Order[at-1].Key()
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
		if x.Step != "" {
			// a step on its records or on records below them
			for c := xn; c != nil; c = c.Parent {
				if createOwner(m.s, mod, m.t, c) == n {
					before = min(before, i)
				}
			}
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
			sources[n.Name] = related(m.t, n, m.fileValues(e.Values))
		}
	}
	var out []OrderEntry
	m.t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone {
			return
		}
		if w, _ := ownCreate(m.s, n); w == nil || slices.ContainsFunc(list, func(e OrderEntry) bool { return e.Node == n.Name && e.Step == "" }) {
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

// related keeps the sources of a create whose node is below or above it
// in the file: a source the file has elsewhere fails the check.
func related(t *Tree, n *Node, vals map[string]any) map[string]any {
	scope := t.names()
	for field, v := range vals {
		ref, ok := ParseRef(v, scope)
		src := t.Node(ref.Node)
		if !ok || src == nil || !below(src, n) && n.Ancestor(ref.Node) == nil {
			delete(vals, field)
		}
	}
	if len(vals) == 0 {
		return nil
	}
	return vals
}

// withoutHead is a head comment without the paragraphs that explain order
// (they come with the order key).
func withoutHead(c string) string {
	var keep []string
	for _, p := range strings.Split(c, "\n\n") {
		if strings.TrimSpace(p) != "" && !isOrderHead(p) {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "\n\n")
}

// isOrderHead reports whether a paragraph of a comment is orderHead, or
// the text of an earlier version.
func isOrderHead(p string) bool {
	return slices.ContainsFunc(append([]string{orderHead}, oldHeads...), func(h string) bool { return commentText(p) == commentText(h) })
}

// commentText is a comment without "#" and the spaces around its lines.
func commentText(c string) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(c), "\n") {
		lines = append(lines, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "#")))
	}
	return strings.Join(lines, "\n")
}

// tidyOrderHead keeps orderHead once, above order: files of an earlier
// version got it again with each new order, and yaml.v3 keeps all but the
// last block of comment lines at the top with the document.
func (m *merger) tidyOrderHead() {
	doc, root := m.t.doc, m.t.doc.Root
	var key *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "order" {
			key = root.Content[i]
		}
	}
	if key == nil {
		return
	}
	head := withoutHead(doc.Head())
	own := withoutHead(key.HeadComment)
	if own != "" {
		own += "\n\n"
	}
	own += orderHead
	if head == doc.Head() && commentText(own) == commentText(key.HeadComment) {
		return
	}
	doc.SetHead(head)
	key.HeadComment = own
	m.changed("order", "the explanation at the top is new (once, the syntax in short)")
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
