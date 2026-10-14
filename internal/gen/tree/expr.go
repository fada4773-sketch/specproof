package tree

import (
	"encoding/json"
	"fmt"
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
func (n *Node) Holds(rec any) bool {
	for _, c := range n.Where {
		if !c.holds(rec) {
			return false
		}
	}
	if len(n.WhereAny) == 0 {
		return true
	}
	for _, c := range n.WhereAny {
		if c.holds(rec) {
			return true
		}
	}
	return false
}

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
