package tree

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/scenario"
)

// Ref is a reference to a field of a record: ".field" (Node "") or
// "<node>.field".
type Ref struct {
	Node  string
	Field string // a dot path; "" is the whole record
}

// ParseRef reads a value as a reference. A string ".x" always is one; a
// string "<name>.x" only if name is a node of the scope.
func ParseRef(v any, scope func(string) bool) (Ref, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return Ref{}, false
	}
	if s == "." {
		return Ref{}, true
	}
	if strings.HasPrefix(s, ".") {
		return Ref{Field: s[1:]}, true
	}
	name, field, ok := strings.Cut(s, ".")
	if !ok || !nameForm(name) || scope == nil || !scope(name) {
		return Ref{}, false
	}
	return Ref{Node: name, Field: field}, true
}

// ParamRef reads a value as a reference to params: "$name".
func ParamRef(v any) (string, bool) {
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, "$") || !nameForm(s[1:]) {
		return "", false
	}
	return s[1:], true
}

// composedRe finds the parts of a composed value: "{{planets.code}}-{{.number}}".
var composedRe = regexp.MustCompile(`\{\{\s*([^{}\s]+)\s*\}\}`)

// treeRef reports whether s is a reference of the tree: ".field",
// "<node>.field" or "$name".
func treeRef(s string, scope func(string) bool) bool {
	if _, ok := ParamRef(s); ok {
		return true
	}
	_, ok := ParseRef(s, scope)
	return ok
}

// ComposedRefs returns the references a text composes ("{{planets.code}}-
// {{.number}}"), nil for a value that composes none. A "{{name}}" that is no
// reference of the tree stays as it is: a value the scenario saved.
func ComposedRefs(v any, scope func(string) bool) []string {
	s, ok := v.(string)
	if !ok || !strings.Contains(s, "{{") {
		return nil
	}
	var out []string
	for _, m := range composedRe.FindAllStringSubmatch(s, -1) {
		if treeRef(m[1], scope) {
			out = append(out, m[1])
		}
	}
	return out
}

// compose fills the references of a composed text by fill, as text; a text
// that is one reference alone keeps the type of its value.
func compose(s string, scope func(string) bool, fill func(ref string) (any, error)) (any, error) {
	ms := composedRe.FindAllStringSubmatchIndex(s, -1)
	if len(ms) == 1 && ms[0][0] == 0 && ms[0][1] == len(s) && treeRef(s[ms[0][2]:ms[0][3]], scope) {
		return fill(s[ms[0][2]:ms[0][3]])
	}
	var b strings.Builder
	last := 0
	for _, m := range ms {
		ref := s[m[2]:m[3]]
		if !treeRef(ref, scope) {
			continue
		}
		v, err := fill(ref)
		if err != nil {
			return nil, err
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(text(v))
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String(), nil
}

// refsOf are the references a text of the tree holds: itself, or the parts
// it composes.
func refsOf(s string, scope func(string) bool) []string {
	if parts := ComposedRefs(s, scope); len(parts) > 0 {
		return parts
	}
	return []string{s}
}

// param is the value of params a "$name" names; ok is false while it is
// not set.
func (t *Tree) param(name string) (any, bool) {
	v, ok := t.Params[name]
	if !ok || v == nil || v == "" {
		return nil, false
	}
	return v, true
}

// UnsetParams lists the entries of params without a value, in file order.
func (t *Tree) UnsetParams() []string {
	var out []string
	for _, name := range t.ParamNames {
		if _, set := t.param(name); !set {
			out = append(out, name)
		}
	}
	return out
}

// String writes the reference back.
func (r Ref) String() string {
	if r.Node == "" {
		return "." + r.Field
	}
	return r.Node + "." + r.Field
}

// Field returns the value at a dot path ("route.originId", "items.0.id")
// of a record, and whether it is there.
func Field(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		switch x := cur.(type) {
		case map[string]any:
			next, ok := x[seg]
			if !ok {
				// field names often differ in case between path and body
				for k, c := range x {
					if strings.EqualFold(k, seg) {
						next, ok = c, true
						break
					}
				}
			}
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			cur = x[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// Pointer returns the value at a JSON pointer, and whether it is there.
func Pointer(v any, ptr string) (any, bool) {
	if ptr == "" || ptr == "/" {
		return v, true
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, false
	}
	var parts []string
	for _, seg := range strings.Split(ptr[1:], "/") {
		parts = append(parts, strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~"))
	}
	cur := v
	for _, seg := range parts {
		switch x := cur.(type) {
		case map[string]any:
			next, ok := x[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			cur = x[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// PointerField is the dot path of a single-level pointer: "/id" → "id".
func PointerField(ptr string) string {
	return strings.ReplaceAll(strings.TrimPrefix(ptr, "/"), "/", ".")
}

// text is the comparable text of a value: "7" for 7 and "7".
func text(v any) string { return scenario.Text(v) }

// Holds reports whether a record passes the conditions of a node.
func (n *Node) Holds(rec any) bool { return n.HoldsWith(rec, nil) }

// HoldsWith is Holds with the tests that are references filled by fill:
// "id: docks.pilotId" holds for the pilot a dock names.
func (n *Node) HoldsWith(rec any, fill func(any) any) bool {
	test := func(c Clause) bool {
		if fill != nil {
			c.Test = fill(c.Test)
		}
		return c.holds(rec)
	}
	for _, c := range n.Where {
		if !test(c) {
			return false
		}
	}
	if len(n.WhereAny) == 0 {
		return true
	}
	for _, c := range n.WhereAny {
		if test(c) {
			return true
		}
	}
	return false
}

// noValue is the test of a reference without value: no record holds it.
type noValue struct{}

// holds tests one clause: present, absent, a value, one of a list, or a
// comparison (">=1950", "<10", "!=OPEN").
func (c Clause) holds(rec any) bool {
	v, ok := Field(rec, c.Field)
	if ok && v == nil {
		ok = false
	}
	switch t := c.Test.(type) {
	case string:
		switch t {
		case "present":
			return ok
		case "absent":
			return !ok
		}
		for _, op := range []string{">=", "<=", "!=", ">", "<"} {
			if strings.HasPrefix(t, op) {
				return ok && compare(v, strings.TrimSpace(t[len(op):]), op)
			}
		}
		return ok && text(v) == t
	case []any:
		if !ok {
			return false
		}
		for _, x := range t {
			if text(v) == text(x) {
				return true
			}
		}
		return false
	case nil:
		return !ok
	case noValue:
		return false
	}
	return ok && text(v) == text(c.Test)
}

// compare compares numerically when both sides are numbers, else as text
// (dates in ISO form compare correctly).
func compare(v any, want, op string) bool {
	a, errA := strconv.ParseFloat(text(v), 64)
	b, errB := strconv.ParseFloat(want, 64)
	var c int
	if errA == nil && errB == nil {
		switch {
		case a < b:
			c = -1
		case a > b:
			c = 1
		}
	} else {
		c = strings.Compare(text(v), want)
	}
	switch op {
	case ">=":
		return c >= 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	case "<":
		return c < 0
	}
	return c != 0
}

// KeyText is the text of the key values of a record, joined by "/".
func KeyText(rec any, key []string) string {
	var parts []string
	for _, k := range key {
		v, ok := Pointer(rec, k)
		if !ok {
			return ""
		}
		parts = append(parts, text(v))
	}
	return strings.Join(parts, "/")
}

// encodeJSON is a stable text of a value, for cache keys.
func encodeJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
