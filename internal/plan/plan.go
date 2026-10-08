// Package plan orders the cases of a run (FR-ORDER): resource groups follow
// the dependency graph of their bindings, cases within a group follow their
// rank (create, read, list, update, 4xx, authentication, delete; the
// operations of Options.LastInTag after all but the deletes), and DELETE
// cases of groups that others depend on (or of all groups with DeleteLast)
// run last, in reverse order.
package plan

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Segment is a consecutive part of a group's cases.
type Segment struct {
	Group string
	Cases []*cases.Case
	First bool // first segment of the group: BeforeGroup runs before it
	Last  bool // last segment of the group: AfterGroup runs after it
}

// Plan is the execution order of a run.
type Plan struct {
	Segments []Segment
	// Deps lists the producer cases each case needs a value from.
	Deps map[*cases.Case][]*cases.Case
	// Precondition marks cases that are only run because a selected case
	// needs their bindings (FR-GO-02).
	Precondition map[*cases.Case]bool
	Groups       []string // group order
}

// Cases returns all planned cases in execution order.
func (p *Plan) Cases() []*cases.Case {
	var out []*cases.Case
	for _, s := range p.Segments {
		out = append(out, s.Cases...)
	}
	return out
}

// Options influence the order.
type Options struct {
	// Tags is Config.Tags: the order of the groups where bindings allow it.
	Tags []string
	// DeleteLast runs the DELETE cases of every group after all other
	// cases, in reverse group order, instead of only for groups that others
	// depend on.
	DeleteLast bool
	// LastInTag are operations (operationId or "METHOD /path") whose cases
	// run after all other cases of their group, whatever their method, in
	// the order listed; only the DELETE cases of the group follow them.
	LastInTag []string
}

// CheckLastInTag reports operations of LastInTag the spec does not have.
func CheckLastInTag(s *spec.Spec, ids []string) error {
	for _, id := range ids {
		if s.Op(id) == nil {
			return fmt.Errorf("operation %q from LastInTag does not exist in the spec (expected: operationId or \"METHOD /path\")", id)
		}
	}
	return nil
}

// lastIndex is the position of a case's operation in LastInTag, or -1.
func lastIndex(c *cases.Case, last []string) int {
	return slices.IndexFunc(last, c.Op.Is)
}

// Build plans the cases. all must contain every case of the spec in the
// order of cases.Build; selected tells which of them the user asked for.
// Unselected cases are only kept if a selected case depends on them.
func Build(all []*cases.Case, selected func(*cases.Case) bool, set *bind.Set, opt Options) (*Plan, error) {
	tags := opt.Tags
	p := &Plan{Deps: map[*cases.Case][]*cases.Case{}, Precondition: map[*cases.Case]bool{}}

	producers := map[string][]*cases.Case{} // op ID -> positive cases
	for _, c := range all {
		if c.Kind == cases.Positive {
			producers[c.Op.ID] = append(producers[c.Op.ID], c)
		}
	}
	for _, c := range all {
		seen := map[*cases.Case]bool{}
		for _, b := range set.Of(c.Op) {
			for _, pc := range producers[b.Producer.ID] {
				if pc != c && !seen[pc] {
					seen[pc] = true
					p.Deps[c] = append(p.Deps[c], pc)
				}
			}
		}
	}

	// Keep the selected cases and everything they need.
	keep := map[*cases.Case]bool{}
	var visit func(c *cases.Case)
	visit = func(c *cases.Case) {
		if keep[c] {
			return
		}
		keep[c] = true
		for _, d := range p.Deps[c] {
			visit(d)
		}
	}
	for _, c := range all {
		if selected(c) {
			visit(c)
		}
	}
	var kept []*cases.Case
	for _, c := range all {
		if keep[c] {
			kept = append(kept, c)
			if !selected(c) {
				p.Precondition[c] = true
			}
		}
	}

	groups, err := orderGroups(kept, p.Deps, tags)
	if err != nil {
		return nil, err
	}
	p.Groups = groups

	byGroup := map[string][]*cases.Case{}
	for _, c := range kept {
		byGroup[c.Group] = append(byGroup[c.Group], c)
	}
	hasDependents := map[string]bool{}
	for c, deps := range p.Deps {
		if !keep[c] {
			continue
		}
		for _, d := range deps {
			if d.Group != c.Group {
				hasDependents[d.Group] = true
			}
		}
	}

	var deferred []Segment
	for _, g := range groups {
		ordered, err := orderWithin(byGroup[g], p.Deps, opt.LastInTag)
		if err != nil {
			return nil, err
		}
		var main, deletes []*cases.Case
		for _, c := range ordered {
			if c.Rank == cases.RankDelete {
				deletes = append(deletes, c)
			} else {
				main = append(main, c)
			}
		}
		if (hasDependents[g] || opt.DeleteLast) && len(deletes) > 0 {
			p.Segments = append(p.Segments, Segment{Group: g, Cases: main, First: true})
			deferred = append(deferred, Segment{Group: g, Cases: deletes, Last: true})
			continue
		}
		p.Segments = append(p.Segments, Segment{Group: g, Cases: append(main, deletes...), First: true, Last: true})
	}
	for i := len(deferred) - 1; i >= 0; i-- {
		p.Segments = append(p.Segments, deferred[i])
	}
	return p, nil
}

// orderGroups sorts groups topologically by their bindings (FR-ORDER-01).
// Ties are broken by the position in tags, then alphabetically. A cycle or a
// tags order that contradicts the graph is an error (FR-ORDER-02, -05).
func orderGroups(kept []*cases.Case, deps map[*cases.Case][]*cases.Case, tags []string) ([]string, error) {
	var names []string
	edges := map[string]map[string]bool{} // producer group -> consumer groups
	for _, c := range kept {
		if !slices.Contains(names, c.Group) {
			names = append(names, c.Group)
		}
		for _, d := range deps[c] {
			if d.Group == c.Group {
				continue
			}
			if edges[d.Group] == nil {
				edges[d.Group] = map[string]bool{}
			}
			edges[d.Group][c.Group] = true
		}
	}
	less := func(a, b string) bool {
		ia, ib := slices.Index(tags, a), slices.Index(tags, b)
		switch {
		case ia >= 0 && ib >= 0:
			return ia < ib
		case ia >= 0 || ib >= 0:
			return ia >= 0
		}
		return a < b
	}
	sort.Slice(names, func(i, j int) bool { return less(names[i], names[j]) })

	for _, from := range sortedKeys(edges) {
		for _, to := range sortedKeys(edges[from]) {
			ia, ib := slices.Index(tags, from), slices.Index(tags, to)
			if ia >= 0 && ib >= 0 && ia > ib {
				return nil, fmt.Errorf("Config.Tags lists %q before %q, but %q depends on %q through bindings; change the order in Config.Tags", to, from, to, from)
			}
		}
	}
	if cycle := findCycle(names, edges); cycle != nil {
		return nil, fmt.Errorf("cyclic dependency between resource groups: %s; break the cycle with x-apitest-bind or Config.Params", strings.Join(cycle, " → "))
	}

	indeg := map[string]int{}
	for _, from := range names {
		for to := range edges[from] {
			indeg[to]++
		}
	}
	var out []string
	done := map[string]bool{}
	for len(out) < len(names) {
		for _, n := range names {
			if done[n] || indeg[n] > 0 {
				continue
			}
			done[n] = true
			out = append(out, n)
			for to := range edges[n] {
				indeg[to]--
			}
			break
		}
	}
	return out, nil
}

// findCycle returns a cycle like [A B A], or nil.
func findCycle(names []string, edges map[string]map[string]bool) []string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var cycle []string
	var dfs func(n string) bool
	dfs = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		for _, to := range sortedKeys(edges[n]) {
			switch color[to] {
			case grey:
				i := slices.Index(stack, to)
				cycle = append(append([]string(nil), stack[i:]...), to)
				return true
			case white:
				if dfs(to) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	for _, n := range names {
		if color[n] == white && dfs(n) {
			return cycle
		}
	}
	return nil
}

// orderWithin keeps the order of the ranks, refined by
// x-apitest-order, puts the operations of last after all but the DELETE
// cases, and moves producers in front of their consumers.
func orderWithin(list []*cases.Case, deps map[*cases.Case][]*cases.Case, last []string) ([]*cases.Case, error) {
	idx := map[*cases.Case]int{}
	for i, c := range list {
		idx[c] = i
	}
	// class: 0 regular, 1 listed in last, 2 DELETE
	class := func(c *cases.Case) int {
		switch {
		case c.Rank == cases.RankDelete:
			return 2
		case lastIndex(c, last) >= 0:
			return 1
		}
		return 0
	}
	sorted := append([]*cases.Case(nil), list...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if ca, cb := class(a), class(b); ca != cb {
			return ca < cb
		}
		if la, lb := lastIndex(a, last), lastIndex(b, last); la != lb {
			return la < lb // also a listed DELETE after the other DELETEs
		}
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return idx[a] < idx[b]
	})
	pos := map[*cases.Case]int{}
	for i, c := range sorted {
		pos[c] = i
	}
	pending := map[*cases.Case]int{}
	for _, c := range sorted {
		for _, d := range deps[c] {
			if _, same := pos[d]; same {
				pending[c]++
			}
		}
	}
	out := make([]*cases.Case, 0, len(sorted))
	done := map[*cases.Case]bool{}
	for len(out) < len(sorted) {
		progressed := false
		for _, c := range sorted {
			if done[c] || pending[c] > 0 {
				continue
			}
			done[c] = true
			out = append(out, c)
			for _, other := range sorted {
				if done[other] {
					continue
				}
				for _, d := range deps[other] {
					if d == c {
						pending[other]--
					}
				}
			}
			progressed = true
			break
		}
		if !progressed {
			var names []string
			for _, c := range sorted {
				if !done[c] {
					names = append(names, c.Name)
				}
			}
			return nil, fmt.Errorf("cyclic dependency between cases: %s", strings.Join(names, ", "))
		}
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
