package tree

import (
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
)

// fileHead heads a new tree file.
const fileHead = `The records "apitest-gen tree" fetches and how they hang together.
Written once by "apitest-gen tree"; yours from then on. A later run keeps
what you wrote and only adds new endpoints at their place.

A node is a request ("GET /path p=.field") or a mapping: call, path, query,
header, body (with read: true for a POST that only reads), items, page,
pick (a number, all, {first: n} or a list of keys), candidates, where,
require, key, save, write, each, off. ".field" reads the record the node
runs under (in write: the record itself), "<node>.field" a node above it.
"write" says how the integration test rebuilds a record in an empty
instance: create (none: the instance holds it), update, patch, delete.`

// Encode writes a new tree file.
func (t *Tree) Encode() ([]byte, error) {
	doc := yamldoc.New(fileHead)
	root := doc.Root
	add := func(k string, v *yaml.Node) { root.Content = append(root.Content, scalar(k), v) }
	add("seed", scalar(strconv.FormatUint(t.Seed, 10)))
	add("budget", scalar(strconv.Itoa(t.Budget)))
	add("tree", nodesYAML(t.Roots))
	if len(t.Unplaced) > 0 {
		u := nodesYAML(t.Unplaced)
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "unplaced",
			HeadComment: "Reads the generator could not hang under a node; switched off. Move them into the tree."}, u)
	}
	if len(t.Ignore) > 0 {
		add("ignore", stringsYAML(t.Ignore))
	}
	return doc.Bytes()
}

func scalar(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: s}
}

func stringsYAML(l []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode}
	for _, s := range l {
		n.Content = append(n.Content, scalar(s))
	}
	return n
}

func nodesYAML(list []*Node) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, n := range list {
		m.Content = append(m.Content, scalar(n.Name), nodeYAML(n))
	}
	return m
}

func valueYAML(v any, flow bool) *yaml.Node {
	n, err := yamldoc.Node(v)
	if err != nil {
		return scalar(text(v))
	}
	if flow {
		setFlow(n)
	}
	return n
}

// setFlow writes short collections on one line.
func setFlow(n *yaml.Node) {
	if n.Kind != yaml.MappingNode && n.Kind != yaml.SequenceNode {
		return
	}
	width := 0
	for _, c := range n.Content {
		if c.Kind != yaml.ScalarNode {
			return
		}
		width += len(c.Value) + 3
	}
	if width <= 80 {
		n.Style = yaml.FlowStyle
	}
}

// shortText is the short form of a call with path values: "GET /x/{a} a=.b".
func shortText(call string, path map[string]any) (string, bool) {
	parts := []string{call}
	keys := sortedKeys(path)
	_, p, _ := strings.Cut(call, " ")
	slices.SortStableFunc(keys, func(a, b string) int {
		return strings.Index(p, "{"+a+"}") - strings.Index(p, "{"+b+"}")
	})
	for _, k := range keys {
		s, ok := path[k].(string)
		if !ok || s == "" || strings.ContainsAny(s, " =#") {
			return "", false
		}
		parts = append(parts, k+"="+s)
	}
	return strings.Join(parts, " "), true
}

func comment(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 {
		return ""
	}
	return "# " + strings.Join(keep, " · ")
}

// short reports whether a node has nothing but a call with path values.
func (n *Node) short() bool {
	return len(n.Query) == 0 && len(n.Header) == 0 && !n.HasBody && !n.Read && !n.HasItems && n.Page == nil &&
		n.Pick.N == 0 && !n.Pick.All && len(n.Pick.Keys) == 0 && n.Candidates == 0 && len(n.Where) == 0 &&
		len(n.WhereAny) == 0 && len(n.Require) == 0 && len(n.Key) == 0 && len(n.Save) == 0 && len(n.Write) == 0 &&
		len(n.Each) == 0 && !n.Off
}

func nodeYAML(n *Node) *yaml.Node {
	if n.short() {
		if s, ok := shortText(n.Call, n.Path); ok {
			v := scalar(s)
			v.LineComment = comment(n.Comment, n.Note)
			return v
		}
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k string, v *yaml.Node) { m.Content = append(m.Content, scalar(k), v) }
	call := scalar(n.Call)
	call.LineComment = comment(n.Comment)
	if !n.Off {
		call.LineComment = comment(n.Comment, n.Note)
	}
	add("call", call)
	if len(n.Path) > 0 {
		add("path", valueYAML(n.Path, true))
	}
	if len(n.Query) > 0 {
		add("query", valueYAML(n.Query, true))
	}
	if len(n.Header) > 0 {
		add("header", valueYAML(n.Header, true))
	}
	if n.Read {
		add("read", scalar("true"))
	}
	if n.HasBody {
		add("body", valueYAML(n.Body, true))
	}
	if n.HasItems {
		add("items", scalar(n.Items))
	}
	if n.Page != nil {
		add("page", pageYAML(n.Page))
	}
	switch {
	case n.Pick.All:
		add("pick", scalar("all"))
	case len(n.Pick.Keys) > 0:
		add("pick", valueYAML(n.Pick.Keys, true))
	case n.Pick.First:
		add("pick", valueYAML(map[string]any{"first": n.Pick.N}, true))
	case n.Pick.N > 0:
		add("pick", scalar(strconv.Itoa(n.Pick.N)))
	}
	if n.Candidates > 0 {
		add("candidates", scalar(strconv.Itoa(n.Candidates)))
	}
	if len(n.Where) > 0 || len(n.WhereAny) > 0 {
		w := &yaml.Node{Kind: yaml.MappingNode}
		for _, c := range n.Where {
			w.Content = append(w.Content, scalar(c.Field), valueYAML(c.Test, true))
		}
		if len(n.WhereAny) > 0 {
			a := &yaml.Node{Kind: yaml.MappingNode}
			for _, c := range n.WhereAny {
				a.Content = append(a.Content, scalar(c.Field), valueYAML(c.Test, true))
			}
			w.Content = append(w.Content, scalar("any"), a)
		}
		add("where", w)
	}
	if len(n.Require) > 0 {
		add("require", nodesYAML(n.Require))
	}
	if len(n.Key) > 0 {
		add("key", stringsYAML(n.Key))
		setFlow(m.Content[len(m.Content)-1])
	}
	if len(n.Save) > 0 {
		add("save", valueYAML(n.Save, true))
	}
	if len(n.Write) > 0 {
		add("write", writesYAML(n.Write))
	}
	if len(n.Each) > 0 {
		add("each", nodesYAML(n.Each))
	}
	if n.Off {
		off := scalar("true")
		off.LineComment = comment(n.Note)
		add("off", off)
	}
	return m
}

func pageYAML(p *Page) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	add := func(k string, v *yaml.Node) { m.Content = append(m.Content, scalar(k), v) }
	add("param", scalar(p.Param))
	if p.In != "" && p.In != "query" {
		add("in", scalar(p.In))
	}
	if p.Start != nil {
		add("start", scalar(strconv.Itoa(*p.Start)))
	}
	if p.Step > 1 {
		add("step", scalar(strconv.Itoa(p.Step)))
	}
	if len(p.Size) > 0 {
		add("size", valueYAML(p.Size, true))
	}
	if p.Next != "" {
		add("next", scalar(p.Next))
	}
	add("max", scalar(strconv.Itoa(p.Max)))
	return m
}

func writesYAML(ws map[string]*Write) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, kind := range writeKinds {
		if w := ws[kind]; w != nil {
			m.Content = append(m.Content, scalar(kind), writeYAML(w))
		}
	}
	return m
}

func writeYAML(w *Write) *yaml.Node {
	if w.None {
		v := scalar("none")
		v.LineComment = comment(w.Note)
		return v
	}
	if !w.Off && len(w.Query) == 0 && len(w.Header) == 0 && !w.HasBody && w.From == "" && w.Change == nil && w.Expect == 0 {
		if s, ok := shortText(w.Call, w.Path); ok {
			v := scalar(s)
			v.LineComment = comment(w.Comment, w.Note)
			return v
		}
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k string, v *yaml.Node) { m.Content = append(m.Content, scalar(k), v) }
	call := scalar(w.Call)
	call.LineComment = comment(w.Comment)
	add("call", call)
	if len(w.Path) > 0 {
		add("path", valueYAML(w.Path, true))
	}
	if len(w.Query) > 0 {
		add("query", valueYAML(w.Query, true))
	}
	if len(w.Header) > 0 {
		add("header", valueYAML(w.Header, true))
	}
	switch {
	case w.From != "":
		add("body", scalar("from "+w.From))
	case w.HasBody:
		add("body", valueYAML(w.Body, true))
	}
	switch {
	case w.Change != nil && len(w.Change) == 0:
		add("change", scalar("auto"))
	case len(w.Change) > 0:
		add("change", valueYAML(w.Change, true))
	}
	if w.Expect != 0 {
		add("expect", scalar(strconv.Itoa(w.Expect)))
	}
	if w.Off {
		off := scalar("true")
		off.LineComment = comment(w.Note)
		add("off", off)
	}
	return m
}
