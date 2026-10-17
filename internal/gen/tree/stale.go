package tree

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

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
		for _, c := range n.Where {
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
