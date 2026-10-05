package spec

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/oasdiff/yaml"
)

// diagnoseRefs looks for local "$ref"s in the main file that do not resolve.
// kin-openapi reports these without a location; this adds the place in the
// spec to the error message (FR-SPEC-03, NFR-08). It returns "" if it finds
// nothing, so the original error is kept.
func diagnoseRefs(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	js, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return ""
	}
	var root any
	if err := json.Unmarshal(js, &root); err != nil {
		return ""
	}
	var problems []string
	walkRefs(root, nil, func(where []string, ref string) {
		if !strings.HasPrefix(ref, "#") {
			return
		}
		if _, ok := resolvePointer(root, strings.TrimPrefix(ref, "#")); !ok {
			problems = append(problems, fmt.Sprintf("%s: $ref %q points to nothing", strings.Join(where, "."), ref))
		}
	})
	sort.Strings(problems)
	return strings.Join(problems, "; ")
}

func walkRefs(v any, where []string, fn func([]string, string)) {
	switch x := v.(type) {
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok {
			fn(where, ref)
		}
		for k, child := range x {
			walkRefs(child, append(append([]string(nil), where...), k), fn)
		}
	case []any:
		for i, child := range x {
			walkRefs(child, append(append([]string(nil), where...), strconv.Itoa(i)), fn)
		}
	}
}

// resolvePointer resolves a JSON pointer ("/a/b") in a decoded document.
func resolvePointer(root any, ptr string) (any, bool) {
	if ptr == "" {
		return root, true
	}
	if p, err := url.PathUnescape(ptr); err == nil {
		ptr = p
	}
	cur := root
	for _, part := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
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
