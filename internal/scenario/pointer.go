package scenario

import (
	"strconv"
	"strings"
)

// Lookup reads a JSON pointer in a value. Besides keys and indexes a part
// may find the element of a list by its fields: "/[code=A-1][planet=mars]/id"
// is the id of the element whose code is A-1 and whose planet is mars (the
// first that has them; values compared as text, field names ignoring case).
// A part that finds an element in an object takes its one list (a page).
// "~1" stands for "/", "~0" for "~", in keys and in the values of a part.
func Lookup(v any, ptr string) (any, bool) {
	if ptr == "" {
		return v, v != nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, false
	}
	cur := v
	for _, part := range strings.Split(ptr[1:], "/") {
		if conds, ok := findPart(part); ok {
			list, isList := cur.([]any)
			if !isList {
				if list = oneList(cur); list == nil {
					return nil, false
				}
			}
			var found any
			for _, el := range list {
				if matches(el, conds) {
					found = el
					break
				}
			}
			if found == nil {
				return nil, false
			}
			cur = found
			continue
		}
		part = unescape(part)
		switch x := cur.(type) {
		case map[string]any:
			next, ok := x[part]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(part)
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

// FillSource puts the saved values into the placeholders of a save source,
// escaped for a pointer ("{{ship1.code}}" in "/[code={{ship1.code}}]/id").
func FillSource(src string, vars map[string]any) (string, []string) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(src, func(p string) string {
		name := placeholder.FindStringSubmatch(p)[1]
		val, ok := vars[name]
		if !ok {
			missing = append(missing, name)
			return p
		}
		return strings.NewReplacer("~", "~0", "/", "~1").Replace(Text(val))
	})
	return out, missing
}

// FindPart writes the part of a pointer that finds a list element by the
// values of its fields, in the order given.
func FindPart(fields []string, values []string) string {
	var b strings.Builder
	for i, f := range fields {
		b.WriteString("[" + f + "=" + values[i] + "]")
	}
	return b.String()
}

type cond struct{ field, value string }

// findPart reads "[a=1][b=2]".
func findPart(part string) ([]cond, bool) {
	if !strings.HasPrefix(part, "[") || !strings.HasSuffix(part, "]") {
		return nil, false
	}
	var out []cond
	for _, c := range strings.Split(part[1:len(part)-1], "][") {
		f, v, ok := strings.Cut(c, "=")
		if !ok || f == "" {
			return nil, false
		}
		out = append(out, cond{unescape(f), unescape(v)})
	}
	return out, true
}

func matches(el any, conds []cond) bool {
	m, ok := el.(map[string]any)
	if !ok {
		return false
	}
	for _, c := range conds {
		v, found := m[c.field]
		if !found {
			for k, x := range m {
				if strings.EqualFold(k, c.field) {
					v, found = x, true
					break
				}
			}
		}
		if !found || Text(v) != c.value {
			return false
		}
	}
	return true
}

// oneList is the one list field of an object, nil for none or several.
func oneList(v any) []any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	var found []any
	n := 0
	for _, c := range m {
		if l, isList := c.([]any); isList {
			found = l
			n++
		}
	}
	if n != 1 {
		return nil
	}
	return found
}

func unescape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
}
