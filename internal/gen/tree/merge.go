package tree

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Change is one thing a merge did to the file.
type Change struct {
	Where, What string
}

// Merge adds to the file t what the proposal has and the file not, and
// changes nothing else the file holds: an operation the file names nowhere
// (in a node, a require, a write block, unplaced or ignore) is added at
// the place the proposal gives it, below the node of the file with the
// same call as the proposal's parent; write kinds go into the write block
// of that node; the rest goes to unplaced. The comments after the calls
// are refreshed (operationId; "gone" for a call the spec no longer has; a
// renamed path with the same operationId is followed). version marks what
// is new ("new in 1.6.0").
func Merge(s *spec.Spec, t *Tree, proposal *Tree, version string) []Change {
	m := &merger{s: s, t: t, known: map[string]bool{}, at: map[*Node]*Node{}, version: version}
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
	for _, n := range proposal.Unplaced {
		if !m.knows(n.Call) {
			m.addUnplaced(n)
		}
	}
	return m.changes
}

type merger struct {
	s       *spec.Spec
	t       *Tree
	known   map[string]bool // operationIds the file names
	at      map[*Node]*Node // proposal node → node of the file
	version string
	changes []Change
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
		case parent == nil:
			m.addRoot(p)
		case into != nil:
			m.addChild(into, p)
		default:
			m.addUnplaced(p)
			continue
		}
	}
}

// strip removes from a proposal subtree what the file names already.
func (m *merger) strip(p *Node) *Node {
	c := *p
	c.Each = nil
	for _, e := range p.Each {
		if !m.knows(e.Call) {
			c.Each = append(c.Each, m.strip(e))
		}
	}
	if len(p.Write) > 0 {
		c.Write = map[string]*Write{}
		for k, w := range p.Write {
			if w.None || !m.knows(w.Call) {
				c.Write[k] = w
			}
		}
	}
	return &c
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
	c := m.strip(p)
	m.newNote(c)
	c.Name = m.freeName(c.Name)
	tree := m.section("tree")
	tree.Content = append(tree.Content, scalar(c.Name), nodeYAML(c))
	m.markTree(p)
	m.changed(c.Name, "new root %s", c.Call)
	m.reload()
	m.at[p] = m.t.Node(c.Name)
}

func (m *merger) addChild(into *Node, p *Node) {
	c := m.strip(p)
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

func (m *merger) addUnplaced(p *Node) {
	c := m.strip(p)
	c.Each = nil
	c.Off = true
	if c.Note == "" {
		c.Note = "no node of the file gives its parameters"
	}
	m.newNote(c)
	c.Name = m.freeName(c.Name)
	u := m.section("unplaced")
	u.Content = append(u.Content, scalar(c.Name), nodeYAML(c))
	m.markTree(p)
	m.changed("unplaced/"+c.Name, "new, switched off: %s", c.Call)
	m.reload()
}

// addWrites adds the write kinds of a proposal node the file's node lacks.
func (m *merger) addWrites(p, e *Node) {
	var add []*Write
	for _, kind := range writeKinds {
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
	k := scalar(key)
	if key == "unplaced" {
		k.HeadComment = "Reads the generator could not hang under a node; switched off. Move them into the tree."
	}
	root.Content = append(root.Content, k, v)
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
		old[p] = n.Name
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
	visitNodes(valueOf(m.t.doc.Root, "unplaced"))
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
