package tree

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// renamed is the value -fix gives the name an update changes by default:
// the name of the record with "-neu" after it.
const renamed = "{{.name}}-neu"

// changeDefaults are the changes -fix gives the updates (PUT, also by
// another key) that have neither change nor body: change: {name:
// '{{.name}}-neu'}, when the body of the update has a field name the test
// may change. That is a text without enum, pattern or format, not readOnly
// and not unique, no key of the record, and no request of the tree reads
// or writes the record by its name (the steps after the update would send
// the old one). "change: auto" in the file keeps the default of apitest-gen
// (one field changed), -fix leaves it.
func changeDefaults(s *spec.Spec, t *Tree) []Fix {
	var out []Fix
	t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone || n.value == nil {
			return
		}
		for _, kind := range n.Kinds() {
			w := n.Write[kind]
			if KindBase(kind) != WriteUpdate || w == nil || w.value == nil || w.Change != nil || w.HasBody || w.None || w.Off || w.Gone {
				continue
			}
			op := lookupOp(s, w.Call)
			if op == nil {
				continue
			}
			field, ok := renameField(op, n)
			if !ok || readsByName(s, t, n, field) {
				continue
			}
			out = append(out, Fix{
				Line:   w.Line,
				Change: fmt.Sprintf("%s.write.%s: add change: {%s: '%s'} (the default of -fix: the update renames the record)", n.Name, kind, field, renamed),
				apply:  setChange(n.Name, kind, field, renamed),
			})
		}
	})
	return out
}

// renameField is the field name of the body of an update, as the schema
// spells it, when the test may change it.
func renameField(op *spec.Operation, n *Node) (string, bool) {
	props, _ := properties(schemaOf(requestRef(op)))
	field, ok := propertyFoldName(props, "name")
	if !ok || props[field] == nil || props[field].Value == nil {
		return "", false
	}
	p := props[field].Value
	if p.Type == nil || !p.Type.Is("string") || len(p.Enum) > 0 || p.Pattern != "" || p.Format != "" || p.ReadOnly {
		return "", false
	}
	if isUniqueField(p, field, n) {
		return "", false
	}
	return field, true
}

// readsByName reports whether a request of the tree takes the name of a
// record of n: a write of n with a path parameter name or a value .name, a
// node below n that reads with .name, any node or write with
// <n>.name, or a key of n.
func readsByName(s *spec.Spec, t *Tree, n *Node, field string) bool {
	for _, k := range keyOf(n) {
		if strings.EqualFold(PointerField(k), field) {
			return true
		}
	}
	scope := t.names()
	byName := func(own bool, values ...any) bool {
		hit := false
		for _, v := range values {
			walkStrings(v, func(whole string) {
				for _, str := range refsOf(whole, scope) {
					ref, ok := ParseRef(str, scope)
					if !ok || !strings.EqualFold(ref.Field, field) {
						continue
					}
					if ref.Node == n.Name || own && ref.Node == "" {
						hit = true
					}
				}
			})
		}
		return hit
	}
	for _, kind := range n.Kinds() {
		w := n.Write[kind]
		if w == nil {
			continue
		}
		if op := lookupOp(s, w.Call); op != nil && slices.ContainsFunc(pathParams(op), func(p string) bool { return strings.EqualFold(p, field) }) {
			return true
		}
		if byName(true, w.Path, w.Query, w.Header) {
			return true
		}
	}
	for _, c := range n.Each {
		if byName(true, c.Path, c.Query, c.Header, c.Body) {
			return true
		}
	}
	for _, c := range n.Require {
		if byName(true, c.Path, c.Query, c.Header, c.Body) {
			return true
		}
	}
	found := false
	t.Walk(func(x *Node) {
		if found {
			return
		}
		vals := []any{x.Path, x.Query, x.Header, x.Body}
		for _, w := range x.Write {
			vals = append(vals, w.Path, w.Query, w.Header, w.Body)
		}
		found = byName(false, vals...)
	})
	return found
}

// setChange sets change: {field: value} at a write of a node; a write in
// the short form (update: PUT /docks/{dockId}) becomes a mapping with the
// request as call.
func setChange(node, kind, field, value string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(node)
		if n == nil || n.value == nil {
			return false
		}
		write := valueOf(n.value, "write")
		if write == nil || write.Kind != yaml.MappingNode {
			return false
		}
		for i := 0; i+1 < len(write.Content); i += 2 {
			if write.Content[i].Value != kind {
				continue
			}
			v := write.Content[i+1]
			change := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle, Content: []*yaml.Node{
				scalar(field), {Kind: yaml.ScalarNode, Value: value, Style: yaml.SingleQuotedStyle},
			}}
			switch v.Kind {
			case yaml.ScalarNode:
				call := &yaml.Node{Kind: yaml.ScalarNode, Value: v.Value, LineComment: v.LineComment}
				write.Content[i+1] = &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("call"), call, scalar("change"), change}}
			case yaml.MappingNode:
				if valueOf(v, "change") != nil {
					return false
				}
				v.Style = 0
				v.Content = append(v.Content, scalar("change"), change)
			default:
				return false
			}
			write.Style = 0
			return true
		}
		return false
	}
}
