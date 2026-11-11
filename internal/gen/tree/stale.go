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

// checkScope finds what the file still sends although the scope excludes
// it (a tag or an operation added to exclude later; a merge leaves the
// file's nodes alone): a node that reads, a write and a require. The fix
// sets off: true (a require is removed); the scope stays the one place
// that says what is left out.
func checkScope(s *spec.Spec, t *Tree) ([]string, []Fix) {
	sc := t.Scope
	if len(sc.ExcludeTags) == 0 && len(sc.ExcludeOps) == 0 {
		return nil, nil
	}
	var out []string
	var fixes []Fix
	fix := func(f Fix, format string, args ...any) {
		out = append(out, fmt.Sprintf("line %d: %s", f.Line, fmt.Sprintf(format, args...))+f.change())
		if f.apply != nil {
			fixes = append(fixes, f)
		}
	}
	excluded := func(call string) (*spec.Operation, bool) {
		op := lookupOp(s, call)
		return op, op != nil && sc.Excluded(op)
	}
	t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone {
			return
		}
		if op, ex := excluded(n.Call); ex && n.Enabled() {
			why := "excluded by scope (" + sc.excludedBy(op) + ")"
			fix(Fix{Line: n.Line, Change: n.Name + ": add off: true # " + why, apply: setNodeOff(n.Name, why)},
				"%s: %s is %s, but the tree still fetches it", n.Name, callOf(op), why)
		} else if n.Enabled() {
			if field, res, by := throughExcluded(s, sc, n); res != "" {
				why := "reads through " + res + ", excluded by scope (" + by + ")"
				fix(Fix{Line: n.Line, Change: n.Name + ": add off: true # " + why, apply: setNodeOff(n.Name, why)},
					"%s: reads %s through the embedded %s, which the scope excludes (%s): the records it names belong to that record, not to the test", n.Name, field, res, by)
			}
		}
		for _, kind := range n.Kinds() {
			w := n.Write[kind]
			if w == nil || w.None || w.Off || w.Gone {
				continue
			}
			if op, ex := excluded(w.Call); ex {
				why := "excluded by scope (" + sc.excludedBy(op) + ")"
				fix(Fix{Line: w.Line, Change: n.Name + ".write." + kind + ": add off: true # " + why, apply: setWriteOff(n.Name, kind, why)},
					"%s.write.%s: %s is %s, but the test still sends it", n.Name, kind, callOf(op), why)
			}
		}
		for _, r := range n.Require {
			if op, ex := excluded(r.Call); ex {
				fix(Fix{Line: r.Line, Change: "remove " + n.Name + ".require." + r.Name, apply: removeRequire(n.Name, r.Name)},
					"%s.require.%s: %s is excluded by scope (%s)", n.Name, r.Name, callOf(op), sc.excludedBy(op))
			}
		}
	})
	return out, fixes
}

// readsItRe reads the note of a where the generator added for the node
// that reads the field: "shipPilot reads it".
var readsItRe = regexp.MustCompile(`^(\S+) reads it$`)

// checkStale finds a where the generator added for a node that reads the
// field, when the tree no longer has that node (removed, or never added by
// a merge) or does not fetch it (off, excluded by scope): it only leaves
// records out. The fix removes it.
func checkStale(s *spec.Spec, t *Tree) ([]string, []Fix) {
	var out []string
	var fixes []Fix
	t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone {
			return
		}
		for i, c := range slices.Concat(n.Where, n.WhereAny) {
			m := readsItRe.FindStringSubmatch(c.Note)
			if m == nil {
				continue
			}
			why := "which the tree does not have"
			if r := t.Node(m[1]); r != nil {
				if usable(s, t, r) {
					continue
				}
				why = "which is not fetched (off, or excluded by scope)"
			}
			f := Fix{Line: n.Line, Change: fmt.Sprintf("remove %s from the where of %s", c.Field, n.Name), apply: removeWhere(n.Name, c.Field)}
			if i >= len(n.Where) {
				f = Fix{Line: n.Line, Change: fmt.Sprintf("remove %s from the where any of %s", c.Field, n.Name), apply: removeWhereAny(n.Name, c.Field)}
			}
			out = append(out, fmt.Sprintf("line %d: %s.where.%s: %v was added for %s, %s: it only leaves records out",
				f.Line, n.Name, c.Field, c.Test, m[1], why)+f.change())
			fixes = append(fixes, f)
		}
	})
	return out, fixes
}

// setNodeOff adds off: true to a node, with the reason as comment.
func setNodeOff(name, why string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.value == nil || n.Off {
			return false
		}
		v := nodeMapping(n)
		on := scalar("true")
		on.LineComment = comment(why)
		at := len(v.Content)
		for i := 0; i+1 < len(v.Content); i += 2 {
			if v.Content[i].Value == "each" {
				at = i
			}
		}
		v.Content = slices.Insert(v.Content, at, scalar("off"), on)
		return true
	}
}

// setWriteOff adds off: true to a write of a node; a write in the short
// form becomes a mapping.
func setWriteOff(name, kind, why string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.Write[kind] == nil || n.Write[kind].value == nil {
			return false
		}
		val := n.Write[kind].value
		on := scalar("true")
		on.LineComment = comment(why)
		switch val.Kind {
		case yaml.ScalarNode:
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
			content = append(content, scalar("off"), on)
			val.Kind, val.Tag, val.Value, val.Style, val.LineComment = yaml.MappingNode, "", "", 0, ""
			val.Content = content
		case yaml.MappingNode:
			if valueOf(val, "off") != nil {
				return false
			}
			val.Style = 0
			val.Content = append(val.Content, scalar("off"), on)
		default:
			return false
		}
		return true
	}
}

// removeRequire removes a require of a node; the key goes with the last.
func removeRequire(name, req string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.value == nil || n.value.Kind != yaml.MappingNode {
			return false
		}
		reqs := valueOf(n.value, "require")
		if !deleteKey(reqs, req) {
			return false
		}
		if len(reqs.Content) == 0 {
			deleteKey(n.value, "require")
			deleteKey(n.value, "candidates")
		}
		return true
	}
}

// removeWhere removes a condition of a node's where; the key goes with the
// last.
func removeWhere(name, field string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.value == nil || n.value.Kind != yaml.MappingNode {
			return false
		}
		where := valueOf(n.value, "where")
		if !deleteKey(where, field) {
			return false
		}
		if len(where.Content) == 0 {
			deleteKey(n.value, "where")
		}
		return true
	}
}

// removeWhereAny removes an alternative from the where any of a node (the
// whole any when field is ""); an empty any and an empty where go.
func removeWhereAny(name, field string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.value == nil || n.value.Kind != yaml.MappingNode {
			return false
		}
		where := valueOf(n.value, "where")
		anyOf := valueOf(where, "any")
		switch {
		case anyOf == nil:
			return false
		case field == "":
			deleteKey(where, "any")
		case !deleteKey(anyOf, field):
			return false
		case len(anyOf.Content) == 0:
			deleteKey(where, "any")
		}
		if len(where.Content) == 0 {
			deleteKey(n.value, "where")
		}
		return true
	}
}

// foldWhereAny turns conditions of the where of a node into alternatives:
// they move into its where any (one of them must hold).
func foldWhereAny(name string, fields []string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.value == nil || n.value.Kind != yaml.MappingNode {
			return false
		}
		where := valueOf(n.value, "where")
		if where == nil || where.Kind != yaml.MappingNode || valueOf(where, "any") != nil {
			return false
		}
		for _, f := range fields {
			if valueOf(where, f) == nil {
				return false // nothing changed yet
			}
		}
		anyOf := &yaml.Node{Kind: yaml.MappingNode}
		for _, f := range fields {
			for i := 0; i+1 < len(where.Content); i += 2 {
				if where.Content[i].Value == f {
					anyOf.Content = append(anyOf.Content, where.Content[i], where.Content[i+1])
					where.Content = slices.Delete(where.Content, i, i+2)
					break
				}
			}
		}
		where.Content = append(where.Content, scalar("any"), anyOf)
		return true
	}
}

// deleteKey removes a key and its value from a mapping.
func deleteKey(m *yaml.Node, key string) bool {
	if m == nil || m.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			return true
		}
	}
	return false
}

// unknownNodeRef reports whether a value of order reads "<name>.field" of
// a name that is no node: a node removed from the tree.
func unknownNodeRef(t *Tree, v any) bool {
	str, ok := v.(string)
	if !ok {
		return false
	}
	name, field, ok := strings.Cut(str, ".")
	return ok && field != "" && nameForm(name) && t.Node(name) == nil
}

// throughExcluded finds a value of a node (path or query) that reads the
// record above through an embedded record of a resource the scope
// excludes (.shipClass.crew.pilotId with ShipClass excluded): the
// field, the resource and what excludes it; "" when none. A resource is
// excluded when the scope excludes every operation of it.
func throughExcluded(s *spec.Spec, sc Scope, n *Node) (string, string, string) {
	m := modelOf(s)
	for _, v := range append(sortedValues(n.Path), sortedValues(n.Query)...) {
		str, ok := v.(string)
		if !ok || !strings.HasPrefix(str, ".") {
			continue
		}
		segs := strings.Split(strings.TrimPrefix(str, "."), ".")
		for i, seg := range segs[:max(len(segs)-1, 0)] {
			for _, res := range m.Resources {
				if !strings.EqualFold(res.Name, seg) && !strings.EqualFold(res.Name, singular(seg)) {
					continue
				}
				if len(res.Ops) == 0 || slices.ContainsFunc(res.Ops, func(o *model.Op) bool { return !sc.Excluded(o.Op) }) {
					continue
				}
				if !embeddedRecord(s, n, segs[:i+1], res) {
					// a value inside the record (an object without a key
					// of that resource): its references are the record's
					continue
				}
				return str, res.Name, sc.excludedBy(res.Ops[0].Op)
			}
		}
	}
	return "", "", ""
}

// embeddedRecord reports whether the object at path in the records of the
// node above n is a record of res of its own: it holds a key of res (its
// id). An object without one (the kind of a garden, without id) is a value
// of the record above; when the schema says nothing, it counts as a record.
func embeddedRecord(s *spec.Spec, n *Node, path []string, res *model.Resource) bool {
	if n.Parent == nil || len(res.Keys) == 0 {
		return len(res.Keys) > 0
	}
	op := lookupOp(s, n.Parent.Call)
	if op == nil {
		return true
	}
	rec, _ := properties((&proposer{}).recordSchema(op))
	obj, _, ok := bodyField(rec, strings.Join(path, "."))
	if !ok || obj == nil || obj.Value == nil {
		return true
	}
	props, _ := properties(itemsOf(obj.Value))
	return slices.ContainsFunc(res.Keys, func(k string) bool { _, has := propertyFoldName(props, k); return has })
}

// sortedValues are the values of a map in the order of its keys.
func sortedValues(m map[string]any) []any {
	var out []any
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}

// callComment is the comment after the call of a node in the file (the
// operationId · why the generator proposed it), "" without one.
func callComment(n *Node) string {
	if n.value == nil {
		return n.Comment
	}
	v := n.value
	if v.Kind == yaml.MappingNode {
		if c := valueOf(v, "call"); c != nil {
			v = c
		}
	}
	return noteOf(v)
}
