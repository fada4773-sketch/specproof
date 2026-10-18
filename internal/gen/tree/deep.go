package tree

import (
	"fmt"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Limits of the search for a reference field in a record: how deep into
// its objects and how many fields at most, so a large record stays fast.
const (
	deepDepth = 6
	deepVisit = 5000
)

// How well a field of the record names a reference field (pilotId), best
// first: the same name deeper (pilotDetails.pilotId), a name that ends
// with it (currentPilotId), the id of an object named after it
// (pilotDetails.id, currentPilot.id).
const (
	tierSame = iota
	tierSuffix
	tierObject
)

// deepHit is a field of the record that names a reference field.
type deepHit struct {
	path string
	val  any
	tier int
}

// refWords are the words of a reference field (pilotId → pilot id) and
// those before the id (pilot); only a name of at least one word before a
// last "id" is one.
func refWords(name string) ([]string, []string, bool) {
	w := words(name)
	if len(w) < 2 || w[len(w)-1] != "id" {
		return nil, nil, false
	}
	return w, w[:len(w)-1], true
}

// deepFind searches a record for the value of a reference field it lacks
// (pilotId): through its objects (no lists), at most deepDepth deep and
// deepVisit fields, in a fixed order. It returns the hits of the best tier
// and whether the search stopped at its limit. A field on top that the
// body takes under its own name (own: coPilotId) names no other field.
func deepFind(in map[string]any, name string, own func(string) bool) ([]deepHit, bool) {
	want, base, ok := refWords(name)
	if !ok || len(in) == 0 {
		return nil, false
	}
	type item struct {
		m     map[string]any
		path  string
		owner []string // the words of the object's own name
		depth int
	}
	var hits []deepHit
	visited := 0
	queue := []item{{m: in}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, k := range sortedKeys(cur.m) {
			if visited++; visited > deepVisit {
				return best(hits), true
			}
			v := cur.m[k]
			path := k
			if cur.path != "" {
				path = cur.path + "." + k
			}
			if sub, isMap := v.(map[string]any); isMap {
				if cur.depth+1 < deepDepth {
					queue = append(queue, item{m: sub, path: path, owner: words(k), depth: cur.depth + 1})
				}
				continue
			}
			if !scalarValue(v) || cur.depth == 0 && own(k) {
				continue
			}
			kw := words(k)
			switch {
			case cur.depth > 0 && slices.Equal(kw, want):
				hits = append(hits, deepHit{path, v, tierSame})
			case len(kw) > len(want) && slices.Equal(kw[len(kw)-len(want):], want):
				hits = append(hits, deepHit{path, v, tierSuffix})
			case cur.depth > 0 && slices.Equal(kw, []string{"id"}) && containsWords(cur.owner, base):
				hits = append(hits, deepHit{path, v, tierObject})
			}
		}
	}
	return best(hits), false
}

// best keeps the hits of the best tier.
func best(hits []deepHit) []deepHit {
	if len(hits) == 0 {
		return nil
	}
	top := hits[0].tier
	for _, h := range hits {
		top = min(top, h.tier)
	}
	return slices.DeleteFunc(slices.Clone(hits), func(h deepHit) bool { return h.tier != top })
}

// containsWords reports whether the words of a name hold part in a row,
// a plural counting as its singular (pilotsInfo holds pilot).
func containsWords(name, part []string) bool {
	if len(part) == 0 || len(name) < len(part) {
		return false
	}
	for i := 0; i+len(part) <= len(name); i++ {
		match := true
		for j, p := range part {
			if w := name[i+j]; w != p && singular(w) != p {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func scalarValue(v any) bool {
	switch v.(type) {
	case nil, map[string]any, []any:
		return false
	}
	return true
}

// deepRef is the value of a reference field a body needs and the record
// has deeper, under another name (pilotId in pilotDetails.pilotId):
// taken when the fields that fit the schema of p name one value, noted
// either way; at is the path of the object the field belongs in.
func (b *builder) deepRef(step string, in map[string]any, name, at string, p *openapi3.Schema, props openapi3.Schemas) (any, bool) {
	switch value.Type(p) {
	case "string", "integer", "number":
	default:
		return nil, false
	}
	hits, cut := deepFind(in, name, func(k string) bool {
		_, ok := propertyFoldName(props, k)
		return ok
	})
	field := strings.TrimPrefix(at+"."+name, ".")
	if cut {
		b.note(step, field+": search stopped", fmt.Sprintf("the record has more than %d fields; give the source in fields: %s: .<path>", deepVisit, field))
	}
	v := spec.NewValidator()
	hits = slices.DeleteFunc(hits, func(h deepHit) bool {
		return len(v.Validate(p, spec.Normalize(h.val), spec.ModeRequest)) > 0
	})
	if len(hits) == 0 {
		return nil, false
	}
	var values []string
	var places []string
	for _, h := range hits {
		if t := text(h.val); !slices.Contains(values, t) {
			values = append(values, t)
		}
		places = append(places, fmt.Sprintf("%s = %s", prefixed(at, h.path), text(h.val)))
	}
	if len(values) > 1 {
		b.note(step, field+": several fields of the record name it, none taken",
			strings.Join(places, ", ")+"; give the one in fields: "+field+": ."+prefixed(at, hits[0].path))
		return nil, false
	}
	why := "the record has no " + name + "; the field that names it"
	if len(hits) > 1 {
		why = fmt.Sprintf("the record has no %s; the %d fields that name it hold the same value (%s)", name, len(hits), strings.Join(places, ", "))
	}
	b.note(step, fmt.Sprintf("%s = %s from %s", field, values[0], prefixed(at, hits[0].path)), why)
	return hits[0].val, true
}

func prefixed(at, path string) string {
	if at == "" {
		return path
	}
	return at + "." + path
}
