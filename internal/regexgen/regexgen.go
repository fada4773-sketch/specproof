// Package regexgen produces strings that match a schema pattern. Patterns
// are ECMA-262; candidates are built from Go's regexp syntax tree and every
// candidate is checked against the original pattern with the same engine
// apitest validates with. Lookarounds, which RE2 cannot parse, are removed
// for building and enforced by that check (generate and test).
package regexgen

import (
	"math/rand/v2"
	"regexp/syntax"
	"strings"
	"unicode/utf8"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Limits are the length constraints of the string; Max < 0 means none.
type Limits struct{ Min, Max int }

// Generate returns a string that matches pattern within the limits. The
// first half of the attempts prefers readable characters (lower-case
// letters and digits), the rest spreads over all allowed characters, which
// helps patterns with lookaheads like "at least one upper-case letter".
func Generate(pattern string, lim Limits, rnd *rand.Rand, attempts int) (string, bool) {
	m, err := spec.CompilePattern(pattern)
	if err != nil {
		return "", false
	}
	sources := []string{pattern}
	if stripped := StripLookarounds(pattern); stripped != pattern {
		sources = append(sources, stripped)
	}
	var trees []*syntax.Regexp
	for _, src := range sources {
		if re, err := syntax.Parse(src, syntax.Perl); err == nil {
			trees = append(trees, re.Simplify())
		}
	}
	if len(trees) == 0 {
		return "", false
	}
	for i := range attempts {
		g := &gen{rnd: rnd, readable: i < attempts/2}
		s := g.build(trees[i%len(trees)])
		s = fit(s, lim, m.MatchString)
		if s != "" && within(s, lim) && m.MatchString(s) {
			return s, true
		}
	}
	return "", false
}

// Highest returns the string a pattern matches that is built from the
// highest characters and the longest repetitions it allows, within the
// limits: "999" for ^\d{3}$, "ZZ9999" for ^[A-Z]{2}\d{4}$. Such a value is
// the least likely to belong to a real record, so it serves as an unknown
// key. ok is false if no such string matches (then use Generate).
func Highest(pattern string, lim Limits) (string, bool) {
	m, err := spec.CompilePattern(pattern)
	if err != nil {
		return "", false
	}
	re, err := syntax.Parse(StripLookarounds(pattern), syntax.Perl)
	if err != nil {
		return "", false
	}
	g := &gen{highest: true, limit: lim.Max}
	s := fit(g.build(re.Simplify()), lim, m.MatchString)
	if s == "" || !within(s, lim) || !m.MatchString(s) {
		return "", false
	}
	return s, true
}

func within(s string, lim Limits) bool {
	n := utf8.RuneCountInString(s)
	return n >= lim.Min && (lim.Max < 0 || n <= lim.Max)
}

// fit pads a too short candidate for unanchored patterns, where extra
// characters keep the match; anchored ones are left to the next attempt.
func fit(s string, lim Limits, match func(string) bool) string {
	n := utf8.RuneCountInString(s)
	if n >= lim.Min {
		return s
	}
	padded := s + strings.Repeat("a", lim.Min-n)
	if match(padded) {
		return padded
	}
	return s
}

type gen struct {
	rnd      *rand.Rand
	readable bool
	// highest takes the last alternative, the longest repetition (up to
	// limit runes in total, if limit >= 0) and the highest character.
	highest bool
	limit   int
}

func (g *gen) build(re *syntax.Regexp) string {
	var b strings.Builder
	g.write(&b, re, 0)
	return b.String()
}

func (g *gen) write(b *strings.Builder, re *syntax.Regexp, depth int) {
	if depth > 50 {
		return
	}
	switch re.Op {
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			b.WriteRune(r)
		}
	case syntax.OpCharClass:
		b.WriteRune(g.pick(re.Rune))
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		// readable: letters and digits; otherwise any printable character
		b.WriteRune(g.pick([]rune{'0', '9', 'A', 'Z', 'a', 'z', 0x21, 0x7e}))
	case syntax.OpCapture:
		g.write(b, re.Sub[0], depth+1)
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			g.write(b, sub, depth+1)
		}
	case syntax.OpAlternate:
		if g.highest {
			g.write(b, re.Sub[len(re.Sub)-1], depth+1)
			break
		}
		g.write(b, re.Sub[g.rnd.IntN(len(re.Sub))], depth+1)
	case syntax.OpStar:
		g.repeat(b, re.Sub[0], 0, 8, depth)
	case syntax.OpPlus:
		g.repeat(b, re.Sub[0], 1, 8, depth)
	case syntax.OpQuest:
		g.repeat(b, re.Sub[0], 0, 1, depth)
	case syntax.OpRepeat:
		hi := re.Max
		if hi < 0 || hi > re.Min+4 {
			hi = re.Min + 4
		}
		g.repeat(b, re.Sub[0], re.Min, hi, depth)
	}
	// anchors, word boundaries and empty matches produce nothing
}

func (g *gen) repeat(b *strings.Builder, re *syntax.Regexp, lo, hi, depth int) {
	n := lo
	switch {
	case g.highest:
		n = hi
		if g.limit >= 0 {
			n = max(lo, min(hi, g.limit-utf8.RuneCountInString(b.String())))
		}
	case g.readable && hi > 1:
		// a short word reads best: 4 to 6 characters for + and *
		n = max(lo, min(hi, 4+g.rnd.IntN(3)))
	case g.readable:
		n = max(lo, hi)
	case hi > lo:
		n = lo + g.rnd.IntN(hi-lo+1)
	}
	for range n {
		g.write(b, re, depth+1)
	}
}

// preferred ranges, readable first
var readableRanges = [][2]rune{{'a', 'z'}, {'0', '9'}, {'A', 'Z'}}

// pick returns a rune of a character class given as range pairs. Wide
// classes such as [^x] are limited to printable ASCII.
func (g *gen) pick(ranges []rune) rune {
	if g.highest {
		// the highest printable ASCII character the class allows
		for i := len(ranges) - 2; i >= 0; i -= 2 {
			if lo, hi := max(ranges[i], 0x21), min(ranges[i+1], 0x7e); lo <= hi {
				return hi
			}
		}
		if len(ranges) >= 2 {
			return ranges[len(ranges)-1]
		}
		return 'z'
	}
	if g.readable {
		for _, pref := range readableRanges {
			for i := 0; i+1 < len(ranges); i += 2 {
				lo, hi := max(ranges[i], pref[0]), min(ranges[i+1], pref[1])
				if lo <= hi {
					return lo + rune(g.rnd.IntN(int(hi-lo)+1))
				}
			}
		}
	}
	// any range, preferring printable ASCII
	var printable [][2]rune
	for i := 0; i+1 < len(ranges); i += 2 {
		lo, hi := max(ranges[i], 0x21), min(ranges[i+1], 0x7e)
		if lo <= hi {
			printable = append(printable, [2]rune{lo, hi})
		}
	}
	if len(printable) > 0 {
		r := printable[g.rnd.IntN(len(printable))]
		return r[0] + rune(g.rnd.IntN(int(r[1]-r[0])+1))
	}
	if len(ranges) >= 2 {
		return ranges[0]
	}
	return 'a'
}

// StripLookarounds removes (?=…), (?!…), (?<=…) and (?<!…) groups, with
// nesting, escapes and character classes taken into account.
func StripLookarounds(p string) string {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(p); {
		if !inClass && (strings.HasPrefix(p[i:], "(?=") || strings.HasPrefix(p[i:], "(?!") ||
			strings.HasPrefix(p[i:], "(?<=") || strings.HasPrefix(p[i:], "(?<!")) {
			i = groupEnd(p, i)
			continue
		}
		switch p[i] {
		case '\\':
			b.WriteString(p[i:min(i+2, len(p))])
			i += 2
			continue
		case '[':
			inClass = true
		case ']':
			inClass = false
		}
		b.WriteByte(p[i])
		i++
	}
	return b.String()
}

// groupEnd returns the index after the group that starts at p[start].
func groupEnd(p string, start int) int {
	depth, inClass := 0, false
	for i := start; i < len(p); i++ {
		switch c := p[i]; {
		case c == '\\':
			i++
		case inClass:
			if c == ']' {
				inClass = false
			}
		case c == '[':
			inClass = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(p)
}
