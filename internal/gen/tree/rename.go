package tree

import (
	"fmt"
	"net/http"
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

// upsertDefaults turns the update of a node into its create when the
// update puts the record at the very path the node reads (GET and PUT
// /planets/{code}/settings/{season}) and nothing else creates records
// of it: the API sets such a record by the PUT, an empty instance has none
// before, so the test must put it first, not change one of the seed.
func upsertDefaults(s *spec.Spec, t *Tree) []Fix {
	m := modelOf(s)
	var out []Fix
	t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone || n.value == nil || !n.Enabled() {
			return
		}
		u := n.Write[WriteUpdate]
		if u == nil || u.value == nil || u.Off || u.None || u.Gone || Method(u.Call) != http.MethodPut {
			return
		}
		if c := n.Write[WriteCreate]; c != nil && !c.None || createOwner(s, m, t, n) != nil {
			return
		}
		_, readPath, _ := strings.Cut(n.Call, " ")
		_, putPath, _ := strings.Cut(u.Call, " ")
		if readPath == "" || readPath != putPath {
			return
		}
		out = append(out, Fix{
			Line:   u.Line,
			Change: fmt.Sprintf("%s.write.update → create (the default of -fix: %s puts the record at the path %s reads, nothing else creates it)", n.Name, u.Call, n.Name),
			apply:  updateToCreate(n.Name),
		})
	})
	return out
}

// updateToCreate renames the update of a node to its create; a create:
// none goes, and onSeed (no key of a create).
func updateToCreate(name string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.value == nil {
			return false
		}
		write := valueOf(nodeMapping(n), "write")
		if write == nil || write.Kind != yaml.MappingNode {
			return false
		}
		deleteKey(write, WriteCreate)
		for i := 0; i+1 < len(write.Content); i += 2 {
			if write.Content[i].Value == WriteUpdate {
				write.Content[i].Value = WriteCreate
				if v := write.Content[i+1]; v.Kind == yaml.MappingNode {
					deleteKey(v, "onSeed")
					deleteKey(v, "change")
				}
				return true
			}
		}
		return false
	}
}

// seedDefaults gives the updates, patches and actions of a node whose
// records the test never creates (create: none, and no node of the
// resource creates them: the instance holds them, its seed) onSeed: true.
// Without it they never run, the operations stay untested.
func seedDefaults(s *spec.Spec, t *Tree) []Fix {
	m := modelOf(s)
	var out []Fix
	t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone || n.value == nil || !n.Enabled() {
			return
		}
		if c := n.Write[WriteCreate]; c == nil || !c.None || createOwner(s, m, t, n) != nil {
			return
		}
		for _, kind := range n.Kinds() {
			w := n.Write[kind]
			base := KindBase(kind)
			if base == WriteCreate || base == WriteDelete || w == nil || w.value == nil || w.OnSeed || w.None || w.Off || w.Gone {
				continue
			}
			out = append(out, Fix{
				Line:   w.Line,
				Change: fmt.Sprintf("%s.write.%s: add onSeed: true (the default of -fix: the instance holds the records of %s; else it never runs)", n.Name, kind, n.Name),
				apply:  setWriteOnSeed(n.Name, kind),
			})
		}
	})
	return out
}

// setWriteOnSeed adds onSeed: true to a write of a node; a write in the
// short form becomes a mapping.
func setWriteOnSeed(name, kind string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.Write[kind] == nil || n.Write[kind].value == nil {
			return false
		}
		val := n.Write[kind].value
		if val.Kind == yaml.ScalarNode {
			call, params, err := shortForm(val.Value)
			if err != nil {
				return false
			}
			c := scalar(call)
			c.LineComment = val.LineComment
			content := []*yaml.Node{scalar("call"), c}
			if len(params) > 0 {
				content = append(content, scalar("path"), valueYAML(params, true))
			}
			val.Kind, val.Tag, val.Value, val.Style, val.LineComment = yaml.MappingNode, "", "", 0, ""
			val.Content = content
		}
		if val.Kind != yaml.MappingNode || valueOf(val, "onSeed") != nil {
			return false
		}
		val.Style = 0
		val.Content = append(val.Content, scalar("onSeed"), scalar("true"))
		return true
	}
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
