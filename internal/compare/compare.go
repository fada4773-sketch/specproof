// Package compare implements stage 2 (schema) and stage 3 (example) of the
// response checks (FR-CMP).
package compare

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Mode is the comparison mode (FR-CMP-02).
type Mode string

// Comparison modes.
const (
	ModeSchema Mode = "schema" // only stage 2
	ModeSubset Mode = "subset" // every field of the example must match; extra fields are fine
	ModeExact  Mode = "exact"  // identical apart from ignored fields
)

// ParseMode validates a mode from Config or x-apitest-compare.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeSchema, ModeSubset, ModeExact:
		return Mode(s), nil
	}
	return "", fmt.Errorf("unknown comparison mode %q (allowed: schema, subset, exact)", s)
}

// Missing marks an absent value in a Diff.
const Missing = "(missing)"

// Diff is one difference between expected and actual value (FR-CMP-08).
type Diff struct {
	Pointer  string
	Expected string
	Actual   string
	Note     string // optional explanation
}

// Options control Values.
type Options struct {
	Mode Mode
	// Ignore contains field names (matched at every level) and JSON pointers
	// (starting with "/"; "*" matches any array index or key).
	Ignore []string
	// Schema of the actual value. Fields with format uuid, date-time, date or
	// readOnly are only checked for presence (FR-CMP-05).
	Schema *openapi3.Schema
	// Unordered compares all arrays independent of order (FR-CMP-07). Single
	// arrays can be marked with x-apitest-compare-unordered in the schema.
	Unordered bool
}

// Values compares expected with actual. Both must be in canonical form
// (numbers as json.Number). The result is sorted by pointer.
func Values(expected, actual any, opt Options) []Diff {
	if opt.Mode == ModeSchema {
		return nil
	}
	c := comparer{opt: opt}
	c.compare("", expected, actual, opt.Schema, true)
	sort.SliceStable(c.diffs, func(i, j int) bool { return c.diffs[i].Pointer < c.diffs[j].Pointer })
	return c.diffs
}

type comparer struct {
	opt   Options
	diffs []Diff
}

func (c *comparer) add(ptr string, exp, act any, present bool, note string) {
	a := Missing
	if present {
		a = Render(act)
	}
	if ptr == "" {
		ptr = "/"
	}
	c.diffs = append(c.diffs, Diff{Pointer: ptr, Expected: Render(exp), Actual: a, Note: note})
}

func (c *comparer) compare(ptr string, exp, act any, s *openapi3.Schema, present bool) {
	if s != nil && formatOnly(s) {
		if !present {
			c.add(ptr, exp, nil, false, "")
		}
		return
	}
	if !present {
		c.add(ptr, exp, nil, false, "")
		return
	}
	switch e := exp.(type) {
	case map[string]any:
		a, ok := act.(map[string]any)
		if !ok {
			c.add(ptr, exp, act, true, "different type")
			return
		}
		for _, k := range sortedKeys(e) {
			child := ptr + "/" + escape(k)
			if c.ignored(child, k) {
				continue
			}
			av, ok := a[k]
			c.compare(child, e[k], av, property(s, k), ok)
		}
		if c.opt.Mode == ModeExact {
			for _, k := range sortedKeys(a) {
				child := ptr + "/" + escape(k)
				if _, ok := e[k]; ok || c.ignored(child, k) {
					continue
				}
				c.diffs = append(c.diffs, Diff{Pointer: child, Expected: Missing, Actual: Render(a[k]), Note: "extra field"})
			}
		}
	case []any:
		a, ok := act.([]any)
		if !ok {
			c.add(ptr, exp, act, true, "different type")
			return
		}
		// Arrays are compared in order. In subset mode the actual array may
		// have more elements than the example (like extra fields).
		if len(a) < len(e) || (c.opt.Mode == ModeExact && len(a) != len(e)) {
			c.add(ptr, exp, act, true, fmt.Sprintf("%d instead of %d elements", len(a), len(e)))
			return
		}
		var is *openapi3.Schema
		if s != nil && s.Items != nil {
			is = s.Items.Value
		}
		if c.opt.Unordered || unordered(s) {
			c.compareUnordered(ptr, e, a, is)
			return
		}
		for i := range e {
			child := ptr + "/" + strconv.Itoa(i)
			if c.ignored(child, "") {
				continue
			}
			c.compare(child, e[i], a[i], is, true)
		}
	default:
		if !Equal(exp, act) {
			c.add(ptr, exp, act, true, "")
		}
	}
}

// compareUnordered matches every expected element with a different actual
// element that has no differences (FR-CMP-07).
func (c *comparer) compareUnordered(ptr string, exp, act []any, items *openapi3.Schema) {
	used := make([]bool, len(act))
	for i, e := range exp {
		child := ptr + "/" + strconv.Itoa(i)
		if c.ignored(child, "") {
			continue
		}
		found := false
		for j, a := range act {
			if used[j] {
				continue
			}
			sub := comparer{opt: c.opt}
			sub.compare(child, e, a, items, true)
			if len(sub.diffs) == 0 {
				used[j], found = true, true
				break
			}
		}
		if !found {
			c.diffs = append(c.diffs, Diff{Pointer: child, Expected: Render(e), Actual: Missing, Note: "no matching element in any order"})
		}
	}
}

// unordered reports x-apitest-compare-unordered on an array schema.
func unordered(s *openapi3.Schema) bool {
	if s == nil {
		return false
	}
	v, _ := s.Extensions["x-apitest-compare-unordered"].(bool)
	return v
}

// ignored reports whether a field is excluded from the value comparison
// (FR-CMP-04): by field name at any level, or by JSON pointer.
func (c *comparer) ignored(ptr, name string) bool {
	for _, ig := range c.opt.Ignore {
		if strings.HasPrefix(ig, "/") {
			if pointerMatch(ig, ptr) {
				return true
			}
		} else if name != "" && ig == name {
			return true
		}
	}
	return false
}

func pointerMatch(pattern, ptr string) bool {
	pp := strings.Split(pattern, "/")
	ap := strings.Split(ptr, "/")
	if len(pp) != len(ap) {
		return false
	}
	for i := range pp {
		if pp[i] != "*" && pp[i] != ap[i] {
			return false
		}
	}
	return true
}

// formatOnly reports whether only presence (and format, checked in stage 2)
// matters for a field, because the server generates its value (FR-CMP-05).
func formatOnly(s *openapi3.Schema) bool {
	if s.ReadOnly {
		return true
	}
	switch s.Format {
	case "uuid", "date-time", "date":
		return true
	}
	return false
}

// property returns the schema of property k, looking into allOf/oneOf/anyOf.
func property(s *openapi3.Schema, k string) *openapi3.Schema {
	if s == nil {
		return nil
	}
	if ref := s.Properties[k]; ref != nil && ref.Value != nil {
		return ref.Value
	}
	for _, refs := range []openapi3.SchemaRefs{s.AllOf, s.OneOf, s.AnyOf} {
		for _, ref := range refs {
			if ref == nil || ref.Value == nil {
				continue
			}
			if p := property(ref.Value, k); p != nil {
				return p
			}
		}
	}
	return nil
}

// Equal compares two primitive values; numbers are compared numerically
// without loss of precision (FR-CMP-06).
func Equal(a, b any) bool {
	an, aNum := number(a)
	bn, bNum := number(b)
	if aNum || bNum {
		return aNum && bNum && an.Cmp(bn) == 0
	}
	switch av := a.(type) {
	case nil:
		return b == nil
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	}
	return Render(a) == Render(b)
}

func number(v any) (*big.Rat, bool) {
	var s string
	switch x := v.(type) {
	case json.Number:
		s = x.String()
	case float64:
		s = strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		s = strconv.Itoa(x)
	case int64:
		s = strconv.FormatInt(x, 10)
	default:
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

// Render formats a value as compact JSON for diffs and the report.
func Render(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func escape(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
