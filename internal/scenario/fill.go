package scenario

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// placeholder matches "{{name}}" in a value; names may contain dots, as in
// "book1.bookId".
var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*\}\}`)

// whole matches a value that is one placeholder and nothing else.
var whole = regexp.MustCompile(`^\{\{\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*\}\}$`)

// Names returns the placeholders a value uses, in order, each once.
func Names(v any) []string {
	var out []string
	seen := map[string]bool{}
	walk(v, func(s string) {
		for _, m := range placeholder.FindAllStringSubmatch(s, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	})
	return out
}

func walk(v any, fn func(string)) {
	switch x := v.(type) {
	case string:
		fn(x)
	case map[string]any:
		for _, c := range x {
			walk(c, fn)
		}
	case []any:
		for _, c := range x {
			walk(c, fn)
		}
	}
}

// Fill returns a copy of v with its placeholders replaced by the values of
// vars: "{{bookId}}" alone takes the value with its type, a placeholder
// inside a text is written as text. missing lists the placeholders vars
// has no value for; they stay as they are.
func Fill(v any, vars map[string]any) (out any, missing []string) {
	switch x := v.(type) {
	case string:
		if m := whole.FindStringSubmatch(x); m != nil {
			val, ok := vars[m[1]]
			if !ok {
				return x, []string{m[1]}
			}
			return val, nil
		}
		if !strings.Contains(x, "{{") {
			return x, nil
		}
		s := placeholder.ReplaceAllStringFunc(x, func(p string) string {
			name := placeholder.FindStringSubmatch(p)[1]
			val, ok := vars[name]
			if !ok {
				missing = append(missing, name)
				return p
			}
			return Text(val)
		})
		return s, missing
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, c := range x {
			fc, miss := Fill(c, vars)
			m[k] = fc
			missing = append(missing, miss...)
		}
		return m, missing
	case []any:
		l := make([]any, len(x))
		for i, c := range x {
			fc, miss := Fill(c, vars)
			l[i] = fc
			missing = append(missing, miss...)
		}
		return l, missing
	}
	return v, nil
}

// Text is a plain value as text: a string as is, a number in its JSON
// form, other values as compact JSON.
func Text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// Clone returns a deep copy of a plain value.
func Clone(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, c := range x {
			m[k] = Clone(c)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, c := range x {
			l[i] = Clone(c)
		}
		return l
	}
	return v
}
