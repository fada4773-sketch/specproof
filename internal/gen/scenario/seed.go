package scenario

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/gen/model"
)

// A "seed" of a "$snapshot" entry keeps fields of the chosen elements, one
// set per record. The "from" of a later entry uses them as placeholders:
// {code} takes the "code" seeded last, {Book.code} the one of Book. The
// request is sent once per set, and the elements of all answers are
// searched together.

// seedSets are the seeded values of one resource.
type seedSets struct {
	resource string
	names    []string         // as written in "seed"
	sets     []map[string]any // per record: lower-case name → value
}

// keepSeeds stores the seed fields of the chosen elements.
func (b *builder) keepSeeds(r *model.Resource, c *checks, chosen []any) {
	if len(c.seeds) == 0 {
		return
	}
	s := seedSets{resource: r.Name}
	for _, f := range c.seeds {
		s.names = append(s.names, f.name)
	}
	var shown []string
	for i, item := range chosen {
		set := map[string]any{}
		var parts []string
		for _, f := range c.seeds {
			v := valueAt(item, c.ref, f.segs)
			set[strings.ToLower(f.name)] = v
			parts = append(parts, fmt.Sprintf("%s=%s", f.name, text(v)))
		}
		s.sets = append(s.sets, set)
		shown = append(shown, fmt.Sprintf("#%d %s", i+1, strings.Join(parts, " ")))
	}
	b.seeds = append(b.seeds, s)
	b.res.note(CodeSeed, r.Name, "%d seed sets for the requests below: %s", len(s.sets), strings.Join(shown, "; "))
}

// seedValue looks up a placeholder: "Book.code" in the seeds of Book,
// "code" in the seeds of the resource seeded last. n is the number of sets
// it has; i selects one (the last if there are fewer).
func (b *builder) seedValue(name string, i int) (v any, n int) {
	key := strings.ToLower(strings.TrimSpace(name))
	if res, field, ok := strings.Cut(key, "."); ok {
		for j := len(b.seeds) - 1; j >= 0; j-- {
			s := b.seeds[j]
			if strings.EqualFold(s.resource, res) {
				if v, n := s.value(field, i); n > 0 {
					return v, n
				}
			}
		}
	}
	for j := len(b.seeds) - 1; j >= 0; j-- {
		if v, n := b.seeds[j].value(key, i); n > 0 {
			return v, n
		}
	}
	return nil, 0
}

func (s seedSets) value(key string, i int) (any, int) {
	if len(s.sets) == 0 {
		return nil, 0
	}
	if _, ok := s.sets[0][key]; !ok {
		return nil, 0
	}
	return s.sets[min(i, len(s.sets)-1)][key], len(s.sets)
}

// requests are the requests of an entry of "$snapshot": one per seed set
// the placeholders use, without duplicates. missing names a placeholder
// without value.
func (b *builder) requests(r *model.Resource, src source) ([]string, string) {
	if !src.explicit {
		// a source of its own (a list below a parent) takes no seeds: its
		// placeholders are the keys of the parent records
		p, missing := b.target(src.op, b.keyRecord(r))
		if missing != "" {
			return nil, missing
		}
		return []string{p}, ""
	}
	raw := src.url
	if raw == "" {
		raw = Template(src.op)
	}
	sets := 1
	for _, name := range placeholders(raw) {
		if _, n := b.seedValue(name, 0); n > sets {
			sets = n
		}
	}
	var out []string
	for i := range sets {
		p, missing := b.fillURL(src.op, raw, b.keyRecord(r), func(name string) any {
			v, _ := b.seedValue(name, i)
			return v
		})
		if missing != "" {
			return nil, missing
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out, ""
}

// placeholders lists the {names} of a request.
func placeholders(raw string) []string {
	var out []string
	for {
		start := strings.Index(raw, "{")
		if start < 0 {
			return out
		}
		end := strings.Index(raw[start:], "}")
		if end < 0 {
			return out
		}
		out = append(out, raw[start+1:start+end])
		raw = raw[start+end+1:]
	}
}
