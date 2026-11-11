package tree

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// refNeed is a body field of a create that holds the key of a record of
// another resource the test creates (the authorId of a book): without a
// source in order the create sends the key of the stage, which a record
// the test creates in an empty instance does not have.
type refNeed struct {
	field string          // the body field (authorId)
	to    *model.Resource // the resource it refers to (Author)
	key   string          // the field of to it holds (id)
	many  bool            // a list of keys (pilotIds)
	// source is the node below or above the created record that gives it
	// ("bookAuthor.id"), "" when the tree has none
	source string
}

// nodeResource is the resource of a node's request, nil if unknown.
func nodeResource(s *spec.Spec, m *model.Model, n *Node) *model.Resource {
	op := lookupOp(s, n.Call)
	if op == nil {
		return nil
	}
	return resourceOf(m, op)
}

// createdResources are the resources the tree creates records of: their
// records are no seed, an empty instance has none the test does not create.
// A create the scope excludes creates nothing (the check of the scope
// names it).
func createdResources(s *spec.Spec, m *model.Model, t *Tree) map[*model.Resource]bool {
	out := map[*model.Resource]bool{}
	for _, n := range creators(s, m, t, nil) {
		_, op := ownCreate(s, n)
		if r := nodeResource(s, m, n); r != nil {
			out[r] = true
		}
		if mo := m.Op(op); mo != nil && mo.Resource != nil {
			out[mo.Resource] = true
		}
	}
	return out
}

// creators are the nodes with a create the scope allows, of the resource
// to (all with nil).
func creators(s *spec.Spec, m *model.Model, t *Tree, to *model.Resource) []*Node {
	var out []*Node
	t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone {
			return
		}
		_, op := ownCreate(s, n)
		if op == nil || t.Scope.Excluded(op) {
			return
		}
		if to != nil {
			mo := m.Op(op)
			if nodeResource(s, m, n) != to && (mo == nil || mo.Resource != to) {
				return
			}
		}
		out = append(out, n)
	})
	return out
}

// sameKind reports whether records of r are records of to: the same
// resource, or one the API cannot create that shares its key.
func sameKind(p *proposer, r, to *model.Resource, key string) bool {
	return r != nil && (r == to || p.creatable(r, key) == to)
}

// refNeeds are the fields of the create of n that refer to records of
// another resource the tree creates, with the node below or above n that
// gives each.
func refNeeds(s *spec.Spec, m *model.Model, t *Tree, n *Node, w *Write, op *spec.Operation, created map[*model.Resource]bool) []refNeed {
	res := nodeResource(s, m, n)
	if mo := m.Op(op); mo != nil && mo.Resource != nil {
		res = mo.Resource
	}
	if res == nil {
		return nil
	}
	props, _ := properties(schemaOf(requestRef(op)))
	if len(props) == 0 {
		return nil
	}
	p := &proposer{m: m}
	var out []refNeed
	for _, ref := range p.referencesOf(res) {
		// a field of the body itself, or inside an object of it (crew.pilotId)
		_, name, ok := bodyField(props, ref.field)
		if !ok || slices.ContainsFunc(out, func(x refNeed) bool { return x.field == name }) {
			continue
		}
		to := p.creatable(ref.to, ref.key)
		if to == res || !created[to] || w.HasBody && !bodySends(w.Body, name, t.names()) {
			continue
		}
		key := to.Field(ref.key)
		if key == "" {
			key = ref.key
		}
		need := refNeed{field: name, to: to, key: key, many: ref.many}
		need.source = refSource(s, m, t, n, need, ref.field)
		out = append(out, need)
	}
	slices.SortFunc(out, func(a, b refNeed) int { return strings.Compare(a.field, b.field) })
	return out
}

// bodySends reports whether a body given in the file sends a field as a
// value of the stage: as it is, or read from the record itself (.authorId);
// a reference to a node is the user's choice. field is a path for a field
// inside an object (crew.pilotId).
func bodySends(body any, field string, scope func(string) bool) bool {
	m, ok := body.(map[string]any)
	if !ok {
		return false
	}
	v, ok := Field(m, field)
	if !ok {
		return false
	}
	ref, isRef := ParseRef(v, scope)
	return !isRef || ref.Node == ""
}

// refSource is the node that gives a referring field of n: a node below n
// of the resource that reads its record by the field (bookAuthor: GET
// /authors/{authorId} authorId=.authorId), else the nearest node above n
// of that resource; "" when there is none. A node that is off or whose
// request the scope excludes gives nothing.
func refSource(s *spec.Spec, m *model.Model, t *Tree, n *Node, need refNeed, field string) string {
	p := &proposer{m: m}
	for _, c := range n.Each {
		if c.IsRequire || !usable(s, t, c) || !sameKind(p, nodeResource(s, m, c), need.to, need.key) {
			continue
		}
		if readsBy(c, n, field) {
			return c.Name + "." + need.key
		}
	}
	if list, _, inList := strings.Cut(field, "."); need.many && inList {
		// another field of the same list names the same records
		// (crew.pilotId and crew.pilot.pilotId): the node that reads them
		// by it gives them too, the build takes per element the record
		// with its value
		for _, c := range n.Each {
			if c.IsRequire || !usable(s, t, c) || !sameKind(p, nodeResource(s, m, c), need.to, need.key) {
				continue
			}
			if readsByPrefix(c, n, list) {
				return c.Name + "." + need.key
			}
		}
	}
	if need.many {
		return ""
	}
	for a := n.Parent; a != nil; a = a.Parent {
		if usable(s, t, a) && sameKind(p, nodeResource(s, m, a), need.to, need.key) {
			return a.Name + "." + need.key
		}
	}
	return ""
}

// usable reports whether a node is fetched: not off, not gone, and its
// request not excluded by the scope.
func usable(s *spec.Spec, t *Tree, n *Node) bool {
	if !n.Enabled() {
		return false
	}
	op := lookupOp(s, n.Call)
	return op != nil && !t.Scope.Excluded(op)
}

// readsBy reports whether a node c below n reads its records by a field
// of n's record: a path value .field, or a where on n.field (.field).
func readsBy(c, n *Node, field string) bool {
	is := func(v any) bool {
		str, ok := v.(string)
		return ok && (strings.EqualFold(str, "."+field) || strings.EqualFold(str, n.Name+"."+field))
	}
	for _, v := range c.Path {
		if is(v) {
			return true
		}
	}
	for _, cl := range slices.Concat(c.Where, c.WhereAny) {
		if is(cl.Test) {
			return true
		}
	}
	return false
}

// readsByPrefix reports whether c reads its records by a field inside the
// list field of the record of n (.crew.pilotId for crew).
func readsByPrefix(c, n *Node, list string) bool {
	is := func(v any) bool {
		str, ok := v.(string)
		if !ok {
			return false
		}
		str = strings.ToLower(str)
		for _, p := range []string{".", strings.ToLower(n.Name) + "."} {
			if strings.HasPrefix(str, p+strings.ToLower(list)+".") {
				return true
			}
		}
		return false
	}
	for _, v := range c.Path {
		if is(v) {
			return true
		}
	}
	for _, cl := range slices.Concat(c.Where, c.WhereAny) {
		if is(cl.Test) {
			return true
		}
	}
	return false
}

// checkReferences finds the values a test that rebuilds the records in an
// empty instance would take from the stage although the test creates the
// record they name: a body field of a create without a source in order
// (authorId of a book: the author is created with another id), a source
// that reads another resource, and a path value that is a key of the stage
// ({authorId} = 20). Each comes with the change that solves it.
func checkReferences(s *spec.Spec, t *Tree) ([]string, []Fix) {
	if !t.HasOrder {
		return nil, nil
	}
	var out []string
	var fixes []Fix
	fix := func(f Fix, format string, args ...any) {
		out = append(out, fmt.Sprintf("line %d: %s", f.Line, fmt.Sprintf(format, args...))+f.change())
		if f.apply != nil {
			fixes = append(fixes, f)
		}
	}
	m := modelOf(s)
	created := createdResources(s, m, t)
	if len(created) == 0 {
		return nil, nil
	}
	p := &proposer{m: m}
	scope := t.names()
	for _, e := range t.Order {
		n := t.Node(e.Node)
		if e.Step != "" || n == nil || n.IsRequire || n.Gone {
			continue
		}
		w, op := ownCreate(s, n)
		if w == nil || !n.Enabled() {
			// a node that is not fetched has no records below it: its creates
			// rebuild records of other nodes (the pilot below a ship)
			continue
		}
		needs := refNeeds(s, m, t, n, w, op, created)
		for _, field := range sortedKeys(e.Values) {
			// a source on a node that is not fetched, for a field that names
			// no record the test creates: it goes, the create sends the value
			// of the record
			ref, ok := ParseRef(e.Values[field], scope)
			src := t.Node(ref.Node)
			if !ok || src == nil || usable(s, t, src) || slices.ContainsFunc(needs, func(x refNeed) bool { return strings.EqualFold(x.field, field) }) {
				continue
			}
			fix(Fix{Line: e.Line, Change: fmt.Sprintf("remove %s: %v", field, e.Values[field]), apply: setOrderSource(e.Line, field, "")},
				"order: %s.%s: %s is not fetched (off, or excluded by scope): no record of it gives the value", e.Node, field, ref.Node)
		}
		for _, need := range needs {
			what := fmt.Sprintf("the %s of %s %s", need.key, article(lowerFirst(need.to.Name)), "the test creates")
			if field, v, has := foldEntry(e.Values, need.field); has {
				ref, ok := ParseRef(v, scope)
				src := t.Node(ref.Node)
				if !ok || src == nil || !below(src, n) && n.Ancestor(ref.Node) == nil {
					continue // checkOrder names it
				}
				if !usable(s, t, src) {
					why := "is off"
					if op := lookupOp(s, src.Call); op != nil && t.Scope.Excluded(op) {
						why = "is excluded by scope (" + t.Scope.excludedBy(op) + ")"
					}
					f := newSourceFix(s, t, n, e, need)
					if need.source != "" {
						f = Fix{Line: e.Line, Change: fmt.Sprintf("%s: %v → %s: %s", field, v, field, need.source), apply: setOrderSource(e.Line, field, need.source)}
					}
					fix(f, "order: %s.%s holds %s, but its source %s %s: no record of it is fetched%s", e.Node, field, what, ref.Node, why, needWhy(s, m, t, n, w, op, need))
					out[len(out)-1] += orWithout(n, w, op, need)
					continue
				}
				r := nodeResource(s, m, src)
				if r == nil || sameKind(p, r, need.to, need.key) {
					continue
				}
				f := Fix{Line: e.Line, Change: fmt.Sprintf("remove %s: %v, then -fix names the source", field, v), apply: setOrderSource(e.Line, field, "")}
				if need.source != "" {
					f = Fix{Line: e.Line, Change: fmt.Sprintf("%s: %v → %s: %s", field, v, field, need.source), apply: setOrderSource(e.Line, field, need.source)}
				}
				fix(f, "order: %s.%s holds %s, but %s reads %s records", e.Node, field, what, ref.Node, r.Name)
				continue
			}
			msg := fmt.Sprintf("order: %s.%s holds %s, but order gives it no source", e.Node, need.field, what) +
				needWhy(s, m, t, n, w, op, need)
			if need.source != "" {
				fix(Fix{Line: e.Line, Change: fmt.Sprintf("- %s → - %s: {…, %s: %s}", e.Node, e.Node, need.field, need.source), apply: addOrderSource(e.Line, need.field, need.source)}, "%s", msg)
				out[len(out)-1] += orWithout(n, w, op, need)
				continue
			}
			fix(newSourceFix(s, t, n, e, need), "%s", msg)
			out[len(out)-1] += orWithout(n, w, op, need)
		}
	}
	o, f := checkPathKeys(s, m, t, created)
	return append(out, o...), append(fixes, f...)
}

// needWhy says where a referring field comes from: the create that sends
// it and the nodes that create the records it names, which get new ids in
// the empty instance.
func needWhy(s *spec.Spec, m *model.Model, t *Tree, n *Node, w *Write, op *spec.Operation, need refNeed) string {
	sends := "in its body"
	if w.From != "" {
		sends = "in the body from " + w.From
	}
	why := fmt.Sprintf("\n  why: %s.write.create (line %d, %s) sends %s %s", n.Name, w.Line, callOf(op), need.field, sends)
	if bodyRequires(op, need.field) {
		why += " (the spec requires it)"
	}
	var by []string
	for _, c := range creators(s, m, t, need.to) {
		by = append(by, fmt.Sprintf("%s (line %d)", c.Name, c.Line))
	}
	what := lowerFirst(need.to.Name)
	if len(by) > 0 {
		why += fmt.Sprintf("; %s creates the %s records", strings.Join(by, ", "), what)
	}
	return why + fmt.Sprintf(", so in the empty instance the %s of the stage does not exist: a node below %s must read the %s each %s names, and order must name it",
		need.key, n.Name, what, lowerFirst(singular(n.Name)))
}

// orWithout is the other way when the spec does not require the field: a
// create whose body leaves it out needs no source (the node that read it
// can then be off or go).
func orWithout(n *Node, w *Write, op *spec.Operation, need refNeed) string {
	if bodyRequires(op, need.field) {
		return ""
	}
	return fmt.Sprintf("\n  or, when the test needs no %s for %s: change line %d: give %s.write.create a body without %s (sent as written; the spec does not require it)",
		lowerFirst(need.to.Name), n.Name, w.Line, n.Name, need.field)
}

// newSourceFix adds below n the node that reads the record a field names
// and names it as the source in order: the read by its key (bookAuthor: GET
// /authors/{authorId} authorId=.authorId), else a list of the resource with
// a where on the key (as the proposal does); only requests the scope does
// not exclude. The records are fetched again after it.
func newSourceFix(s *spec.Spec, t *Tree, n *Node, e OrderEntry, need refNeed) Fix {
	c, desc := sourceNode(s, t, n, need, nil)
	if c == nil {
		// no read of these records the scope allows: the test cannot take
		// the one a record names; its records become seed (the instance
		// holds them), or the read leaves exclude
		what := lowerFirst(need.to.Name)
		if cs := creators(s, modelOf(s), t, need.to); len(cs) > 0 && len(t.Scope.ExcludeTags)+len(t.Scope.ExcludeOps) > 0 {
			c := cs[0]
			why := "no read of " + what + " records in scope: the instance holds them"
			return Fix{Line: e.Line, Change: fmt.Sprintf("%s.write.create: add off: true (the instance must hold the %s records), or take a read of them out of scope.exclude", c.Name, what),
				apply: setWriteOff(c.Name, WriteCreate, why)}
		}
		return Fix{Line: e.Line, Change: fmt.Sprintf("add below %s a node that reads the %s records by .%s, then name it: - %s: {%s: <node>.%s}",
			n.Name, what, need.field, e.Node, need.field, need.key)}
	}
	source := c.Name + "." + need.key
	return Fix{
		Line: e.Line, adds: c.Name, under: n.Name, fetch: []string{n.Name},
		Change: fmt.Sprintf("add below %s the node %s: %s, and - %s: {…, %s: %s}; then fetch again with -node %s",
			n.Name, c.Name, desc, e.Node, need.field, source, n.Name),
		apply: func(t *Tree) bool {
			return putOrderSource(e.Line, need.field, source)(t) && addChildNode(t, n.Name, c)
		},
	}
}

// sourceNode is the node to add below n that reads the records a field of
// its records names: the read by the key the field holds (by another key
// only when the resource has one), else a list of the resource with a
// where on the key; nil when the scope allows neither. taken are names
// other nodes to be added have.
func sourceNode(s *spec.Spec, t *Tree, n *Node, need refNeed, taken map[string]bool) (*Node, string) {
	excluded := func(op *spec.Operation) bool { return t.Scope.Excluded(op) }
	var read *spec.Operation
	if !need.many {
		for _, mo := range need.to.OpsWith(model.RoleRead) {
			if len(pathParams(mo.Op)) != 1 || excluded(mo.Op) {
				continue
			}
			exact := strings.EqualFold(byField(mo), need.key)
			if exact || read == nil && len(need.to.Keys) <= 1 {
				read = mo.Op
				if exact {
					break
				}
			}
		}
	}
	name := freeNodeName(t, lowerFirst(need.to.Name), lowerFirst(singular(n.Name))+need.to.Name)
	for i := 2; taken[name]; i++ {
		name = freeNodeName(t, fmt.Sprintf("%s%s%d", lowerFirst(singular(n.Name)), need.to.Name, i))
	}
	if taken != nil {
		taken[name] = true
	}
	if read != nil {
		c := &Node{Name: name, Call: callOf(read), Comment: read.ID, Path: map[string]any{pathParams(read)[0]: "." + need.field}}
		short, _ := shortText(c.Call, c.Path)
		return c, short
	}
	var list *spec.Operation
	for _, mo := range need.to.OpsWith(model.RoleList) {
		if len(pathParams(mo.Op)) == 0 && !requiredQuery(mo.Op) && !excluded(mo.Op) && lookupOp(s, callOf(mo.Op)) != nil {
			list = mo.Op
			break
		}
	}
	if list == nil {
		return nil, ""
	}
	c := &Node{Name: name, Call: callOf(list), Comment: list.ID, Key: keyPointers(need.to), Page: pageOf(list),
		Where: []Clause{{Field: need.key, Test: n.Name + "." + need.field, Note: "the " + lowerFirst(need.to.Name) + " " + n.Name + " refers to"}}}
	if need.many {
		c.Pick = Pick{All: true}
	}
	return c, fmt.Sprintf("%s where {%s: %s.%s}", callOf(list), need.key, n.Name, need.field)
}

// checkPathKeys finds path values of nodes and writes that are a key of
// the stage (a value, or a $name with a value in params) where the path
// takes the key of a record the test creates: in the empty instance that
// record has another key. The fix names the node above of that resource.
func checkPathKeys(s *spec.Spec, m *model.Model, t *Tree, created map[*model.Resource]bool) ([]string, []Fix) {
	var out []string
	var fixes []Fix
	saved := map[string]bool{}
	t.Walk(func(n *Node) {
		for _, w := range n.Write {
			for name := range w.Save {
				saved[name] = true
			}
		}
	})
	p := &proposer{m: m}
	scope := t.names()
	check := func(n *Node, where string, line int, call string, path map[string]any, target *yaml.Node, write bool) {
		op := lookupOp(s, call)
		if op == nil {
			return
		}
		mo := m.Op(op)
		if mo == nil {
			return
		}
		for _, param := range sortedKeys(path) {
			v := path[param]
			stage := v
			if name, ok := ParamRef(v); ok {
				val, set := t.param(name)
				if !set || saved[name] {
					continue
				}
				stage = val
			}
			if _, isRef := ParseRef(stage, scope); isRef || len(ComposedRefs(text(stage), scope)) > 0 {
				continue
			}
			if _, isParam := ParamRef(stage); isParam {
				continue // checkRefs names it
			}
			mp := mo.Param(param)
			if mp == nil || mp.Resource == nil {
				continue
			}
			to := p.creatable(mp.Resource, mp.Field)
			if !created[to] {
				continue
			}
			key := mp.Field
			if f := to.Field(key); f != "" {
				key = f
			}
			msg := fmt.Sprintf("%s: {%s} = %v is the %s of %s of the stage, but the test creates the %s records: in the empty instance it has another %s",
				where, param, v, key, article(lowerFirst(to.Name)), lowerFirst(to.Name), key)
			var src string
			start := n.Parent
			if write {
				start = n
			}
			for a := start; a != nil; a = a.Parent {
				if sameKind(p, nodeResource(s, m, a), to, key) {
					src = a.Name + "." + key
					if a == n {
						src = "." + key
					}
					break
				}
			}
			f := Fix{Line: line, Change: fmt.Sprintf("%s=%v → %s=<node>.%s (a node above that reads the %s)", param, v, param, key, lowerFirst(to.Name))}
			if src != "" && target != nil {
				from := text(v)
				f = Fix{Line: line, Change: fmt.Sprintf("%s=%v → %s=%s", param, v, param, src), apply: func(*Tree) bool { return replaceRef(target, from, src) }}
			}
			out = append(out, fmt.Sprintf("line %d: %s", f.Line, msg)+f.change())
			if f.apply != nil {
				fixes = append(fixes, f)
			}
		}
	}
	t.Walk(func(n *Node) {
		if n.IsRequire || !n.Enabled() {
			return
		}
		check(n, n.Name, n.Line, n.Call, n.Path, n.value, false)
		for _, kind := range n.Kinds() {
			w := n.Write[kind]
			if w == nil || w.None || w.Off || w.Gone {
				continue
			}
			check(n, n.Name+".write."+kind, w.Line, w.Call, w.Path, w.value, true)
		}
	})
	return out, fixes
}

// foldEntry is the entry of a map whose key matches name, ignoring case.
func foldEntry(m map[string]any, name string) (string, any, bool) {
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return k, v, true
		}
	}
	return "", nil, false
}

// freeNodeName is the first of the names no node of the tree has, else the
// last with a number.
func freeNodeName(t *Tree, names ...string) string {
	used := map[string]bool{}
	t.Walk(func(n *Node) { used[n.Name] = true })
	for _, name := range names {
		if !used[name] {
			return name
		}
	}
	last := names[len(names)-1]
	for i := 2; ; i++ {
		if c := fmt.Sprintf("%s%d", last, i); !used[c] {
			return c
		}
	}
}

// addOrderSource adds the source of a body field to the line of order at
// line: "- books" becomes "- books: {authorId: bookAuthor.id}".
func addOrderSource(line int, field, value string) func(*Tree) bool {
	return func(t *Tree) bool {
		section, i := orderItem(t, line)
		if section == nil {
			return false
		}
		item := section.Content[i]
		switch item.Kind {
		case yaml.ScalarNode:
			vals := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle, Content: []*yaml.Node{scalar(field), scalar(value)}, LineComment: item.LineComment}
			section.Content[i] = &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar(item.Value), vals}}
			return true
		case yaml.MappingNode:
			if len(item.Content) != 2 {
				return false
			}
			vals := item.Content[1]
			if vals.Kind == yaml.ScalarNode && vals.Tag == "!!null" {
				vals.Kind, vals.Tag, vals.Value, vals.Style = yaml.MappingNode, "", "", yaml.FlowStyle
			}
			if vals.Kind != yaml.MappingNode || valueOf(vals, field) != nil {
				return false
			}
			vals.Content = append(vals.Content, scalar(field), scalar(value))
			return true
		}
		return false
	}
}

// putOrderSource sets the source of a body field in the line of order at
// line, adding it when the line has none for the field.
func putOrderSource(line int, field, value string) func(*Tree) bool {
	return func(t *Tree) bool {
		at := line
		if m, ok := t.moved[line]; ok {
			at = m
		}
		for _, e := range t.Order {
			if _, _, has := foldEntry(e.Values, field); e.Line == at && has {
				return setOrderSource(line, field, value)(t)
			}
		}
		return addOrderSource(line, field, value)(t)
	}
}

// addChildNode adds a node below the node parent.
func addChildNode(t *Tree, parent string, c *Node) bool {
	n := t.Node(parent)
	if n == nil || n.value == nil {
		return false
	}
	v := nodeMapping(n)
	if v == nil {
		return false
	}
	each := valueOf(v, "each")
	switch {
	case each == nil:
		each = &yaml.Node{Kind: yaml.MappingNode}
		v.Content = append(v.Content, scalar("each"), each)
	case each.Kind == yaml.ScalarNode && each.Tag == "!!null":
		each.Kind, each.Tag, each.Value = yaml.MappingNode, "", ""
	}
	if each.Kind != yaml.MappingNode || valueOf(each, c.Name) != nil {
		return false
	}
	each.Content = append(each.Content, scalar(c.Name), nodeYAML(c))
	return true
}

// keyGap notes a body field of r that sends a key of the stage: kr is the
// record the test creates anew with another key, so the empty instance
// lacks the value. When the body is built from the record of a node with a
// line in order, the fix gives the field its source there: a node below
// that reads that record by the field, or one -fix adds (then the records
// are fetched again).
func (b *builder) keyGap(step string, r *Record, what, field string, v any, kr keyRef) {
	msg := fmt.Sprintf("it is the key of %s on the stage; the test creates %s anew with another key, so the empty instance has no %s %s",
		kr.rec.ID, kr.rec.ID, kr.field, text(v))
	var e *Record
	var entry *OrderEntry
	if r != nil {
		e = b.entity(r)
		entry = b.entryOf(e)
	}
	to := b.resourceOf(kr.rec.Node)
	if entry == nil || to == nil || field == "" {
		b.noteAt(LevelError, step, fmt.Sprintf("%s = %s is a key of the stage", what, text(v)), msg+"; give the source in order ("+field+": <node>."+kr.field+")")
		return
	}
	f := b.keySource(e.Node, *entry, field, to, kr.field)
	// one line: the note stands in a table and in the timeline
	b.noteAt(LevelError, step, fmt.Sprintf("%s = %s is a key of the stage", what, text(v)), msg+strings.ReplaceAll(f.change(), "\n  ", "; "))
	k := fmt.Sprintf("%d|%s", entry.Line, strings.ToLower(field))
	if f.apply == nil || b.keyGaps[k] {
		return
	}
	if b.keyGaps == nil {
		b.keyGaps = map[string]bool{}
	}
	b.keyGaps[k] = true
	b.out.Fixes = append(b.out.Fixes, f)
}

// keySource is the change of tree.yaml that gives a body field of the
// records of n the key of the record of resource to it names: the source in
// order when a node below n reads that record by the field, else a node
// added below n and the source (newSourceFix).
func (b *builder) keySource(n *Node, e OrderEntry, field string, to *model.Resource, key string) Fix {
	many := false
	for _, r := range b.d.Of(n) {
		if v, ok := Field(r.Value, field); ok {
			_, many = v.([]any)
			break
		}
	}
	need := refNeed{field: field, to: to, key: to.Field(key), many: many}
	if need.key == "" {
		need.key = key
	}
	if src := refSource(b.s, b.m, b.t, n, need, field); src != "" {
		return Fix{Line: e.Line, Change: fmt.Sprintf("- %s → - %s: {…, %s: %s}", e.Node, e.Node, field, src), apply: putOrderSource(e.Line, field, src)}
	}
	// a node an earlier fix of this build adds for the same records (the
	// other field of the same list element): its source, no second node
	group := n.Name + "|" + to.Name + "|" + field
	if list, _, inList := strings.Cut(field, "."); many && inList {
		group = n.Name + "|" + to.Name + "|" + list + "."
	}
	if added := b.added[group]; added != "" {
		src := added + "." + need.key
		return Fix{Line: e.Line, fetch: []string{n.Name},
			Change: fmt.Sprintf("- %s → - %s: {…, %s: %s} (%s: the node the fix before adds); then fetch again with -node %s", e.Node, e.Node, field, src, added, n.Name),
			apply:  putOrderSource(e.Line, field, src)}
	}
	f := newSourceFix(b.s, b.t, n, e, need)
	if f.adds != "" {
		if b.added == nil {
			b.added = map[string]string{}
		}
		b.added[group] = f.adds
	}
	return f
}
