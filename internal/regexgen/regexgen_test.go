package regexgen

import (
	"math/rand/v2"
	"testing"
	"unicode/utf8"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Every generated string must match its original pattern, checked the way
// apitest checks it.
func TestPatterns(t *testing.T) {
	patterns := []string{
		`^\d{4}$`, `^\d{3}$`, `^[a-z]+$`, `^[a-z_]+$`, `^[a-zA-Z]+$`,
		`^\w{3}$`, `^\w{2}|[TXX]$`, `^[BS]|[ITS]\d{7}$`,
		`^(\d{1,})-?(\d{2,3})?-?(\d{3})?-?(\d{4})?`,
		`(?=^.{4,253}$)(^((?!-)[a-zA-Z0-9-]{1,63}(?<!-)\.)+[a-zA-Z]{2,63}$)`,
		`^(?=.*\d)(?=.*[A-Z])(?=.*[a-z]).{8,}$`,
		`^[^\s@]+@[^\s@]+\.[^\s@]+$`,
		`^[A-Z]{2}\d{2}[A-Z0-9]{11,30}$`,
		`^(?:[01]\d|2[0-3]):[0-5]\d$`,
		`^v\d+\.\d+\.\d+(-[a-z]+)?$`,
		`[*?+]`, `^\p{L}+$`, `abc`, `^(red|green|blue)$`,
		`^[^,]{1,5}$`,
	}
	for _, p := range patterns {
		m, err := spec.CompilePattern(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		rnd := rand.New(rand.NewPCG(1, 2))
		s, ok := Generate(p, Limits{Max: -1}, rnd, 60)
		if !ok || !m.MatchString(s) {
			t.Errorf("%s: %q %v", p, s, ok)
		}
	}
}

func TestLimits(t *testing.T) {
	rnd := rand.New(rand.NewPCG(3, 4))
	s, ok := Generate(`^[a-z]+$`, Limits{Min: 6, Max: 8}, rnd, 60)
	if n := utf8.RuneCountInString(s); !ok || n < 6 || n > 8 {
		t.Errorf("length: %q", s)
	}
	// unanchored patterns are padded
	if s, ok := Generate(`\d`, Limits{Min: 5, Max: -1}, rnd, 10); !ok || len(s) < 5 {
		t.Errorf("padding: %q", s)
	}
	if _, ok := Generate(`^\d{4}$`, Limits{Max: 3}, rnd, 20); ok {
		t.Error("an impossible length must fail")
	}
	if _, ok := Generate(`(`, Limits{Max: -1}, rnd, 5); ok {
		t.Error("an invalid pattern must fail")
	}
}

func TestDeterministic(t *testing.T) {
	a, _ := Generate(`^[A-Z]{3}-\d{4}$`, Limits{Max: -1}, rand.New(rand.NewPCG(9, 9)), 30)
	b, _ := Generate(`^[A-Z]{3}-\d{4}$`, Limits{Max: -1}, rand.New(rand.NewPCG(9, 9)), 30)
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
}

func TestStripLookarounds(t *testing.T) {
	for in, want := range map[string]string{
		`^(?=.*\d)[a-z]+$`:    `^[a-z]+$`,
		`(?<!-)\.(?!(a|b))x`:  `\.x`,
		`[(?=]\(?=x`:          `[(?=]\(?=x`,
		`^((?!-)[a-z]{1,3})$`: `^([a-z]{1,3})$`,
	} {
		if got := StripLookarounds(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestHighest(t *testing.T) {
	for _, c := range []struct {
		pattern string
		lim     Limits
		want    string
	}{
		{`^\d{3}$`, Limits{0, -1}, "999"},
		{`^[A-Z]{2}\d{4}$`, Limits{0, -1}, "ZZ9999"},
		{`^(red|green|blue)-[a-f0-9]{2}$`, Limits{0, -1}, "blue-ff"},
		{`^[a-z]+$`, Limits{0, 5}, "zzzzz"},
		{`^[A-Z]{3}\d{2,6}$`, Limits{0, 7}, "ZZZ9999"},
		{`^\d{3}$`, Limits{0, 2}, ""},
		{`^(?=.*\d)[a-z]{3}\d?$`, Limits{0, -1}, "zzz9"},
	} {
		got, ok := Highest(c.pattern, c.lim)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s %v: got %q %v, want %q", c.pattern, c.lim, got, ok, c.want)
		}
	}
}
