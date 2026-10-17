package tree

import (
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// glob matches a text against a pattern where * is any text; case does not
// matter.
func glob(pattern, s string) bool {
	if !strings.Contains(pattern, "*") {
		return strings.EqualFold(pattern, s)
	}
	re := "(?i)^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
	ok, err := regexp.MatchString(re, s)
	return err == nil && ok
}

func anyGlob(patterns []string, values ...string) bool {
	for _, p := range patterns {
		for _, v := range values {
			if glob(p, v) {
				return true
			}
		}
	}
	return false
}

// tagsOf are the tags of an operation; an operation without tags has the
// tag "untagged".
func tagsOf(op *spec.Operation) []string {
	if len(op.Tags) == 0 {
		return []string{spec.Untagged}
	}
	return op.Tags
}

// InScope reports whether the scope holds an operation: one of its tags is
// in Tags (or Tags is empty), none is excluded, and neither its
// operationId nor "METHOD /path" is excluded.
func (sc Scope) InScope(op *spec.Operation) bool {
	tags := tagsOf(op)
	if len(sc.Tags) > 0 && !anyGlob(sc.Tags, tags...) {
		return false
	}
	if anyGlob(sc.ExcludeTags, tags...) {
		return false
	}
	return !anyGlob(sc.ExcludeOps, op.ID, callOf(op))
}

// Excluded reports whether exclude names an operation, by a tag or by its
// operationId or "METHOD /path"; tags only narrow what is proposed (a read
// of another tag may stand in the file: deps, or added by hand).
func (sc Scope) Excluded(op *spec.Operation) bool {
	return anyGlob(sc.ExcludeTags, tagsOf(op)...) || anyGlob(sc.ExcludeOps, op.ID, callOf(op))
}

// excludedBy names what excludes an operation: "tag X" or "operation Y".
func (sc Scope) excludedBy(op *spec.Operation) string {
	for _, p := range sc.ExcludeTags {
		for _, tag := range tagsOf(op) {
			if glob(p, tag) {
				return "tag " + tag
			}
		}
	}
	return "operation " + op.ID
}

func inScope(s *spec.Spec, sc Scope) []*spec.Operation {
	var out []*spec.Operation
	for _, op := range s.Ops {
		if sc.InScope(op) {
			out = append(out, op)
		}
	}
	return out
}

// deps are the reads outside the scope that give a value the tree takes
// from params: a list without values of its own whose records hold it
// surely (the resource and its key: {authorId} from GET /authors). An
// excluded tag or operation stays out.
func deps(full *spec.Spec, in []*spec.Operation, t *Tree, sc Scope) []*spec.Operation {
	if len(t.ParamNames) == 0 {
		return nil
	}
	fs := *full
	p := &proposer{s: &fs}
	p.m = model.Detect(&fs, nil)
	var users []*spec.Operation
	for name := range t.Params {
		for _, op := range in {
			if slices.ContainsFunc(p.needs(op), func(n need) bool { return n.name == name }) {
				users = append(users, op)
			}
		}
	}
	var out []*spec.Operation
	for _, op := range full.Ops {
		if op.Method != http.MethodGet || slices.Contains(in, op) || slices.Contains(out, op) || len(p.needs(op)) > 0 || !isList(op) {
			continue
		}
		if !sc.excludedOnly().InScope(op) {
			continue
		}
		r := p.resourceOf(op)
		if r == nil {
			continue
		}
		for _, user := range users {
			for _, nd := range p.needs(user) {
				if _, open := t.Params[nd.name]; !open {
					continue
				}
				if _, score := p.provides(user, nd.name, r); score >= 85 && !slices.Contains(out, op) {
					out = append(out, op)
				}
			}
		}
	}
	return out
}

// excludedOnly is the scope without its tags: what deps may add.
func (sc Scope) excludedOnly() Scope {
	return Scope{ExcludeTags: sc.ExcludeTags, ExcludeOps: sc.ExcludeOps}
}

// AddScope widens the scope of the file: more tags, more exclusions, deps.
// The file keeps everything else; the scope section is written at the top.
func (t *Tree) AddScope(add Scope) []Change {
	sc := t.Scope
	var changes []Change
	join := func(list *[]string, more []string, what string) {
		for _, x := range more {
			if x != "" && !slices.ContainsFunc(*list, func(y string) bool { return strings.EqualFold(x, y) }) {
				*list = append(*list, x)
				changes = append(changes, Change{Where: "scope", What: what + " " + x})
			}
		}
	}
	join(&sc.Tags, add.Tags, "tag")
	join(&sc.ExcludeTags, add.ExcludeTags, "excluded tag")
	join(&sc.ExcludeOps, add.ExcludeOps, "excluded operation")
	if add.Deps && !sc.Deps {
		sc.Deps = true
		changes = append(changes, Change{Where: "scope", What: "deps: true"})
	}
	if len(changes) == 0 {
		return nil
	}
	if t.doc == nil {
		t.Scope = sc
		return changes
	}
	root := t.doc.Root
	v := scopeYAML(sc)
	replaced := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "scope" {
			root.Content[i+1] = v
			replaced = true
		}
	}
	if !replaced {
		key := scalar("scope")
		key.HeadComment = scopeHead
		insertSection(root, key, v, "order")
	}
	if nt, err := fromDoc(t.doc); err == nil {
		*t = *nt
	}
	return changes
}
