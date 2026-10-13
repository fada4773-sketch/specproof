package scenario

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// Finding codes of a diff.
const (
	StatusChanged   = "STATUS_CHANGED"
	FieldChanged    = "FIELD_CHANGED"
	FieldRemoved    = "FIELD_REMOVED"
	FieldAdded      = "FIELD_ADDED"
	HeaderChanged   = "HEADER_CHANGED"
	ElementMissing  = "ELEMENT_MISSING"
	ElementOther    = "ELEMENT_OTHER"
	SchemaViolation = "SCHEMA_VIOLATION"
	Slower          = "SLOWER"
	StepMissing     = "STEP_MISSING"
	RequestError    = "ERROR"
)

// Fail-on switches: findings of these codes fail only when named.
const (
	FailAdded  = "added"
	FailSlower = "slower"
	FailOther  = "other"
)

// Finding is one difference between two snapshots.
type Finding struct {
	Code    string
	Step    string // step id
	Request string // "GET /books/17"
	Pointer string // where in the body, "header ETag" for a header
	Old     string
	New     string
	Message string
	Fails   bool
}

// DiffOptions control Diff.
type DiffOptions struct {
	// Ignore are field names (at any level) and JSON pointers ("*" for any
	// element) that are not compared, besides the volatile fields of the
	// snapshots.
	Ignore []string
	// ListKey is the field list elements are matched by when the snapshot
	// names none; default "id".
	ListKey string
	// FailOn makes added fields (FailAdded), slower answers (FailSlower)
	// and other elements in a list (FailOther) fail.
	FailOn []string
	// SlowerFactor and SlowerMinMs: an answer is SLOWER if it took more
	// than SlowerFactor times as long as before and over SlowerMinMs.
	// Zero means 2 and 200.
	SlowerFactor float64
	SlowerMinMs  int64
	// IgnoreCase compares strings without regard to case.
	IgnoreCase bool
}

// Diff compares post with pre, step by step: status, compared headers,
// body field by field (lists by key), the schema of the post answers and
// the time they took.
func Diff(pre, post *Snapshot, opt DiffOptions) []Finding {
	if opt.ListKey == "" {
		opt.ListKey = "id"
	}
	if opt.SlowerFactor == 0 {
		opt.SlowerFactor = 2
	}
	if opt.SlowerMinMs == 0 {
		opt.SlowerMinMs = 200
	}
	var out []Finding
	for _, a := range pre.Steps {
		b := post.Shot(a.ID)
		add := func(f Finding) {
			f.Step, f.Request = a.ID, a.Request
			out = append(out, f)
		}
		if b == nil {
			add(Finding{Code: StepMissing, Message: "the post snapshot has no answer for this request", Fails: true})
			continue
		}
		if b.Error != "" {
			add(Finding{Code: RequestError, Old: statusText(a), New: b.Error, Message: "no answer after the deployment", Fails: true})
			continue
		}
		if a.Error != "" {
			// no answer before: nothing to compare with
			continue
		}
		for _, p := range b.Problems {
			add(Finding{Code: SchemaViolation, New: p, Message: "the answer does not fit the spec", Fails: true})
		}
		if a.Status != b.Status {
			add(Finding{Code: StatusChanged, Old: fmt.Sprint(a.Status), New: fmt.Sprint(b.Status), Message: "another status than before", Fails: true})
			continue
		}
		ignore := append(append(append([]string{}, opt.Ignore...), a.Volatile...), b.Volatile...)
		for _, name := range sortedKeys(a.Headers) {
			if slices.ContainsFunc(ignore, func(ig string) bool { return strings.EqualFold(ig, "header "+name) }) {
				continue
			}
			nv, ok := lookupHeader(b.Headers, name)
			switch {
			case !ok:
				add(Finding{Code: HeaderChanged, Pointer: "header " + name, Old: a.Headers[name], New: Missing, Message: "the header is gone", Fails: true})
			case nv != a.Headers[name]:
				add(Finding{Code: HeaderChanged, Pointer: "header " + name, Old: a.Headers[name], New: nv, Message: "the header changed", Fails: true})
			}
		}
		if a.HasBody || b.HasBody {
			lists := map[string]string{}
			for k, v := range a.Lists {
				lists[k] = v
			}
			d := differ{opt: opt, ignore: ignore, lists: lists}
			d.diff("", a.Body, b.Body, "")
			for _, f := range d.out {
				add(f)
			}
		}
		if b.DurationMs > opt.SlowerMinMs && a.DurationMs > 0 && float64(b.DurationMs) > opt.SlowerFactor*float64(a.DurationMs) {
			add(Finding{Code: Slower, Old: fmt.Sprintf("%d ms", a.DurationMs), New: fmt.Sprintf("%d ms", b.DurationMs),
				Message: "slower than before", Fails: slices.Contains(opt.FailOn, FailSlower)})
		}
	}
	return out
}

// Missing marks an absent value in a finding.
const Missing = "(missing)"

func statusText(s *Shot) string {
	if s.Status == 0 {
		return ""
	}
	return fmt.Sprint(s.Status)
}

func lookupHeader(h map[string]string, name string) (string, bool) {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

type differ struct {
	opt    DiffOptions
	ignore []string
	lists  map[string]string
	out    []Finding
}

func (d *differ) add(code, ptr string, before, after any, msg string, fails bool) {
	if ptr == "" {
		ptr = "/"
	}
	o, n := Missing, Missing
	if before != absent {
		o = render(before)
	}
	if after != absent {
		n = render(after)
	}
	d.out = append(d.out, Finding{Code: code, Pointer: ptr, Old: o, New: n, Message: msg, Fails: fails})
}

// absent stands for a value that is not there.
var absent = &struct{}{}

func (d *differ) diff(ptr string, a, b any, name string) {
	if d.ignored(ptr, name) {
		return
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			d.add(FieldChanged, ptr, a, b, "another type", true)
			return
		}
		for _, k := range sortedKeys(x) {
			child := ptr + "/" + escape(k)
			yv, ok := y[k]
			if !ok {
				if !d.ignored(child, k) {
					d.add(FieldRemoved, child, x[k], absent, "the field is gone", true)
				}
				continue
			}
			d.diff(child, x[k], yv, k)
		}
		for _, k := range sortedKeys(y) {
			if _, ok := x[k]; ok {
				continue
			}
			child := ptr + "/" + escape(k)
			if !d.ignored(child, k) {
				d.add(FieldAdded, child, absent, y[k], "a new field", slices.Contains(d.opt.FailOn, FailAdded))
			}
		}
	case []any:
		y, ok := b.([]any)
		if !ok {
			d.add(FieldChanged, ptr, a, b, "another type", true)
			return
		}
		if key := d.listKey(ptr, x, y); key != "" {
			d.keyed(ptr, key, x, y, name)
			return
		}
		if len(x) != len(y) {
			d.add(FieldChanged, ptr, a, b, fmt.Sprintf("%d instead of %d elements", len(y), len(x)), true)
			return
		}
		for i := range x {
			d.diff(fmt.Sprintf("%s/%d", ptr, i), x[i], y[i], name)
		}
	default:
		if _, isMap := b.(map[string]any); isMap {
			d.add(FieldChanged, ptr, a, b, "another type", true)
			return
		}
		if _, isList := b.([]any); isList {
			d.add(FieldChanged, ptr, a, b, "another type", true)
			return
		}
		if !sameScalar(a, b, d.opt.IgnoreCase) {
			d.add(FieldChanged, ptr, a, b, "", true)
		}
	}
}

// listKey is the field the elements of a list are matched by: the one the
// snapshot names for this list, else ListKey if every element has it.
func (d *differ) listKey(ptr string, a, b []any) string {
	key := d.lists[ptr]
	if key == "" {
		key = d.lists[Generalize(ptr)]
	}
	if key != "" {
		return key
	}
	if len(a) == 0 && len(b) == 0 {
		return ""
	}
	for _, l := range [][]any{a, b} {
		for _, el := range l {
			m, ok := el.(map[string]any)
			if !ok {
				return ""
			}
			if _, ok := m[d.opt.ListKey]; !ok {
				return ""
			}
		}
	}
	return d.opt.ListKey
}

// keyed compares two lists element by element, matched by key: an element
// of before that is gone is ELEMENT_MISSING, an element that came is
// ELEMENT_OTHER (someone else's data); matched elements are compared.
func (d *differ) keyed(ptr, key string, a, b []any, name string) {
	index := map[string]any{}
	for _, el := range b {
		if m, ok := el.(map[string]any); ok {
			index[Text(m[key])] = el
		}
	}
	seen := map[string]bool{}
	for _, el := range a {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		k := Text(m[key])
		seen[k] = true
		child := fmt.Sprintf("%s/[%s=%s]", ptr, key, k)
		other, ok := index[k]
		if !ok {
			d.add(ElementMissing, child, el, absent, "the element is no longer in the list", true)
			continue
		}
		d.diff(child, el, other, name)
	}
	for _, el := range b {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		k := Text(m[key])
		if !seen[k] {
			d.add(ElementOther, fmt.Sprintf("%s/[%s=%s]", ptr, key, k), absent, el, "another element in the list", slices.Contains(d.opt.FailOn, FailOther))
		}
	}
}

// ignored reports whether a field is not compared: by name at any level,
// or by a JSON pointer where "*" matches any element or key and a list
// element "[id=7]" counts as an element.
func (d *differ) ignored(ptr, name string) bool {
	for _, ig := range d.ignore {
		if strings.HasPrefix(ig, "header ") {
			continue
		}
		if !strings.HasPrefix(ig, "/") {
			if name != "" && (ig == name || strings.EqualFold(ig, name)) && !strings.HasSuffix(ptr, "]") {
				return true
			}
			continue
		}
		if pointerMatch(ig, ptr) {
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
		if pp[i] == "*" || pp[i] == ap[i] {
			continue
		}
		if strings.HasPrefix(ap[i], "[") && strings.HasSuffix(ap[i], "]") && pp[i] == "*" {
			continue
		}
		if ok, _ := path.Match(pp[i], ap[i]); ok {
			continue
		}
		return false
	}
	return true
}

func sameScalar(a, b any, fold bool) bool {
	if fmt.Sprintf("%T", a) != fmt.Sprintf("%T", b) {
		return false
	}
	ta, tb := Text(a), Text(b)
	if fold {
		return strings.EqualFold(ta, tb)
	}
	return ta == tb
}

func render(v any) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	if v == nil {
		return "null"
	}
	return Text(v)
}

func escape(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

// Failed reports whether any finding fails.
func Failed(fs []Finding) bool {
	return slices.ContainsFunc(fs, func(f Finding) bool { return f.Fails })
}
