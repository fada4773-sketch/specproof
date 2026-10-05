// Package selection evaluates "go test -run" and "-skip" patterns the same
// way the testing package does. apitest needs this before running a case,
// because a filtered subtest is never started, but its preconditions must
// still run (FR-GO-02).
//
// The logic mirrors testing/match.go (Go 1.27): a pattern is split at "/"
// and "|" outside of brackets and parentheses; every element is a regular
// expression for the name element at the same level.
package selection

import (
	"flag"
	"fmt"
	"regexp"
	"strings"
)

// Filter decides whether a test name is selected.
type Filter struct {
	run  matcher // nil means "everything"
	skip matcher // nil means "nothing"
}

// New builds a filter from -run and -skip patterns. Empty patterns select
// everything and skip nothing.
func New(run, skip string) (*Filter, error) {
	f := &Filter{}
	var err error
	if run != "" {
		if f.run, err = compile(run); err != nil {
			return nil, fmt.Errorf("-test.run: %w", err)
		}
	}
	if skip != "" {
		if f.skip, err = compile(skip); err != nil {
			return nil, fmt.Errorf("-test.skip: %w", err)
		}
	}
	return f, nil
}

// FromFlags builds a filter from the flags of the running test binary.
func FromFlags() (*Filter, error) {
	return New(flagValue("test.run"), flagValue("test.skip"))
}

func flagValue(name string) string {
	if f := flag.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// Selected reports whether the test with the full name (elements separated
// by "/") would run.
func (f *Filter) Selected(fullName string) bool {
	elem := strings.Split(fullName, "/")
	if f.run != nil {
		if ok, _ := f.run.matches(elem); !ok {
			return false
		}
	}
	if f.skip != nil {
		if skip, partial := f.skip.matches(elem); skip && !partial {
			return false
		}
	}
	return true
}

type matcher interface {
	matches(name []string) (ok, partial bool)
}

type simple []*regexp.Regexp

func (m simple) matches(name []string) (ok, partial bool) {
	for i, s := range name {
		if i >= len(m) {
			break
		}
		if !m[i].MatchString(s) {
			return false, false
		}
	}
	return true, len(name) < len(m)
}

type alternation []matcher

func (m alternation) matches(name []string) (ok, partial bool) {
	for _, alt := range m {
		if ok, partial = alt.matches(name); ok {
			return ok, partial
		}
	}
	return false, false
}

// compile splits s like testing.splitRegexp and compiles every element after
// applying the same rewrite as test names.
func compile(s string) (matcher, error) {
	var alts [][]string
	var cur []string
	cs, cp := 0, 0
	for i := 0; i < len(s); {
		switch s[i] {
		case '[':
			cs++
		case ']':
			if cs--; cs < 0 {
				cs = 0
			}
		case '(':
			if cs == 0 {
				cp++
			}
		case ')':
			if cs == 0 {
				cp--
			}
		case '\\':
			i++
		case '/':
			if cs == 0 && cp == 0 {
				cur = append(cur, s[:i])
				s, i = s[i+1:], 0
				continue
			}
		case '|':
			if cs == 0 && cp == 0 {
				cur = append(cur, s[:i])
				s, i = s[i+1:], 0
				alts = append(alts, cur)
				cur = nil
				continue
			}
		}
		i++
	}
	alts = append(alts, append(cur, s))

	var out alternation
	for _, elems := range alts {
		var m simple
		for _, e := range elems {
			re, err := regexp.Compile(rewrite(e))
			if err != nil {
				return nil, err
			}
			m = append(m, re)
		}
		out = append(out, m)
	}
	if len(out) == 1 {
		return out[0], nil
	}
	return out, nil
}

// rewrite is testing.rewrite, applied to pattern elements as well.
func rewrite(s string) string {
	var b strings.Builder
	for _, r := range s {
		if isSpace(r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isSpace(r rune) bool {
	if r < 0x2000 {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xA0, 0x1680:
			return true
		}
		return false
	}
	if r <= 0x200a {
		return true
	}
	switch r {
	case 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return false
}
