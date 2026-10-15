package tree

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Change is one thing a merge did to the file.
type Change struct {
	Where, What string
}

// Merge adds to the file t what the proposal has and the file not, and
// changes nothing else the file holds: an operation the file names nowhere
// (in a node, a require, a write block or ignore) is added at the place
// the proposal gives it, below the node of the file with the same call as
// the proposal's parent, else at the top; write kinds go into the write
// block of that node. A value no node of the file gives becomes "$name"
// with an entry in params. A create, update or patch without body gets
// "body: from" the proposal's read. The comments after the calls are
// refreshed (operationId; "gone" for a call the spec no longer has; a
// renamed path with the same operationId is followed). version marks what
// is new ("new in 1.6.0"). The unplaced section of an earlier version is
// dissolved: its reads are placed anew, its values go into params.
func Merge(s *spec.Spec, t *Tree, proposal *Tree, version string) []Change {
	m := &merger{s: s, t: t, known: map[string]bool{}, at: map[*Node]*Node{}, version: version,
		proposal: proposal, byName: map[string]*Node{}, legacy: map[string]any{}, kept: map[*Node]bool{}}
	proposal.Walk(func(n *Node) {
		if !n.IsRequire {
			m.byName[n.Name] = n
		}
	})
	m.dissolve()
	m.refresh()
	t.Walk(func(n *Node) {
		m.mark(n.Call)
		for _, w := range n.Write {
			if !w.None {
				m.mark(w.Call)
			}
		}
	})
	for _, c := range t.Ignore {
		m.mark(c)
	}
	m.proposed(proposal.Roots, nil)
	m.moveWrites()
	m.bindParams()
	m.bodies()
	m.ignore()
	m.params()
	return m.changes
}

type merger struct {
	s        *spec.Spec
	t        *Tree
	proposal *Tree
	known    map[string]bool // operationIds the file names
	at       map[*Node]*Node // proposal node → node of the file
	byName   map[string]*Node
	// kept are nodes whose values are values of the file already
	kept  map[*Node]bool
	model *model.Model
	// legacy are values of the unplaced section: parameter → value
	legacy  map[string]any
	version string
	changes []Change
}

// dissolve removes the unplaced section of an earlier version; the values
// written there go into params.
func (m *merger) dissolve() {
	root := m.t.doc.Root
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "unplaced" {
			continue
		}
		for _, n := range m.t.legacy {
			for _, vals := range []map[string]any{n.Path, n.Query} {
				for k, v := range vals {
					if _, isRef := ParseRef(v, m.t.names()); isRef || v == nil {
						continue
					}
					if _, isParam := ParamRef(v); !isParam {
						m.legacy[k] = v
					}
				}
			}
		}
		root.Content = append(root.Content[:i], root.Content[i+2:]...)
		m.changed("unplaced", "removed: its %d reads are placed in the tree, values no node gives are in params", len(m.t.legacy))
		m.reload()
		return
	}
}

// rename turns the values of an added node or write into values of the
// file: "<node>.field" of a proposal node names the node of the file it
// maps to; a reference without such node becomes "$name" of params.
func (m *merger) rename(vals map[string]any, op string, root bool) {
	for k, v := range vals {
		if _, isParam := ParamRef(v); isParam {
			continue
		}
		ref, ok := ParseRef(v, func(name string) bool { return m.byName[name] != nil })
		if !ok {
			continue
		}
		if ref.Node == "" {
			if root {
				vals[k] = m.param(k, op)
			}
			continue
		}
		if e := m.at[m.byName[ref.Node]]; e != nil {
			vals[k] = e.Name + "." + ref.Field
			continue
		}
		vals[k] = m.param(k, op)
	}
}

// param is "$name" for a value no node of the file gives; params gets the
// entry when the merge ends.
func (m *merger) param(name, op string) string {
	if m.proposal.Params == nil {
		m.proposal.Params = map[string]any{}
	}
	if _, ok := m.proposal.Params[name]; !ok {
		m.proposal.Params[name] = nil
		m.proposal.ParamNames = append(m.proposal.ParamNames, name)
		if m.proposal.ParamNotes == nil {
			m.proposal.ParamNotes = map[string]string{}
		}
		m.proposal.ParamNotes[name] = op
	}
	return "$" + name
}

// params adds to the params section every "$name" the file uses and lacks,
// with the value of the old unplaced section if it had one.
func (m *merger) params() {
	var missing []string
	m.t.Walk(func(n *Node) {
		vals := []any{anyMap(n.Path), anyMap(n.Query), anyMap(n.Header), n.Body}
		for _, w := range n.Write {
			vals = append(vals, anyMap(w.Path), anyMap(w.Query), anyMap(w.Header), w.Body)
		}
		for _, v := range vals {
			walkStrings(v, func(s string) {
				if name, ok := ParamRef(s); ok {
					if _, has := m.t.Params[name]; !has && !slices.Contains(missing, name) {
						missing = append(missing, name)
					}
				}
			})
		}
	})
	if len(missing) == 0 {
		return
	}
	root := m.t.doc.Root
	section := valueOf(root, "params")
	if section == nil || section.Kind != yaml.MappingNode {
		old := section
		section = &yaml.Node{Kind: yaml.MappingNode}
		if old != nil {
			// "params:" without entries
			for i := 0; i+1 < len(root.Content); i += 2 {
				if root.Content[i+1] == old {
					root.Content[i+1] = section
				}
			}
		} else {
			// at the top, below the head of the file
			key := paramsKey()
			if len(root.Content) > 0 && root.Content[0].HeadComment != "" {
				key.HeadComment = root.Content[0].HeadComment + "\n\n" + key.HeadComment
				root.Content[0].HeadComment = ""
			}
			root.Content = append([]*yaml.Node{key, section}, root.Content...)
		}
	}
	for _, name := range missing {
		v := m.legacy[name]
		section.Content = append(section.Content, scalar(name), paramValueYAML(v, m.proposal.ParamNotes[name]))
		if v != nil {
			m.changed("params."+name, "new, %v from the old unplaced section", v)
		} else {
			m.changed("params."+name, "new: no node gives it; set a value")
		}
	}
	m.reload()
}

// bodies adds "body: from <read>" to the creates, updates and patches of
// the file that have no body, as the proposal has it.
func (m *merger) bodies() {
	for p, e := range m.at {
		if e == nil {
			continue
		}
		for _, kind := range p.Kinds() {
			if base := KindBase(kind); base != WriteCreate && base != WriteUpdate && base != WritePatch {
				continue
			}
			pw, ew := p.Write[kind], e.Write[kind]
			if pw == nil || ew == nil || pw.From == "" || ew.None || ew.Gone || ew.HasBody || ew.From != "" {
				continue
			}
			pop, eop := lookupOp(m.s, pw.Call), lookupOp(m.s, ew.Call)
			if pop == nil || eop == nil || pop.ID != eop.ID {
				continue
			}
			from := e.Name
			if src := m.at[m.byName[pw.From]]; src != nil {
				from = src.Name
			}
			v := m.writeMapping(e, kind)
			if v == nil {
				continue
			}
			v.Content = append(v.Content, scalar("body"), scalar("from "+from))
			m.changed(e.Name+".write."+kind, "body: from %s", from)
		}
	}
	m.reload()
}

// writeMapping returns the value of a write of the file as a mapping,
// turning the short form into {call: …}.
func (m *merger) writeMapping(e *Node, kind string) *yaml.Node {
	block := valueOf(m.mapping(e), "write")
	if block == nil {
		return nil
	}
	for i := 0; i+1 < len(block.Content); i += 2 {
		if block.Content[i].Value != kind {
			continue
		}
		v := block.Content[i+1]
		if v.Kind == yaml.MappingNode {
			return v
		}
		call, params, _ := shortForm(v.Value)
		mv := &yaml.Node{Kind: yaml.MappingNode}
		c := scalar(call)
		c.LineComment = v.LineComment
		mv.Content = append(mv.Content, scalar("call"), c)
		if len(params) > 0 {
			mv.Content = append(mv.Content, scalar("path"), valueYAML(params, true))
		}
		block.Content[i+1] = mv
		return mv
	}
	return nil
}

// ignore adds the operations the proposal ignores and the file names
// nowhere, with the reason.
func (m *merger) ignore() {
	var add []*yaml.Node
	for _, c := range m.proposal.Ignore {
		if m.knows(c) {
			continue
		}
		n := scalar(c)
		n.LineComment = comment(m.proposal.IgnoreNotes[c])
		add = append(add, n)
		m.mark(c)
		m.changed("ignore", "new %s: no node reads the records it writes", c)
	}
	if len(add) == 0 {
		return
	}
	root := m.t.doc.Root
	l := valueOf(root, "ignore")
	if l == nil || l.Kind != yaml.SequenceNode {
		l = &yaml.Node{Kind: yaml.SequenceNode}
		root.Content = append(root.Content, scalar("ignore"), l)
	}
	l.Content = append(l.Content, add...)
	m.reload()
}

func (m *merger) mark(call string) {
	if op := lookupOp(m.s, call); op != nil {
		m.known[op.ID] = true
	}
}

func (m *merger) knows(call string) bool {
	op := lookupOp(m.s, call)
	return op == nil || m.known[op.ID]
}

func (m *merger) changed(where, format string, args ...any) {
	m.changes = append(m.changes, Change{Where: where, What: fmt.Sprintf(format, args...)})
}

// find is the node of the file with the call of a proposal node: below the
// node its parent maps to, else anywhere in the tree.
func (m *merger) find(p *Node, under *Node) *Node {
	op := lookupOp(m.s, p.Call)
	if op == nil {
		return nil
	}
	same := func(n *Node) bool {
		o := lookupOp(m.s, n.Call)
		return o != nil && o.ID == op.ID && !n.IsRequire
	}
	if under != nil {
		for _, c := range under.Each {
			if same(c) {
				return c
			}
		}
	}
	var found *Node
	m.t.Walk(func(n *Node) {
		if found == nil && same(n) && n.Parent == nil == (p.Parent == nil) {
			found = n
		}
	})
	if found == nil {
		m.t.Walk(func(n *Node) {
			if found == nil && same(n) {
				found = n
			}
		})
	}
	return found
}

func (m *merger) proposed(list []*Node, parent *Node) {
	for _, p := range list {
		var into *Node
		if parent != nil {
			into = m.at[parent]
		}
		if m.knows(p.Call) {
			if e := m.find(p, into); e != nil {
				m.at[p] = e
				m.addWrites(p, e)
			}
			m.proposed(p.Each, p)
			continue
		}
		switch {
		case into != nil:
			m.addChild(into, p)
		case parent == nil:
			m.addRoot(p)
		default:
			m.relocate(p)
		}
	}
}

// bindParams replaces a "$name" without value by a reference to a node of
// the file that gives it now (a tag added later: $personId → persons.id):
// a node above, or fetched before; for a write any node. An entry of
// params that nothing uses any more is removed. A $name with a value
// stays: the value was chosen.
func (m *merger) bindParams() {
	pr := m.fileProposer()
	order := pr.order()
	pos := map[*Node]int{}
	for i, n := range order {
		pos[n] = i
	}
	bound := map[string]bool{}
	// best is the node that gives a value of op for a value at node n:
	// above n, or (write) anywhere, or fetched before n
	best := func(op *spec.Operation, name string, n *Node, write bool) (*Node, string) {
		var at *Node
		field, score := "", 0
		for _, x := range order {
			ok := write || n.Ancestor(x.Name) != nil && x != n || pos[x] < pos[n] && !inside(x, n)
			if !ok || x.Off {
				continue
			}
			if f, s := pr.provides(op, name, pr.res[x]); s > score {
				at, field, score = x, f, s
			}
		}
		return at, field
	}
	ref := func(at, from *Node, field string, write bool) string {
		if write && at == from || !write && at == from.Parent {
			return "." + field
		}
		return at.Name + "." + field
	}
	for _, n := range order {
		if n.Gone {
			continue
		}
		if op := lookupOp(m.s, n.Call); op != nil {
			for _, vals := range []map[string]any{n.Path, n.Query} {
				for k, v := range vals {
					name, ok := ParamRef(v)
					if !ok || m.t.Params[name] != nil {
						continue
					}
					if at, field := best(op, k, n, false); at != nil {
						r := ref(at, n, field, false)
						m.replaceParam(n.value, k, name, r)
						m.changed(n.Name, "%s = %s: given by %s now", k, r, at.Name)
						bound[name] = true
					}
				}
			}
		}
		for _, kind := range n.Kinds() {
			w := n.Write[kind]
			op := lookupOp(m.s, w.Call)
			if op == nil || w.Gone || w.None {
				continue
			}
			for _, vals := range []map[string]any{w.Path, w.Query} {
				for k, v := range vals {
					name, ok := ParamRef(v)
					if !ok || m.t.Params[name] != nil {
						continue
					}
					if at, field := best(op, k, n, true); at != nil {
						r := ref(at, n, field, true)
						m.replaceParam(w.value, k, name, r)
						m.changed(n.Name+".write."+kind, "%s = %s: given by %s now", k, r, at.Name)
						bound[name] = true
					}
				}
			}
		}
	}
	if len(bound) == 0 {
		return
	}
	m.reload()
	// entries nothing uses any more
	used := map[string]bool{}
	m.t.Walk(func(n *Node) {
		vals := []any{anyMap(n.Path), anyMap(n.Query), anyMap(n.Header), n.Body}
		for _, w := range n.Write {
			vals = append(vals, anyMap(w.Path), anyMap(w.Query), anyMap(w.Header), w.Body)
		}
		for _, v := range vals {
			walkStrings(v, func(s string) {
				if name, ok := ParamRef(s); ok {
					used[name] = true
				}
			})
		}
	})
	section := valueOf(m.t.doc.Root, "params")
	if section == nil || section.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(section.Content); {
		name := section.Content[i].Value
		if bound[name] && !used[name] {
			section.Content = append(section.Content[:i], section.Content[i+2:]...)
			m.changed("params."+name, "removed: a node gives it now")
			continue
		}
		i += 2
	}
	if len(section.Content) == 0 {
		root := m.t.doc.Root
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == "params" {
				// keep the head of the file on the next key
				if i+2 < len(root.Content) && root.Content[i].HeadComment != "" {
					head := strings.TrimSuffix(root.Content[i].HeadComment, paramsHeadComment())
					root.Content[i+2].HeadComment = strings.TrimSpace(head)
				}
				root.Content = append(root.Content[:i], root.Content[i+2:]...)
				break
			}
		}
	}
	m.reload()
}

func paramsHeadComment() string { return paramsKey().HeadComment }

// inside reports whether x lies below n (n's subtree is fetched after n).
func inside(x, n *Node) bool {
	for p := x.Parent; p != nil; p = p.Parent {
		if p == n {
			return true
		}
	}
	return false
}

// replaceParam sets the value of parameter key from "$name" to a
// reference, in the short form ("k=$name") or a path, query or header
// mapping of a node or write value.
func (m *merger) replaceParam(v *yaml.Node, key, name, ref string) {
	if v == nil {
		return
	}
	switch v.Kind {
	case yaml.ScalarNode:
		v.Value = strings.Replace(v.Value, key+"=$"+name, key+"="+ref, 1)
	case yaml.MappingNode:
		if c := valueOf(v, "call"); c != nil {
			c.Value = strings.Replace(c.Value, key+"=$"+name, key+"="+ref, 1)
		}
		for _, sec := range []string{"path", "query", "header"} {
			mp := valueOf(v, sec)
			if mp == nil || mp.Kind != yaml.MappingNode {
				continue
			}
			for i := 0; i+1 < len(mp.Content); i += 2 {
				if mp.Content[i].Value == key && mp.Content[i+1].Value == "$"+name {
					mp.Content[i+1].Value = ref
				}
			}
		}
	}
}

// fileProposer finds providers among the nodes of the file.
func (m *merger) fileProposer() *proposer {
	if m.model == nil {
		m.model = model.Detect(m.s, nil)
	}
	pr := &proposer{s: m.s, m: m.model, t: m.t, res: map[*Node]*model.Resource{}}
	m.t.Walk(func(n *Node) {
		if op := lookupOp(m.s, n.Call); op != nil && !n.IsRequire && n.Enabled() {
			pr.res[n] = pr.resourceOf(op)
		}
	})
	return pr
}

// moveWrites gives the writes of a proposal node the file does not have
// (an ignored list) to the first node of the file of the same resource.
func (m *merger) moveWrites() {
	var pr *proposer
	m.proposal.Walk(func(p *Node) {
		if m.at[p] != nil || len(p.Write) == 0 {
			return
		}
		fresh := false
		for _, w := range p.Write {
			fresh = fresh || !w.None && !m.knows(w.Call)
		}
		op := lookupOp(m.s, p.Call)
		if !fresh || op == nil {
			return
		}
		if pr == nil {
			pr = m.fileProposer()
		}
		res := pr.resourceOf(op)
		if res == nil {
			return
		}
		for _, n := range pr.order() {
			if pr.res[n] == res {
				m.addWrites(p, n)
				pr = nil
				return
			}
		}
	})
}

// relocate adds a read whose parent in the proposal the file does not have
// (it is ignored or gone): below the nodes of the file that give its
// values, else at the top with the values from params.
func (m *merger) relocate(p *Node) {
	op := lookupOp(m.s, p.Call)
	if op == nil {
		m.addRoot(p)
		return
	}
	pr := m.fileProposer()
	provs, _ := pr.providers(op)
	var parent provider
	for _, x := range provs {
		if parent.node == nil || x.at > parent.at {
			parent = x
		}
	}
	if parent.node == nil {
		m.addRoot(p)
		return
	}
	q := *p
	q.Path, q.Query = nil, nil
	for _, nd := range pr.needs(op) {
		x, ok := provs[nd.name]
		switch {
		case !ok:
			q.set(nd, m.param(nd.name, p.Comment))
		case x.node == parent.node:
			q.set(nd, "."+x.field)
		default:
			q.set(nd, x.node.Name+"."+x.field)
		}
	}
	m.kept[&q] = true
	m.addChild(parent.node, &q)
	m.at[p] = m.at[&q]
	m.markTree(p)
}

// strip removes from a proposal subtree what the file names already and
// turns its values into values of the file (rename); root marks a node
// added at the top.
func (m *merger) strip(p *Node, root bool) *Node {
	c := *p
	c.Each = nil
	c.Path, c.Query = copyMap(p.Path), copyMap(p.Query)
	if !m.kept[p] {
		m.rename(c.Path, p.Comment, root)
		m.rename(c.Query, p.Comment, root)
	}
	for _, e := range p.Each {
		if !m.knows(e.Call) {
			c.Each = append(c.Each, m.strip(e, false))
		}
	}
	if len(p.Write) > 0 {
		c.Write = map[string]*Write{}
		for k, w := range p.Write {
			if w.None || !m.knows(w.Call) {
				x := *w
				x.Path, x.Query = copyMap(w.Path), copyMap(w.Query)
				m.rename(x.Path, w.Comment, false)
				m.rename(x.Query, w.Comment, false)
				c.Write[k] = &x
			}
		}
	}
	return &c
}

func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// markTree marks the operations of an added subtree as known.
func (m *merger) markTree(n *Node) {
	m.mark(n.Call)
	for _, w := range n.Write {
		if !w.None {
			m.mark(w.Call)
		}
	}
	for _, c := range n.Each {
		m.markTree(c)
	}
}

func (m *merger) newNote(n *Node) {
	if m.version != "" {
		n.Comment = strings.TrimSpace(n.Comment + " · new in " + m.version)
		n.Comment = strings.TrimPrefix(n.Comment, "· ")
	}
}

func (m *merger) addRoot(p *Node) {
	c := m.strip(p, true)
	m.newNote(c)
	c.Name = m.freeName(c.Name)
	tree := m.section("tree")
	if len(c.Path) == 0 && len(c.Query) == 0 {
		// a root that needs no value comes first: the nodes of the file may
		// read it ("persons.id" for a $personId it gives now)
		tree.Content = append([]*yaml.Node{scalar(c.Name), nodeYAML(c)}, tree.Content...)
	} else {
		tree.Content = append(tree.Content, scalar(c.Name), nodeYAML(c))
	}
	m.markTree(p)
	m.changed(c.Name, "new root %s", c.Call)
	m.reload()
	m.at[p] = m.t.Node(c.Name)
}

func (m *merger) addChild(into *Node, p *Node) {
	c := m.strip(p, false)
	m.newNote(c)
	c.Name = m.freeName(c.Name)
	value := m.mapping(into)
	each := valueOf(value, "each")
	if each == nil {
		each = &yaml.Node{Kind: yaml.MappingNode}
		value.Content = append(value.Content, scalar("each"), each)
	}
	each.Content = append(each.Content, scalar(c.Name), nodeYAML(c))
	m.markTree(p)
	m.changed(into.Name+"/"+c.Name, "new node %s", c.Call)
	m.reload()
	m.at[p] = m.t.Node(c.Name)
}

// addWrites adds the write kinds of a proposal node the file's node lacks.
func (m *merger) addWrites(p, e *Node) {
	var add []*Write
	for _, kind := range p.Kinds() {
		w := p.Write[kind]
		if w == nil || w.None || m.knows(w.Call) || e.Write[kind] != nil {
			continue
		}
		add = append(add, w)
	}
	if len(add) == 0 {
		return
	}
	value := m.mapping(e)
	block := valueOf(value, "write")
	if block == nil {
		block = &yaml.Node{Kind: yaml.MappingNode}
		// the write block goes before each, where the proposal has it
		at := len(value.Content)
		for i := 0; i+1 < len(value.Content); i += 2 {
			if value.Content[i].Value == "each" {
				at = i
			}
		}
		value.Content = append(value.Content[:at], append([]*yaml.Node{scalar("write"), block}, value.Content[at:]...)...)
	}
	for _, w := range add {
		c := *w
		c.Path, c.Query = copyMap(w.Path), copyMap(w.Query)
		m.rename(c.Path, w.Comment, false)
		m.rename(c.Query, w.Comment, false)
		if src := m.at[m.byName[c.From]]; src != nil {
			c.From = src.Name
		} else if c.From != "" {
			c.From = e.Name
		}
		if m.version != "" {
			c.Comment = strings.TrimPrefix(strings.TrimSpace(c.Comment+" · new in "+m.version), "· ")
		}
		block.Content = append(block.Content, scalar(w.Kind), writeYAML(&c))
		m.mark(w.Call)
		m.changed(e.Name+".write."+w.Kind, "new %s", w.Call)
	}
	m.reload()
}

// mapping returns the value of a node as a mapping, turning the short form
// into {call: …}.
func (m *merger) mapping(n *Node) *yaml.Node {
	if n.value.Kind == yaml.MappingNode {
		return n.value
	}
	call, params, _ := shortForm(n.value.Value)
	v := &yaml.Node{Kind: yaml.MappingNode}
	c := scalar(call)
	c.LineComment = n.value.LineComment
	v.Content = append(v.Content, scalar("call"), c)
	if len(params) > 0 {
		v.Content = append(v.Content, scalar("path"), valueYAML(params, true))
	}
	for i := 0; i+1 < len(n.owner.Content); i += 2 {
		if n.owner.Content[i+1] == n.value {
			n.owner.Content[i+1] = v
		}
	}
	n.value = v
	return v
}

// section returns the mapping under a top-level key, adding it.
func (m *merger) section(key string) *yaml.Node {
	root := m.t.doc.Root
	if v := valueOf(root, key); v != nil {
		if v.Kind != yaml.MappingNode {
			v.Kind, v.Tag, v.Value = yaml.MappingNode, "", ""
		}
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode}
	root.Content = append(root.Content, scalar(key), v)
	return v
}

func valueOf(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// freeName makes a name unique in the file.
func (m *merger) freeName(name string) string {
	used := map[string]bool{}
	m.t.Walk(func(n *Node) { used[n.Name] = true })
	if !used[name] {
		return name
	}
	for i := 2; ; i++ {
		if c := fmt.Sprintf("%s%d", name, i); !used[c] {
			return c
		}
	}
}

// reload parses the edited document again, so nodes point at its YAML.
func (m *merger) reload() {
	t, _ := fromDoc(m.t.doc)
	if t == nil {
		return
	}
	old := map[*Node]string{}
	for p, n := range m.at {
		if n != nil {
			old[p] = n.Name
		}
	}
	*m.t = *t
	for p, name := range old {
		m.at[p] = m.t.Node(name)
	}
}

// generated matches a comment the generator wrote after a call:
// "# getDock", "# getDock · new in 1.6.0", "# gone: …".
var generated = regexp.MustCompile(`^#\s*(gone: .*|[A-Za-z_][\w.-]*(\s+·\s+.*)?)$`)

// refresh writes the operationId after every call of the file, follows a
// renamed path whose operationId stayed, and marks calls the spec no
// longer has. Comments someone wrote stay.
func (m *merger) refresh() {
	var calls []*yaml.Node
	var visit func(v *yaml.Node)
	visitNodes := func(mp *yaml.Node) {
		if mp == nil || mp.Kind != yaml.MappingNode {
			return
		}
		for i := 1; i < len(mp.Content); i += 2 {
			visit(mp.Content[i])
		}
	}
	visit = func(v *yaml.Node) {
		switch v.Kind {
		case yaml.ScalarNode:
			calls = append(calls, v)
		case yaml.MappingNode:
			if c := valueOf(v, "call"); c != nil {
				calls = append(calls, c)
			}
			visitNodes(valueOf(v, "each"))
			visitNodes(valueOf(v, "require"))
			if w := valueOf(v, "write"); w != nil && w.Kind == yaml.MappingNode {
				for i := 1; i < len(w.Content); i += 2 {
					wv := w.Content[i]
					switch {
					case wv.Kind == yaml.ScalarNode && wv.Value != "none":
						calls = append(calls, wv)
					case wv.Kind == yaml.MappingNode:
						if c := valueOf(wv, "call"); c != nil {
							calls = append(calls, c)
						}
					}
				}
			}
		}
	}
	visitNodes(valueOf(m.t.doc.Root, "tree"))
	for _, c := range calls {
		call, _, err := shortForm(c.Value)
		if err != nil {
			continue
		}
		own := c.LineComment != "" && !generated.MatchString(c.LineComment)
		if own {
			continue
		}
		extra := ""
		if _, rest, ok := strings.Cut(c.LineComment, " · "); ok {
			extra = rest
		}
		if op := lookupOp(m.s, call); op != nil {
			c.LineComment = comment(op.ID, extra)
			continue
		}
		// the path changed, the operationId of the comment is still there
		old := strings.TrimSpace(strings.TrimPrefix(c.LineComment, "#"))
		old, _, _ = strings.Cut(old, " ")
		if op := m.s.Op(old); op != nil && op.HasOperationID && Method(call) != "" {
			m.changed(old, "path changed: %s → %s", call, callOf(op))
			c.Value = strings.Replace(c.Value, call, callOf(op), 1)
			c.LineComment = comment(op.ID, extra)
			continue
		}
		if !strings.HasPrefix(c.LineComment, "# gone") {
			m.changed(call, "not in the spec any more")
		}
		c.LineComment = "# gone: not in the spec"
		if m.version != "" {
			c.LineComment += " " + m.version
		}
	}
	m.reload()
}
