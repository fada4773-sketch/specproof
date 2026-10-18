package tree

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// generatedName reports whether a field is one the instance gives a
// record: its id. A readOnly field may as well name another record
// (moonId of a garden), so only the name counts.
func generatedName(f string) bool { return strings.EqualFold(f, "id") }

// generatedKeys are the fields the instance gives the records of n (their
// id) that key lacks: n creates records, they have the field, the create
// does not require it. An empty instance gives another value than the
// stage (456 there, 1 here): with the field in key the create leaves it
// out and saves the new one for the steps after it.
func generatedKeys(s *spec.Spec, n *Node) []string {
	if n.IsRequire || n.Gone {
		return nil
	}
	w, cop := ownCreate(s, n)
	op := lookupOp(s, n.Call)
	if w == nil || op == nil {
		return nil
	}
	rec, _ := properties((&proposer{}).recordSchema(op))
	_, required := properties(schemaOf(requestRef(cop)))
	fold := func(f string) func(string) bool {
		return func(x string) bool { return strings.EqualFold(strings.TrimPrefix(x, "/"), f) }
	}
	var out []string
	for _, f := range sortedKeys(rec) {
		if !generatedName(f) {
			continue
		}
		if !slices.ContainsFunc(required, fold(f)) && !slices.ContainsFunc(keyOf(n), fold(f)) {
			out = append(out, f)
		}
	}
	return out
}

// checkGeneratedKeys names the nodes whose key lacks the id the instance
// gives their records; -fix adds it to key.
func checkGeneratedKeys(s *spec.Spec, t *Tree) ([]string, []Fix) {
	var out []string
	var fixes []Fix
	t.Walk(func(n *Node) {
		for _, f := range generatedKeys(s, n) {
			ptr := "/" + f
			keys := keyOf(n)
			fx := Fix{Line: n.Line, Change: fmt.Sprintf("%s: key: [%s] → key: [%s]", n.Name, strings.Join(keys, ", "), strings.Join(append(slices.Clone(keys), ptr), ", ")),
				apply: addKey(n.Name, ptr)}
			out = append(out, fmt.Sprintf("line %d: %s: key lacks %s: the instance gives the %s of a record it creates, an empty instance another one than the stage; with it in key the create leaves it out and saves the new one for the steps after it",
				n.Line, n.Name, ptr, f)+fx.change())
			fixes = append(fixes, fx)
		}
	})
	return out, fixes
}

// addGeneratedKeys puts into the key of every node the id the instance
// gives its records (the proposal).
func addGeneratedKeys(s *spec.Spec, t *Tree) {
	t.Walk(func(n *Node) {
		for _, f := range generatedKeys(s, n) {
			n.Key = append(slices.Clone(keyOf(n)), "/"+f)
		}
	})
}

// addKey adds a pointer to the key of a node: "key: [/code]" becomes
// "key: [/code, /id]"; a node without key gets one before where, write
// and each.
func addKey(name, ptr string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.value == nil {
			return false
		}
		v := nodeMapping(n)
		if v == nil || v.Kind != yaml.MappingNode {
			return false
		}
		key := valueOf(v, "key")
		switch {
		case key == nil:
			at := len(v.Content)
			for i := 0; i+1 < len(v.Content); i += 2 {
				if k := v.Content[i].Value; k == "save" || k == "write" || k == "each" {
					at = i
					break
				}
			}
			seq := stringsYAML(append(slices.Clone(keyOf(n)), ptr))
			seq.Style = yaml.FlowStyle
			v.Content = slices.Insert(v.Content, at, scalar("key"), seq)
			return true
		case key.Kind == yaml.ScalarNode:
			if key.Value == ptr {
				return false
			}
			seq := stringsYAML([]string{key.Value, ptr})
			seq.LineComment = key.LineComment
			*key = *seq
			return true
		case key.Kind == yaml.SequenceNode:
			for _, c := range key.Content {
				if strings.EqualFold(c.Value, ptr) {
					return false
				}
			}
			key.Content = append(key.Content, scalar(ptr))
			return true
		}
		return false
	}
}
