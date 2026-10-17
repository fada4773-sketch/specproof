package tree

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/exec"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Note is something the review lists: a decision of the builder or a gap.
type Note struct {
	Step string
	What string
	Why  string
}

// Built is the scenario built from the records of a fetch.
type Built struct {
	Scenario *scenario.Scenario
	Notes    []Note
	// Seeds are records the scenario reads but does not create: the
	// instance must hold them (its seed).
	Seeds []*Record
	// Created are the records the scenario creates.
	Created []*Record
}

// uniqueNames are fields that usually must be unique per record.
var uniqueNames = []string{"isbn", "email", "code", "slug", "sku", "username", "login", "externalid", "iban", "serialnumber"}

type keyRef struct {
	rec   *Record
	field string
}

type builder struct {
	s       *spec.Spec
	t       *Tree
	d       *Data
	m       *model.Model
	schemes openapi3.SecuritySchemes
	out     *Built

	canon    map[*Record]*Record // record → the record that stands for it
	created  map[*Record]bool
	keyVar   map[*Record]map[string]string
	byValue  map[string][]keyRef
	etag     map[*Record]string
	createID map[*Record]string
	readID   map[*Record]string
	deps     map[*Record][]*Record
	ids      map[string]int
	sent     map[string]bool
	steps    []*scenario.Step
	scope    func(string) bool
	// writeSaved are the names writes save ($receiptNo reads them in the
	// scenario); stage holds the value of params for each, used when no
	// write that saves it runs.
	writeSaved map[string]bool
	stage      map[string]any
	// strict: the file has an order; the test follows it and the sources it
	// gives, problems are what the records do not allow (the build stops)
	strict   bool
	problems []string
	// gaps are the sources of order without a record, per line and field:
	// one problem names all records that lack one
	gaps  map[string]*sourceGap
	fixes []Fix
	// slot is the line of order the steps being added belong to (ships,
	// ships.read, ships.update); slotOf keeps it per step. changes are the
	// writes that change a record, reader the reads that check one.
	slot    string
	slotOf  map[*scenario.Step]string
	changes map[*scenario.Step]*Record
	reader  map[*scenario.Step]*Record
}

func (b *builder) problem(format string, args ...any) {
	p := fmt.Sprintf(format, args...)
	if !slices.Contains(b.problems, p) {
		b.problems = append(b.problems, p)
	}
}

// Build turns the records into the scenario that rebuilds them in an empty
// instance: the creates (records a create refers to first), the reads and
// lists with checks, an update and a patch per node, the 401 steps, and the
// deletes in reverse order.
func Build(s *spec.Spec, t *Tree, d *Data) (*Built, error) {
	if problems, fixes := check(s, t); len(problems) > 0 {
		return nil, &FindingError{Problems: problems, Fixes: fixes}
	}
	b := &builder{
		s: s, t: t, d: d, m: model.Detect(s, nil), out: &Built{},
		canon: map[*Record]*Record{}, created: map[*Record]bool{}, keyVar: map[*Record]map[string]string{},
		byValue: map[string][]keyRef{}, etag: map[*Record]string{}, createID: map[*Record]string{},
		readID: map[*Record]string{}, deps: map[*Record][]*Record{}, ids: map[string]int{}, sent: map[string]bool{},
		scope: t.names(), writeSaved: map[string]bool{}, stage: map[string]any{}, strict: t.Strict(),
		slotOf: map[*scenario.Step]string{}, changes: map[*scenario.Step]*Record{}, reader: map[*scenario.Step]*Record{},
	}
	if s.Doc.Components != nil {
		b.schemes = s.Doc.Components.SecuritySchemes
	}
	t.Walk(func(n *Node) {
		if n.IsRequire || !n.Enabled() {
			return
		}
		for _, w := range n.Write {
			if w.None || w.Off || w.Gone {
				continue
			}
			for name := range w.Save {
				b.writeSaved[name] = true
			}
		}
	})
	b.entities()
	order := b.creates()
	b.reads()
	b.updates()
	b.arrange()
	b.unauthorized()
	b.deletes(order)
	b.afterSaves()
	b.sameCreates()
	if len(b.problems) > 0 {
		return nil, &FindingError{Problems: b.problems, Fixes: b.fixes}
	}
	sc := &scenario.Scenario{Steps: b.steps}
	for _, r := range order {
		if !slices.Contains(sc.Order, r.Node.Name) {
			sc.Order = append(sc.Order, r.Node.Name)
		}
		refs := map[string]string{}
		for _, dep := range b.deps[r] {
			refs[dep.Node.Name] = dep.ID
		}
		if len(refs) == 0 {
			refs = nil
		}
		sc.Records = append(sc.Records, scenario.Record{Name: r.ID, Resource: r.Node.Name, Refs: refs})
	}
	if err := sc.Check(); err != nil {
		return nil, fmt.Errorf("built scenario: %w", err)
	}
	b.out.Scenario = sc
	b.out.Created = order
	return b.out, nil
}

func (b *builder) note(step, what, why string) {
	b.out.Notes = append(b.out.Notes, Note{Step: step, What: what, Why: why})
}

func (b *builder) id(base string) string {
	base = idChars.Replace(base)
	b.ids[base]++
	if n := b.ids[base]; n > 1 {
		return base + "." + strconv.Itoa(n)
	}
	return base
}

var idChars = strings.NewReplacer("/", "_", " ", "_", "{", "", "}", "")

// createOf is the create of a node, nil if it has none. A node without a
// write block takes the create of another node of the same resource (the
// author below a book creates like the list of authors), if the create
// names no node above it that the node lacks.
func (b *builder) createOf(n *Node) (*Write, *spec.Operation) {
	owner := createOwner(b.s, b.m, b.t, n)
	if owner == nil {
		return nil, nil
	}
	return ownCreate(b.s, owner)
}

// ownerName is the node of order a record is created by.
func ownerName(b *builder, r *Record) string {
	if o := createOwner(b.s, b.m, b.t, r.Node); o != nil {
		return o.Name
	}
	return r.Node.Name
}

// entryOf is the order entry of the create of a record's node, nil without.
func (b *builder) entryOf(r *Record) *OrderEntry {
	if !b.strict {
		return nil
	}
	owner := createOwner(b.s, b.m, b.t, r.Node)
	if owner == nil {
		return nil
	}
	return b.t.Entry(owner.Name)
}

// orderIndex is the place of a record's create in order; records without
// one come last.
func (b *builder) orderIndex(n *Node) int {
	owner := createOwner(b.s, b.m, b.t, n)
	if owner == nil {
		return len(b.t.Order)
	}
	if i := b.t.OrderIndex(owner.Name); i >= 0 {
		return i
	}
	return len(b.t.Order)
}

// sources sets the fields of a body that order names to the values of the
// records it names: below the record (the pilot read below a ship), else
// above it; the key of a record the test creates as its placeholder. A
// record without such a record is a problem: the build stops.
func (b *builder) sources(r *Record, body map[string]any, props openapi3.Schemas) {
	e := b.entryOf(b.entity(r))
	if e == nil {
		return
	}
	for _, field := range sortedKeys(e.Values) {
		name := field
		if len(props) > 0 {
			k, ok := propertyFoldName(props, field)
			if !ok {
				continue // an update without the field
			}
			name = k
		}
		vals, ok := b.sourceOf(r, e, field)
		if !ok {
			continue
		}
		if p := props[name]; p != nil && p.Value != nil && value.Type(p.Value) == "array" {
			body[name] = vals
		} else {
			body[name] = vals[0]
		}
	}
}

// sourceOf are the values order gives a field of a record's body: of the
// records of the source node below r (or of its entity), else above it.
// A record without them is a problem.
func (b *builder) sourceOf(r *Record, e *OrderEntry, field string) ([]any, bool) {
	ref, ok := ParseRef(e.Values[field], b.scope)
	if !ok || ref.Node == "" {
		return nil, false
	}
	var from []*Record
	for _, under := range []*Record{r, b.entity(r)} {
		for _, c := range b.d.Records {
			if c.Parent == under && c.Node.Name == ref.Node {
				from = append(from, c)
			}
		}
		if len(from) > 0 {
			break
		}
	}
	if len(from) == 0 {
		if a := r.Ancestor(ref.Node); a != nil {
			from = []*Record{a}
		}
	}
	if len(from) == 0 {
		if v, found := b.entity(r).Get(field); !found || empty(v) {
			// the record names no such record: nothing to take
			return nil, false
		}
		b.noSource(r, e, field, ref)
		return nil, false
	}
	var vals []any
	for _, c := range from {
		v, found := c.Get(ref.Field)
		if !found {
			msg := fmt.Sprintf("line %d: order: %s: {%s: %s}: %s has no field %s", e.Line, e.Node, field, ref, c.ID, ref.Field)
			f := Fix{Line: e.Line, Change: fmt.Sprintf("%s: %s → %s: %s.<one of %s>", field, ref, field, ref.Node, strings.Join(fieldNames(c), ", "))}
			b.problem("%s", msg+f.change())
			continue
		}
		ce := b.entity(c)
		if name := b.keyVar[ce][ref.Field]; b.created[ce] && name != "" {
			v = "{{" + name + "}}"
		}
		vals = append(vals, v)
	}
	return vals, len(vals) > 0
}

// sourceGap is a source of order that records lack: the problem it is,
// its fix and the records.
type sourceGap struct {
	at, fix int
	ids     []string
}

// noSource notes a source of order without a record for r, once per line
// and field with all records that lack one.
func (b *builder) noSource(r *Record, e *OrderEntry, field string, ref Ref) {
	if b.gaps == nil {
		b.gaps = map[string]*sourceGap{}
	}
	k := fmt.Sprintf("%d|%s", e.Line, field)
	g := b.gaps[k]
	if g == nil {
		g = &sourceGap{at: len(b.problems), fix: -1}
		b.gaps[k] = g
		b.problems = append(b.problems, "")
	}
	if slices.Contains(g.ids, r.ID) {
		return
	}
	g.ids = append(g.ids, r.ID)
	msg, f := b.noSourceRecord(g.ids, e, field, ref)
	b.problems[g.at] = msg
	if f.apply != nil {
		if g.fix < 0 {
			g.fix = len(b.fixes)
			b.fixes = append(b.fixes, f)
		}
		b.fixes[g.fix] = f
	}
}

// noSourceRecord says in short what a source of order without a record for
// the records ids needs: the change of tree.yaml, and the fetch after it.
func (b *builder) noSourceRecord(ids []string, e *OrderEntry, field string, ref Ref) (string, Fix) {
	src, at := b.t.Node(ref.Node), b.t.Node(e.Node)
	where := "below or above"
	var conds []string // the fields of the record the source reads with (.pilotId)
	if src != nil && at != nil {
		if below(src, at) {
			where = "below"
		} else if at.Ancestor(ref.Node) != nil {
			where = "above"
		}
		if src.Parent == at {
			for _, k := range sortedKeys(src.Path) {
				if x, ok := ParseRef(src.Path[k], nil); ok && x.Field != "" && !strings.Contains(x.Field, ".") {
					conds = append(conds, x.Field)
				}
			}
		}
	}
	who, them := ids[0]+" has", "it"
	if len(ids) > 1 {
		who, them = strings.Join(ids, ", ")+" have", "them"
	}
	none := !slices.ContainsFunc(b.d.Records, func(c *Record) bool { return c.Node.Name == ref.Node })
	msg := fmt.Sprintf("line %d: order: %s: {%s: %s}: %s no record of %s %s %s", e.Line, e.Node, field, ref, who, ref.Node, where, them)
	var f Fix
	switch {
	case none:
		msg += fmt.Sprintf(" (the data holds no record of %s at all)", ref.Node)
		f = Fix{Change: fmt.Sprintf("fetch again (-base-url …) and read the notes of %s", ref.Node)}
	case where == "below" && len(conds) == 1 && at.value != nil:
		msg += " (the fetch took none under " + them + ": see its notes)"
		f = Fix{Line: at.Line, Change: fmt.Sprintf("%s: add where: {%s: present}, then fetch again with -node %s", at.Name, conds[0], at.Name),
			apply: addWhere(at.Name, conds[0], ref.Node+" reads it")}
	case where == "below":
		msg += " (the fetch took none under " + them + ": see its notes)"
		f = Fix{Line: at.Line, Change: fmt.Sprintf("%s: require %s with min: 1, or remove {%s: …} in line %d", at.Name, ref.Node, field, e.Line)}
	default:
		f = Fix{Line: e.Line, Change: fmt.Sprintf("remove {%s: …}, or fetch a record of %s for %s", field, ref.Node, them)}
	}
	return msg + f.change(), f
}

// sentValue is the value the create of a record sent in a field when order
// gives its source (the pilotId of a ship: the pilot's placeholder).
func (b *builder) sentValue(r *Record, field string) (any, bool) {
	e := b.entity(r)
	if !b.created[e] {
		return nil, false
	}
	entry := b.entryOf(e)
	if entry == nil {
		return nil, false
	}
	// a path value of the create named like the field (the moonId of a
	// garden created at /moons/{moonId}/gardens)
	if w, _ := b.createOf(e.Node); w != nil {
		for p, v := range w.Path {
			if strings.EqualFold(p, field) {
				if x := b.resolve(v, e, e.Node, p, "", e); x != nil {
					return x, true
				}
			}
		}
	}
	for f := range entry.Values {
		if strings.EqualFold(f, field) {
			if vals, ok := b.sourceOf(e, entry, f); ok {
				if len(vals) == 1 {
					return vals[0], true
				}
				return vals, true
			}
		}
	}
	return nil, false
}

func propertyFoldName(props openapi3.Schemas, name string) (string, bool) {
	for k := range props {
		if strings.EqualFold(k, name) {
			return k, true
		}
	}
	return "", false
}

func (b *builder) writeOf(n *Node, kind string) (*Write, *spec.Operation) {
	w := n.Write[kind]
	if w == nil || w.None || w.Off || w.Gone {
		return nil, nil
	}
	op := lookupOp(b.s, w.Call)
	if op == nil {
		return nil, nil
	}
	return w, op
}

// creatable reports whether the tree creates records of the node's
// resource somewhere: then a record it does not rebuild is no seed.
func (b *builder) creatable(n *Node) bool {
	res := b.resourceOf(n)
	found := false
	b.t.Walk(func(o *Node) {
		if found || o.IsRequire {
			return
		}
		if w, _ := b.writeOf(o, WriteCreate); w != nil && (o == n || res != nil && b.resourceOf(o) == res) {
			found = true
		}
	})
	return found
}

// resourceOf is the model resource of a node's request, nil if unknown.
func (b *builder) resourceOf(n *Node) *model.Resource {
	op := lookupOp(b.s, n.Call)
	if op == nil {
		return nil
	}
	return resourceOf(b.m, op)
}

// isView reports whether a record is another answer about the record of
// its parent (GET /docks/{id} under a list of docks): same resource, else
// same key.
func (b *builder) isView(r *Record) bool {
	if r.Parent == nil {
		return false
	}
	if w, _ := b.writeOf(r.Node, WriteCreate); w != nil {
		return false
	}
	ra, rb := b.resourceOf(r.Node), b.resourceOf(r.Parent.Node)
	if ra != nil && rb != nil {
		return ra == rb
	}
	pk := keyOf(r.Parent.Node)
	k := KeyText(r.Value, pk)
	return k != "" && k == KeyText(r.Parent.Value, pk)
}

// entity is the record a record stands for: its parent's for a view, the
// first record of the same kind and key for a record fetched twice.
func (b *builder) entity(r *Record) *Record {
	for r != nil && b.isView(r) {
		r = r.Parent
	}
	if c := b.canon[r]; c != nil {
		return c
	}
	return r
}

// sameCreates notes creates that send the same request and body as an
// earlier one: two records of the stage with the same values (a field the
// API keeps unique fails then), or one record read by nodes whose keys do
// not match.
func (b *builder) sameCreates() {
	seen := map[string]string{}
	for _, st := range b.steps {
		if !strings.HasSuffix(st.ID, "."+WriteCreate) {
			continue
		}
		body, _ := json.Marshal(st.Body)
		path, _ := json.Marshal(st.Path)
		k := st.Call + " " + string(path) + " " + string(body)
		if first, dup := seen[k]; dup {
			b.note(st.ID, "same request and body as "+first,
				"two records of the stage with the same values, or one record read by two nodes whose key lists share no field; check the key of their nodes, or pick other records")
			continue
		}
		seen[k] = st.ID
	}
}

// sameRecords groups the records that are one record of the instance: of
// one kind (resource, else node), equal in the keys both their nodes name.
// Nodes of one resource may name different keys ([/id] and [/id, /isbn]):
// the keys they share decide, so one book read by both is created once.
func (b *builder) sameRecords() (map[string][]*Record, []string) {
	type rec struct {
		r    *Record
		kind string
	}
	var list []rec
	keysOf := map[string][][]string{} // per kind the key lists of its nodes
	for _, r := range b.d.Records {
		if r.Node.IsRequire || b.isView(r) || KeyText(r.Value, keyOf(r.Node)) == "" {
			continue
		}
		kind := r.Node.Name
		if res := b.resourceOf(r.Node); res != nil {
			kind = res.Name
		}
		list = append(list, rec{r, kind})
		k := keyOf(r.Node)
		if !slices.ContainsFunc(keysOf[kind], func(x []string) bool { return slices.Equal(x, k) }) {
			keysOf[kind] = append(keysOf[kind], k)
		}
	}
	// union of the records that agree in a shared key list
	parent := make([]int, len(list))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	first := map[string]int{}
	for i, x := range list {
		own := keyOf(x.r.Node)
		for _, other := range keysOf[x.kind] {
			shared := slices.DeleteFunc(slices.Clone(own), func(p string) bool { return !slices.Contains(other, p) })
			text := KeyText(x.r.Value, shared)
			if len(shared) == 0 || text == "" {
				continue
			}
			k := x.kind + "\x00" + strings.Join(shared, ",") + "\x00" + text
			if j, ok := first[k]; ok {
				parent[find(i)] = find(j)
			} else {
				first[k] = i
			}
		}
	}
	groups := map[string][]*Record{}
	var order []string
	for i, x := range list {
		k := fmt.Sprint(find(i))
		if groups[k] == nil {
			order = append(order, k)
		}
		groups[k] = append(groups[k], x.r)
	}
	// order: the groups as their first records come
	return groups, order
}

// entities finds the records that stand for others and the records the
// scenario creates, with the names of their keys.
func (b *builder) entities() {
	// per kind and key the records that are the same; the first of a node
	// that can create stands for them (a garden found by a search stands
	// behind the garden of its moon), else the first
	groups, order := b.sameRecords()
	// several nodes with a create of their own hold the same records (the
	// beds of a garden by id and by code): they take turns, so every
	// create runs; else a node that borrows a create, else the first
	uses := map[*Node]int{}
	own := func(n *Node) bool { w, _ := b.writeOf(n, WriteCreate); return w != nil }
	for _, k := range order {
		g := groups[k]
		head := g[0]
		var owners []*Record
		for _, r := range g {
			if own(r.Node) && !slices.ContainsFunc(owners, func(o *Record) bool { return o.Node == r.Node }) {
				owners = append(owners, r)
			}
		}
		if len(owners) > 0 {
			head = owners[0]
			for _, r := range owners[1:] {
				if uses[r.Node] < uses[head.Node] {
					head = r
				}
			}
			uses[head.Node]++
		} else {
			for _, r := range g {
				if w, _ := b.createOf(r.Node); w != nil {
					head = r
					break
				}
			}
		}
		for _, r := range g {
			if r != head {
				b.canon[r] = head
			}
		}
	}
	noted := map[*Node]bool{}
	for _, r := range b.d.Records {
		if n := r.Node; own(n) && uses[n] == 0 && !noted[n] && b.canon[r] != nil {
			noted[n] = true
			b.note("", n.Name+": create not run", "its records are created by "+b.canon[r].Node.Name+" (the same keys); pick records only "+n.Name+" lists to run it")
		}
	}
	for _, r := range b.d.Records {
		if b.isView(r) || b.canon[r] != nil {
			continue
		}
		if w, _ := b.createOf(r.Node); w == nil {
			continue
		}
		b.created[r] = true
		b.keyVar[r] = map[string]string{}
		for _, ptr := range keyOf(r.Node) {
			field := PointerField(ptr)
			if strings.Contains(field, ".") {
				continue
			}
			v, ok := Pointer(r.Value, ptr)
			if !ok {
				continue
			}
			name := r.ID + "." + field
			b.keyVar[r][field] = name
			b.byValue[text(v)] = append(b.byValue[text(v)], keyRef{r, field})
		}
	}
}

// relation scores how well a field name refers to the record: the length
// of the record's name in it ("originDockId" and docks: 4), 0 for none.
func (b *builder) relation(field string, r *Record) int {
	if b.readBy(r.Node, field) {
		// the tree reads the record by that field (docks: .docks)
		return 1000
	}
	f := strings.ToLower(field)
	best := 0
	names := []string{r.Node.Name}
	if res := b.resourceOf(r.Node); res != nil {
		names = append(names, res.Name)
	}
	for _, n := range names {
		n = strings.ToLower(singular(n))
		if len(n) >= 2 && strings.Contains(f, n) && len(n) > best {
			best = len(n)
		}
	}
	return best
}

// readBy reports whether a node reads its records by a field of the record
// above, the field names them: the read of one record by it (GET
// /docks/{code} code=.docks), or a where on a key of the records
// (id: docks.pilotId).
func (b *builder) readBy(n *Node, field string) bool {
	if n.Parent == nil {
		return false
	}
	names := func(v any) bool {
		s, ok := v.(string)
		if !ok {
			return false
		}
		name, f, ok := strings.Cut(s, ".")
		return ok && strings.EqualFold(f, field) && (name == "" || n.Parent.Ancestor(name) != nil)
	}
	if op := lookupOp(b.s, n.Call); op != nil && !isList(op) && !n.HasItems {
		for _, v := range n.Path {
			if names(v) {
				return true
			}
		}
	}
	for _, c := range n.Where {
		key := slices.ContainsFunc(keyOf(n), func(k string) bool { return strings.EqualFold(PointerField(k), c.Field) })
		if key && names(c.Test) {
			return true
		}
	}
	return false
}

// placeholderFor replaces a value of the stage by the saved key of the
// record the scenario creates for it, when the field name refers to that
// record: authorId 3 → {{authors2.id}}. Without a fitting record the value
// stays.
func (b *builder) placeholderFor(v any, fields []string, exclude *Record, step string) any {
	best := b.matches(v, fields, exclude)
	if b.strict {
		// the file says where values come from: a match is only a hint
		if len(best) == 1 {
			b.note(step, fmt.Sprintf("%s = %s stays as it is", strings.Join(fields, "/"), text(v)),
				"it is the key of "+best[0].rec.ID+", which the test creates; to send that, give the source in order ("+fields[0]+": <node>."+best[0].field+")")
		}
		return v
	}
	switch {
	case len(best) == 1:
		return "{{" + b.keyVar[best[0].rec][best[0].field] + "}}"
	case len(best) > 1:
		var names []string
		for _, kr := range best {
			names = append(names, kr.rec.ID)
		}
		b.note(step, fmt.Sprintf("%s = %s stays as it is", strings.Join(fields, "/"), text(v)),
			"it fits several records ("+strings.Join(names, ", ")+"); write a reference (.field or <node>.field) to choose one")
	}
	return v
}

// matches are the keys of created records a value of the stage is, whose
// record the field names best.
func (b *builder) matches(v any, fields []string, exclude *Record) []keyRef {
	if v == nil {
		return nil
	}
	switch v.(type) {
	case map[string]any, []any, bool:
		return nil
	}
	var best []keyRef
	score := 0
	for _, kr := range b.byValue[text(v)] {
		if kr.rec == exclude {
			continue
		}
		s := 0
		for _, f := range fields {
			s = max(s, b.relation(f, kr.rec))
		}
		switch {
		case s == 0:
		case s > score:
			best, score = []keyRef{kr}, s
		case s == score:
			best = append(best, kr)
		}
	}
	return best
}

// resolve fills a value of the tree for the scenario: a reference to a key
// of a record the scenario creates becomes its placeholder, another
// reference its value (or a placeholder by name), a plain value stays.
func (b *builder) resolve(v any, at *Record, scope *Node, param, step string, exclude *Record) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, c := range x {
			out[k] = b.resolve(c, at, scope, k, step, exclude)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = b.resolve(c, at, scope, param, step, exclude)
		}
		return out
	}
	if s, ok := v.(string); ok && len(ComposedRefs(s, b.scope)) > 0 {
		// each part as text; a placeholder of a part is filled by the run
		out, err := compose(s, b.scope, func(ref string) (any, error) {
			if x := b.resolve(ref, at, scope, param, step, exclude); x != nil {
				return x, nil
			}
			return nil, errNoValue
		})
		if err != nil {
			return nil
		}
		return out
	}
	if name, ok := ParamRef(v); ok {
		if !b.writeSaved[name] {
			return b.paramValue(name, at, scope, param, step, exclude)
		}
		// a write saves it: the scenario reads the saved value, the step
		// runs after that write (afterSaves); the value of params stands in
		// when no such write runs
		if _, done := b.stage[name]; !done {
			b.stage[name] = nil
			if _, set := b.t.param(name); set {
				b.stage[name] = b.paramValue(name, at, scope, param, step, exclude)
			}
		}
		return "{{" + name + "}}"
	}
	ref, ok := ParseRef(v, b.scope)
	if !ok {
		return v
	}
	rec := at
	if ref.Node != "" {
		rec = nil
		if at != nil {
			rec = at.Ancestor(ref.Node)
		}
		if rec == nil {
			// another node of the tree: its first record
			for _, r := range b.d.Records {
				if r.Node.Name == ref.Node {
					rec = r
					break
				}
			}
		}
	}
	if rec == nil {
		b.note(step, fmt.Sprintf("%s = %s", param, ref), "no record to read it from")
		return nil
	}
	val, found := rec.Get(ref.Field)
	if !found {
		b.note(step, fmt.Sprintf("%s = %s", param, ref), rec.ID+" has no such field")
		return nil
	}
	e := b.entity(rec)
	if b.created[e] {
		if name := b.keyVar[e][ref.Field]; name != "" {
			return "{{" + name + "}}"
		}
		// a view has the key of its record under the same field
		if e != rec {
			for field, name := range b.keyVar[e] {
				if strings.EqualFold(field, ref.Field) {
					return "{{" + name + "}}"
				}
			}
		}
	}
	if v, ok := b.sentValue(rec, ref.Field); ok {
		// the create sent it from the source order gives
		return v
	}
	last := ref.Field[strings.LastIndex(ref.Field, ".")+1:]
	return b.placeholderFor(val, []string{param, last}, exclude, step)
}

// errNoValue stops a composed value whose part has no value (noted).
var errNoValue = errors.New("no value")

// paramValue is the value of params a "$name" names, for the scenario.
func (b *builder) paramValue(name string, at *Record, scope *Node, param, step string, exclude *Record) any {
	val, set := b.t.param(name)
	if !set {
		b.note(step, fmt.Sprintf("%s = $%s", param, name), "params has no value for it; set it at the top of tree.yaml")
		return nil
	}
	if len(ComposedRefs(val, b.scope)) > 0 {
		// a composed value in params reads like it in place of $name
		return b.resolve(val, at, scope, param, step, exclude)
	}
	if _, isRef := ParseRef(val, b.scope); isRef {
		// a reference in params ("books.id") reads like one in place of $name
		return b.resolve(val, at, scope, param, step, exclude)
	}
	if _, nested := ParamRef(val); nested {
		b.note(step, fmt.Sprintf("%s = $%s", param, name), "params."+name+" names another $name; give a value or a reference to a node")
		return nil
	}
	// a value of the stage: the placeholder of the record created for it
	return b.placeholderFor(val, []string{param, name}, exclude, step)
}

// params fills path, query and header of a step from the values of the
// tree; required parameters without value get the example of the spec.
func (b *builder) params(st *scenario.Step, op *spec.Operation, path, query, header map[string]any, at *Record, scope *Node, exclude *Record) {
	fill := func(m map[string]any) map[string]any {
		if len(m) == 0 {
			return nil
		}
		out := map[string]any{}
		for k, v := range m {
			out[k] = b.resolve(v, at, scope, k, st.ID, exclude)
		}
		return out
	}
	st.Path, st.Query, st.Header = fill(path), fill(query), fill(header)
	for _, p := range op.Params {
		if p.In == openapi3.ParameterInPath || !p.Required || strings.EqualFold(p.Name, "If-Match") {
			continue
		}
		if p.In != openapi3.ParameterInQuery && p.In != openapi3.ParameterInHeader {
			continue
		}
		m := &st.Query
		if p.In == openapi3.ParameterInHeader {
			m = &st.Header
		}
		if _, ok := (*m)[p.Name]; ok {
			continue
		}
		v, ok := paramExample(p)
		if !ok {
			b.note(st.ID, fmt.Sprintf("no value for the required %s parameter %s", p.In, p.Name), "the spec has no example, default or enum; set it in the tree")
			continue
		}
		if *m == nil {
			*m = map[string]any{}
		}
		(*m)[p.Name] = v
	}
}

// creates adds a create step per record the scenario creates, in an order
// where every record comes after the records its create refers to, and
// returns that order.
func (b *builder) creates() []*Record {
	type pending struct {
		r    *Record
		st   *scenario.Step
		uses []string
	}
	var list []*pending
	owner := map[string]*Record{}
	for r := range b.created {
		for _, name := range b.keyVar[r] {
			owner[name] = r
		}
	}
	for _, r := range b.d.Records {
		if !b.created[r] {
			continue
		}
		w, op := b.createOf(r.Node)
		expect := w.Expect
		if expect == 0 {
			expect = lowestSuccess(op)
		}
		if expect == 0 {
			expect = http.StatusCreated
		}
		st := &scenario.Step{ID: b.id(r.ID + ".create"), Call: callOf(op), Expect: expect,
			Why: fmt.Sprintf("rebuilds %s (%s)", r.ID, r.Request)}
		b.params(st, op, w.Path, w.Query, w.Header, r, r.Node, r)
		if ref := requestRef(op); ref != nil || w.HasBody {
			body, unique := b.body(st.ID, r, w, ref)
			st.Body, st.HasBody, st.Unique = body, true, unique
		}
		b.saves(st, r, op, expect)
		addSaves(st, w)
		b.createID[r] = st.ID
		list = append(list, &pending{r: r, st: st, uses: st.Uses()})
	}
	// order: each time the first record (in fetch order) whose saved values
	// are all there
	done := map[*Record]bool{}
	var order []*Record
	take := func(p *pending) {
		done[p.r] = true
		order = append(order, p.r)
		b.slot = ownerName(b, p.r)
		b.push(p.st)
	}
	defer func() { b.slot = "" }()
	if b.strict {
		// the order of the file: each record after the records its create
		// takes keys of, else the file is wrong
		slices.SortStableFunc(list, func(x, y *pending) int { return b.orderIndex(x.r.Node) - b.orderIndex(y.r.Node) })
		for _, p := range list {
			for _, name := range p.uses {
				if o := owner[name]; o != nil && o != p.r && !done[o] {
					b.problem("order: %s is created before %s, but its create takes {{%s}}; move %s above %s in order",
						p.r.ID, o.ID, name, ownerName(b, o), ownerName(b, p.r))
				}
			}
			take(p)
		}
	}
	for len(order) < len(list) {
		var next *pending
		for _, p := range list {
			if done[p.r] {
				continue
			}
			ready := true
			for _, name := range p.uses {
				if o := owner[name]; o != nil && o != p.r && !done[o] {
					ready = false
				}
			}
			if ready {
				next = p
				break
			}
		}
		if next == nil {
			for _, p := range list {
				if !done[p.r] {
					b.note(p.st.ID, "order of "+p.r.ID, "its references form a cycle; it is created in the order of the fetch")
					next = p
					break
				}
			}
		}
		take(next)
	}
	for _, p := range list {
		for _, name := range p.uses {
			if o := owner[name]; o != nil && o != p.r && !slices.Contains(b.deps[p.r], o) {
				b.deps[p.r] = append(b.deps[p.r], o)
			}
		}
	}
	return order
}

// saves adds what a create keeps: the keys of the record (from the answer,
// the Location header or the request) and its ETag.
func (b *builder) saves(st *scenario.Step, r *Record, op *spec.Operation, status int) {
	st.Save = map[string]string{}
	resp := responseSchema(op, status)
	props, _ := properties(resp)
	req, _ := properties(schemaOf(requestRef(op)))
	first := true
	for _, field := range sortedKeys(b.keyVar[r]) {
		name := b.keyVar[r][field]
		switch {
		case props[field] != nil:
			st.Save[name] = "/" + field
		case req[field] != nil && hasField(st.Body, field):
			st.Save[name] = "request /" + field
		case first && (resp == nil || documentsHeader(op, status, "Location")):
			st.Save[name] = "header Location"
		default:
			b.note(st.ID, "no saved key "+field, "the answer of "+st.Call+" has no field "+field+" and no Location header; later steps use the value of the stage")
			delete(b.keyVar[r], field)
			continue
		}
		first = false
	}
	if b.usesIfMatch(r.Node) && documentsHeader(op, status, "ETag") {
		b.etag[r] = r.ID + ".etag"
		st.Save[b.etag[r]] = "header ETag"
	}
	if len(st.Save) == 0 {
		st.Save = nil
	}
}

// addSaves adds to a step what its write saves (receiptNo: header X-Receipt-No).
func addSaves(st *scenario.Step, w *Write) {
	if len(w.Save) == 0 {
		return
	}
	if st.Save == nil {
		st.Save = map[string]string{}
	}
	maps.Copy(st.Save, w.Save)
}

func schemaOf(ref *openapi3.SchemaRef) *openapi3.Schema {
	if ref == nil {
		return nil
	}
	return ref.Value
}

func hasField(body any, field string) bool {
	m, ok := body.(map[string]any)
	if !ok {
		return false
	}
	_, ok = m[field]
	return ok
}

func documentsHeader(op *spec.Operation, status int, name string) bool {
	_, r := cases.ResponseFor(op, status)
	if r == nil {
		return false
	}
	for h := range r.Headers {
		if strings.EqualFold(h, name) {
			return true
		}
	}
	return false
}

// usesIfMatch reports whether a write of the node takes If-Match.
func (b *builder) usesIfMatch(n *Node) bool {
	for _, kind := range n.Kinds() {
		if _, op := b.writeOf(n, kind); op != nil && ifMatch(op) != nil {
			return true
		}
	}
	return false
}

func ifMatch(op *spec.Operation) *openapi3.Parameter {
	for _, p := range op.Params {
		if p.In == openapi3.ParameterInHeader && strings.EqualFold(p.Name, "If-Match") {
			return p
		}
	}
	return nil
}

// body builds the request body of a write: the body of the tree, else the
// record (with the fields of its views, or the record From names) cut to
// the schema of the request. Fields that refer to a record the scenario
// creates get its placeholder; required fields the record lacks are
// generated. unique lists the fields a run with Unique makes new.
func (b *builder) body(step string, r *Record, w *Write, ref *openapi3.SchemaRef) (any, []string) {
	if w.HasBody {
		return b.resolve(w.Body, r, r.Node, "", step, r), nil
	}
	src := b.source(r, w.From, step)
	if ref == nil || ref.Value == nil {
		return map[string]any{}, nil
	}
	dto := dict.DTORef(ref)
	out := b.project(step, src, ref.Value, r.Node.Name+"."+dto, dto, 0)
	m, ok := out.(map[string]any)
	if !ok {
		return out, nil
	}
	props, _ := properties(ref.Value)
	var unique []string
	set := map[string]bool{}
	if e := b.entryOf(b.entity(r)); e != nil {
		for f := range e.Values {
			if k, ok := propertyFoldName(props, f); ok {
				set[k] = true
			}
		}
	}
	b.sources(r, m, props)
	for _, k := range sortedKeys(m) {
		if !set[k] {
			m[k] = b.substitute(m[k], k, r, step)
		}
		if p := props[k]; p != nil && p.Value != nil && isUniqueField(p.Value, k, r.Node) {
			if _, isStr := m[k].(string); isStr && len(scenario.Names(m[k])) == 0 {
				unique = append(unique, "/"+k)
			}
		}
	}
	b.validate(step, m, ref.Value)
	return m, unique
}

// substitute replaces the values of a body that refer to records the
// scenario creates, also in nested objects and lists.
func (b *builder) substitute(v any, field string, r *Record, step string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			x[k] = b.substitute(c, k, r, step)
		}
		return x
	case []any:
		for i, c := range x {
			x[i] = b.substitute(c, field, r, step)
		}
		return x
	}
	return b.placeholderFor(v, []string{field}, r, step)
}

// source is the value a body is built from: the record of the node from
// names (above or below r) first, then r itself, its views and the other
// reads about it (a node below r that reads it by its key: details, log);
// a field is taken from the first that has it.
func (b *builder) source(r *Record, from, step string) any {
	var parts []any
	if from != "" && from != "." {
		found := false
		if a := r.Ancestor(from); a != nil {
			parts, found = append(parts, a.Value), true
		}
		for _, c := range b.d.Records {
			if !found && c.Node.Name == from && c.Ancestor(r.Node.Name) == r {
				parts, found = append(parts, c.Value), true
			}
		}
		if !found {
			b.note(step, "body from "+from, "no record of "+from+" belongs to "+r.ID+"; the body is built from "+r.ID+" and the reads about it")
		}
	}
	parts = append(parts, r.Value)
	var others []any
	for _, c := range b.d.Records {
		if c.Parent != r {
			continue
		}
		switch {
		case b.isView(c):
			parts = append(parts, c.Value)
		case b.about(c, r):
			others = append(others, c.Value)
		}
	}
	parts = append(parts, others...)
	base := map[string]any{}
	merged := false
	for _, p := range parts {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		merged = true
		for k, v := range m {
			if _, has := lookupFold(base, k); !has {
				base[k] = scenario.Clone(v)
			}
		}
	}
	if !merged {
		return r.Value
	}
	return base
}

// about reports whether a record below r reads r by its key (GET
// /books/{bookId}/details below a book) and is no record of its own.
func (b *builder) about(c, r *Record) bool {
	if w, _ := b.createOf(c.Node); w != nil {
		return false
	}
	if _, isMap := c.Value.(map[string]any); !isMap {
		return false
	}
	for _, v := range c.Node.Path {
		ref, ok := ParseRef(v, b.scope)
		if !ok || ref.Node != "" && ref.Node != r.Node.Name {
			continue
		}
		for _, k := range keyOf(r.Node) {
			if strings.EqualFold(PointerField(k), ref.Field) {
				return true
			}
		}
	}
	return false
}

// project cuts a value to a schema: object fields the schema declares (no
// readOnly ones), list elements by the item schema. A required field the
// value lacks is generated.
func (b *builder) project(step string, v any, s *openapi3.Schema, path, dto string, depth int) any {
	if s == nil || depth > 12 {
		return v
	}
	if s.Type != nil && s.Type.Is("array") && s.Items != nil {
		l, ok := v.([]any)
		if !ok {
			return v
		}
		out := make([]any, len(l))
		for i, c := range l {
			out[i] = b.project(step, c, s.Items.Value, path+"[]", dict.DTORef(s.Items), depth+1)
		}
		return out
	}
	props, required := properties(s)
	if len(props) == 0 {
		return v
	}
	in, _ := v.(map[string]any)
	out := map[string]any{}
	for _, name := range sortedKeys(props) {
		p := props[name].Value
		if p == nil || p.ReadOnly {
			continue
		}
		if val, ok := lookupFold(in, name); ok && val != nil {
			out[name] = b.project(step, val, p, path+"."+name, dict.DTORef(props[name]), depth+1)
			continue
		}
		if !slices.Contains(required, name) {
			continue
		}
		g := value.Generate(p, value.Context{Seed: 42, Path: "tree." + path + "." + name, Name: name, Parent: dto})
		if !g.OK {
			b.note(step, "no value for "+name, "the record has none and the schema allows no generated one ("+g.Reason+"); write the body in the tree")
			continue
		}
		out[name] = spec.Normalize(g.Value)
		b.note(step, fmt.Sprintf("%s = %s generated", name, text(out[name])), "the record of the stage has no "+name+", the request requires it")
	}
	return out
}

func lookupFold(m map[string]any, name string) (any, bool) {
	if v, ok := m[name]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return nil, false
}

// validate notes the fields of a body that do not fit their schema;
// fields with placeholders are left out.
func (b *builder) validate(step string, m map[string]any, s *openapi3.Schema) {
	props, _ := properties(s)
	v := spec.NewValidator()
	for _, k := range sortedKeys(m) {
		p := props[k]
		if p == nil || p.Value == nil || len(scenario.Names(m[k])) > 0 {
			continue
		}
		if errs := v.Validate(p.Value, spec.Normalize(m[k]), spec.ModeRequest); len(errs) > 0 {
			b.note(step, fmt.Sprintf("%s = %s does not fit the schema", k, text(m[k])), errs[0].String())
		}
	}
}

func isUniqueField(p *openapi3.Schema, name string, n *Node) bool {
	if v, _ := p.Extensions["x-apitest-unique"].(bool); v {
		return true
	}
	if slices.Contains(uniqueNames, strings.ToLower(name)) {
		return true
	}
	for _, k := range keyOf(n) {
		if strings.EqualFold(PointerField(k), name) {
			return true
		}
	}
	return false
}

// reads adds the reads in the order of the fetch: per list node one list
// per record above it (it holds the records of the node), per other node
// one read per record (it holds what the create sent).
func (b *builder) reads() {
	defer func() { b.slot = "" }()
	listed := map[string]bool{}
	for _, r := range b.d.Records {
		n := r.Node
		if n.IsRequire {
			continue
		}
		op := lookupOp(b.s, n.Call)
		if op == nil {
			continue
		}
		if n.HasItems || isList(op) {
			key := n.PathName() + "\x00"
			if r.Parent != nil {
				key += r.Parent.ID
			}
			if listed[key] {
				continue
			}
			listed[key] = true
			b.slot = n.Name + "." + StepList
			b.listStep(r, op)
			continue
		}
		b.slot = n.Name + "." + StepRead
		b.readStep(r, op)
	}
}

func (b *builder) listStep(r *Record, op *spec.Operation) {
	n := r.Node
	base := n.Name + ".list"
	if r.Parent != nil {
		base = r.Parent.ID + "." + n.Name
	}
	st := &scenario.Step{ID: b.id(base), Call: callOf(op), Expect: lowestSuccess(op)}
	if st.Expect == 0 {
		st.Expect = http.StatusOK
	}
	query := n.Query
	if n.Page != nil && n.Page.In != "body" && len(n.Page.Size) > 0 {
		query = merged(cloneMap(query), n.Page.Size)
	}
	b.params(st, op, n.Path, singleValues(query), n.Header, r.Parent, n.Parent, nil)
	if n.HasBody {
		st.Body, st.HasBody = b.resolve(n.Body, r.Parent, n.Parent, "", st.ID, nil), true
	}
	// the list must hold the records the scenario created, and the records
	// of the seed when the list is read by the values of the stage alone
	key := keyOf(n)[0]
	seedList := len(st.Uses()) == 0
	var values []any
	rebuilt, seeded, other := 0, 0, 0
	for _, c := range b.d.Children(r.Parent, n) {
		e := b.entity(c)
		switch name := b.keyVar[e][PointerField(key)]; {
		case b.created[e] && name != "":
			values = append(values, "{{"+name+"}}")
			rebuilt++
		case b.created[e] || b.creatable(e.Node):
			other++
		case seedList:
			if v, ok := Pointer(c.Value, key); ok {
				values = append(values, v)
			}
			b.seed(e)
			seeded++
		default:
			other++
		}
	}
	if len(values) > 0 && !strings.Contains(PointerField(key), ".") {
		st.Contains = &scenario.Contains{Key: key, Values: values}
	}
	switch {
	case seeded > 0:
		st.Why = fmt.Sprintf("lists %s; the seed holds its %s", n.Name, plural(seeded, "record"))
	case rebuilt > 0:
		st.Why = fmt.Sprintf("lists %s, it holds the %s created", n.Name, plural(rebuilt, "record"))
		if rebuilt == 1 {
			st.Why = fmt.Sprintf("lists %s, it holds the record created", n.Name)
		}
	default:
		st.Why = "lists " + n.Name
	}
	if other > 0 {
		b.note(st.ID, fmt.Sprintf("%s: %d records are not checked", n.Name, other), "the scenario does not rebuild them (no create, or no saved key); the list is checked by status and schema")
	}
	b.add(st)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// seed notes a record the instance must hold: the scenario reads it by
// the values of the stage.
func (b *builder) seed(r *Record) {
	if !slices.Contains(b.out.Seeds, r) {
		b.out.Seeds = append(b.out.Seeds, r)
	}
}

// singleValues keeps the first value of a list-valued query parameter: the
// scenario reads a list once.
func singleValues(m map[string]any) map[string]any {
	if len(m) == 0 {
		return m
	}
	out := map[string]any{}
	for k, v := range m {
		if l, ok := v.([]any); ok && len(l) > 0 {
			v = l[0]
		}
		out[k] = v
	}
	return out
}

func cloneMap(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (b *builder) readStep(r *Record, op *spec.Operation) {
	n := r.Node
	e := b.entity(r)
	st := &scenario.Step{ID: b.id(r.ID + ".read"), Call: callOf(op), Expect: lowestSuccess(op)}
	if st.Expect == 0 {
		st.Expect = http.StatusOK
	}
	b.params(st, op, n.Path, singleValues(n.Query), n.Header, r.Parent, n.Parent, nil)
	b.ownKeys(st, r, e)
	if n.HasBody {
		st.Body, st.HasBody = b.resolve(n.Body, r.Parent, n.Parent, "", st.ID, nil), true
	}
	switch {
	case b.created[e] && responseSchema(op, st.Expect) != nil:
		st.Equals = b.createID[e]
		st.Why = "reads " + e.ID + ", it holds what " + st.Equals + " sent"
	case b.created[e]:
		st.Why = "reads " + e.ID
	case len(st.Uses()) > 0:
		st.Why = "reads " + e.ID + ", which comes with the records it names"
	case b.creatable(e.Node):
		// the API creates such records, but this one is not rebuilt: an
		// empty instance does not hold it
		b.note(st.ID, "no read of "+e.ID, "the scenario does not rebuild it (its create needs a node above it); hang the node below the one whose create it takes")
		return
	default:
		st.Why = "reads " + e.ID + " of the seed"
		b.seed(e)
	}
	if b.add(st) && b.readID[e] == "" {
		b.readID[e] = st.ID
	}
	if st.Equals != "" {
		b.reader[st] = e
	}
}

// ownKeys sets a path value that is a list (the keys the record above
// lists: dockCode=.docks) to the one of them the record has, as the
// placeholder of its key when the scenario creates it.
func (b *builder) ownKeys(st *scenario.Step, r, e *Record) {
	for k, v := range st.Path {
		l, ok := v.([]any)
		if !ok {
			// the read of one record by its key: the key the test created it
			// with (the moon of a garden the test does not rebuild)
			if s, isText := v.(string); b.created[e] && (!isText || len(scenario.Names(s)) == 0) {
				for _, ptr := range keyOf(r.Node) {
					if own, found := Pointer(r.Value, ptr); found && text(own) == text(v) {
						if name := b.keyVar[e][PointerField(ptr)]; name != "" {
							st.Path[k] = "{{" + name + "}}"
						}
						break
					}
				}
			}
			continue
		}
		st.Path[k] = nil
		for _, ptr := range keyOf(r.Node) {
			own, found := Pointer(r.Value, ptr)
			if !found || !slices.ContainsFunc(l, func(x any) bool { return text(x) == text(own) }) {
				continue
			}
			st.Path[k] = own
			if name := b.keyVar[e][PointerField(ptr)]; b.created[e] && name != "" {
				st.Path[k] = "{{" + name + "}}"
			}
			break
		}
		if st.Path[k] == nil {
			b.note(st.ID, k+" from a list", r.ID+" has none of its values as key; set key at "+r.Node.Name)
		}
	}
}

// add appends a step unless the same request was added before.
func (b *builder) add(st *scenario.Step) bool {
	key := st.Call + " " + encodeJSON(st.Path) + " " + encodeJSON(st.Query) + " " + encodeJSON(st.Body) + " " + st.Token + " " + strconv.Itoa(st.Expect)
	if b.sent[key] {
		return false
	}
	b.sent[key] = true
	b.push(st)
	return true
}

// push appends a step to the slot of order being built.
func (b *builder) push(st *scenario.Step) {
	b.steps = append(b.steps, st)
	if b.slot != "" {
		b.slotOf[st] = b.slot
	}
}

// updates adds per node the update and the patch of its first created
// record, each followed by a read that must show the change; the variants
// by other keys (update-by-name) too, each with a change of its own.
func (b *builder) updates() {
	var nodes []*Node
	b.t.Walk(func(n *Node) {
		if !n.IsRequire && n.Enabled() {
			nodes = append(nodes, n)
		}
	})
	if b.strict {
		// the changes in the order of the file, the others after them
		slices.SortStableFunc(nodes, func(x, y *Node) int { return b.orderIndex(x) - b.orderIndex(y) })
	}
	for _, n := range nodes {
		for _, kind := range n.Kinds() {
			if base := KindBase(kind); base != WriteUpdate && base != WritePatch && !IsAction(kind) {
				continue
			}
			w, op := b.writeOf(n, kind)
			if w == nil {
				continue
			}
			// a record of the node the scenario created, or a view of one
			// (the write block at GET /gardens/{gardenId} below the gardens)
			var r *Record
			for _, c := range b.d.Of(n) {
				if b.created[b.entity(c)] {
					r = c
					break
				}
			}
			if recs := b.d.Of(n); r == nil && w.OnSeed && len(recs) > 0 {
				// onSeed: the first record, which the instance holds
				r = recs[0]
				b.note("", n.Name+": "+kind+" runs on "+r.ID+" of the seed", "onSeed: the instance holds the record; the scenario changes it")
			}
			if r == nil {
				why := "no record of " + n.Name + " is created by the scenario; it changes only records it created (onSeed: true changes a record of the seed)"
				if w.OnSeed {
					why = "onSeed: the fetch took no record of " + n.Name
				}
				b.note("", n.Name+": "+kind+" not run", why)
				continue
			}
			b.slot = n.Name + "." + kind
			b.update(r, w, op, kind)
			b.slot = ""
		}
	}
}

func (b *builder) update(r *Record, w *Write, op *spec.Operation, kind string) {
	expect := w.Expect
	if expect == 0 {
		expect = lowestSuccess(op)
	}
	if expect == 0 {
		expect = http.StatusOK
	}
	// r may be a view of the created record e: the request takes the
	// values of r, the step, ETag and read after it are those of e
	e := b.entity(r)
	st := &scenario.Step{ID: b.id(e.ID + "." + kind), Call: callOf(op), Expect: expect}
	b.params(st, op, w.Path, w.Query, w.Header, r, r.Node, nil)
	if p := ifMatch(op); p != nil && b.etag[e] != "" {
		if st.Header == nil {
			st.Header = map[string]any{}
		}
		st.Header[p.Name] = "{{" + b.etag[e] + "}}"
	}
	changed := ""
	action := IsAction(kind)
	if ref := requestRef(op); ref != nil || w.HasBody {
		// the unique values of the create again: a run with Unique sends the
		// new ones, the keys the next update by code or name takes stay
		body, unique := b.body(st.ID, r, w, ref)
		st.Unique = unique
		if !w.HasBody && !action {
			full := KindBase(kind) == WriteUpdate
			// the n-th variant changes the field n times: each update sends
			// a value the one before did not
			for range slices.Index(r.Node.KindsOf(KindBase(kind)), kind) + 1 {
				body, changed = b.change(body, schemaOf(ref), r, w.Change, full)
			}
		}
		st.Body, st.HasBody = body, true
		st.Unique = slices.DeleteFunc(st.Unique, func(ptr string) bool { return !hasField(body, strings.TrimPrefix(ptr, "/")) })
	}
	st.Why = KindBase(kind) + "s " + e.ID
	if by, ok := strings.CutPrefix(kind, KindBase(kind)+"-by-"); ok {
		st.Why += " by " + by
	}
	if changed != "" {
		st.Why += ", changes " + changed
	}
	if documentsHeader(op, expect, "ETag") && b.etag[e] != "" {
		st.Save = map[string]string{b.etag[e]: "header ETag"}
	}
	addSaves(st, w)
	b.add(st)
	if action {
		// an action changes the record in its own way: no read that
		// expects the body
		st.Why = "runs " + kind + " on " + e.ID
		return
	}
	if st.HasBody {
		b.changes[st] = e
	}
	if read := b.readID[e]; read != "" && st.HasBody {
		if rs := b.stepByID(read); rs != nil {
			again := cloneStep(rs)
			again.ID = b.id(e.ID + ".read.after" + camelKind(kind))
			again.Equals = st.ID
			again.Contains = nil
			again.Why = "reads " + e.ID + " again, it holds what " + st.ID + " sent"
			b.push(again)
		}
	}
}

// arrange puts the steps in the order of the file when it places steps
// besides the creates: the lines of order in turn, each with its steps,
// then the steps it does not name in the order they were built. A read
// after a change of its record checks what the change sent. A step that
// takes a value a later step saves is a problem.
func (b *builder) arrange() {
	if !b.strict || !b.t.HasSteps() {
		return
	}
	byKey := map[string][]*scenario.Step{}
	var rest []*scenario.Step
	for _, st := range b.steps {
		if k := b.slotOf[st]; k != "" && b.t.StepIndex(k) >= 0 {
			byKey[k] = append(byKey[k], st)
		} else {
			rest = append(rest, st)
		}
	}
	var out []*scenario.Step
	for _, e := range b.t.Order {
		if e.Step != "" && len(byKey[e.Key()]) == 0 {
			b.note("", "order: "+e.Key()+" runs no step", "the scenario builds no such step; the notes of "+e.Node+" say why")
		}
		out = append(out, byKey[e.Key()]...)
	}
	out = append(out, rest...)
	b.steps = out

	saver := map[string]*scenario.Step{}
	for _, st := range b.steps {
		for name := range st.Save {
			if saver[name] == nil {
				saver[name] = st
			}
		}
	}
	slotName := func(st *scenario.Step) string {
		if k := b.slotOf[st]; k != "" {
			return k
		}
		return st.ID
	}
	saved := map[string]bool{}
	seen := map[string]bool{}
	last := map[*Record]string{}
	for _, st := range b.steps {
		if r := b.reader[st]; r != nil && last[r] != "" {
			st.Equals = last[r]
			st.Why = "reads " + r.ID + ", it holds what " + st.Equals + " sent"
		}
		if r := b.changes[st]; r != nil {
			last[r] = st.ID
		}
		for _, name := range st.Uses() {
			if saved[name] || b.writeSaved[name] {
				continue
			}
			if s := saver[name]; s != nil {
				b.problem("order: %s runs before %s, but %s takes {{%s}}, which %s saves; move %s below %s",
					slotName(st), slotName(s), st.ID, name, s.ID, slotName(st), slotName(s))
			}
		}
		if st.Equals != "" && !seen[st.Equals] {
			b.problem("order: %s runs before the step %s it checks against; move it below", slotName(st), st.Equals)
		}
		seen[st.ID] = true
		for name := range st.Save {
			saved[name] = true
		}
	}
}

// change sets the fields of change, or changes one field (change auto or
// none given): a text gets " (revised)", a number one more. Keys,
// references and unique fields stay. A patch sends only the changed
// fields.
func (b *builder) change(body any, s *openapi3.Schema, r *Record, set map[string]any, full bool) (any, string) {
	m, ok := body.(map[string]any)
	if !ok {
		return body, ""
	}
	if len(set) > 0 {
		out := map[string]any{}
		if full {
			out = m
		}
		var names []string
		for _, k := range sortedKeys(set) {
			out[k] = b.resolve(set[k], r, r.Node, k, "", nil)
			names = append(names, k)
		}
		return out, strings.Join(names, ", ")
	}
	props, _ := properties(s)
	for _, name := range sortedKeys(m) {
		ref := props[name]
		if ref == nil || ref.Value == nil || isUniqueField(ref.Value, name, r.Node) || len(scenario.Names(m[name])) > 0 {
			continue
		}
		nv, ok := changed(ref.Value, m[name])
		if !ok {
			continue
		}
		if !full {
			return map[string]any{name: nv}, name
		}
		m[name] = nv
		return m, name
	}
	return m, ""
}

func changed(p *openapi3.Schema, v any) (any, bool) {
	if len(p.Enum) > 0 || p.Pattern != "" || p.Format != "" {
		return nil, false
	}
	switch value.Type(p) {
	case "string":
		s, _ := v.(string)
		nv := s + " (revised)"
		if p.MaxLength != nil && uint64(len(nv)) > *p.MaxLength {
			if *p.MaxLength < 2 {
				return nil, false
			}
			nv = strings.Repeat("r", int(*p.MaxLength))
			if s == nv {
				return nil, false
			}
		}
		return nv, true
	case "integer", "number":
		n, err := strconv.ParseFloat(text(v), 64)
		if err != nil {
			return nil, false
		}
		n++
		if p.Max != nil && n > *p.Max {
			n -= 2
		}
		if p.Min != nil && n < *p.Min {
			return nil, false
		}
		return json.Number(strconv.FormatFloat(n, 'f', -1, 64)), true
	}
	return nil, false
}

func (b *builder) stepByID(id string) *scenario.Step {
	for _, st := range b.steps {
		if st.ID == id {
			return st
		}
	}
	return nil
}

func cloneStep(st *scenario.Step) *scenario.Step {
	c := *st
	cm := func(m map[string]any) map[string]any {
		if m == nil {
			return nil
		}
		return scenario.Clone(m).(map[string]any)
	}
	c.Path, c.Query, c.Header = cm(st.Path), cm(st.Query), cm(st.Header)
	c.Body = scenario.Clone(st.Body)
	c.Save = nil
	c.Notes = nil
	return &c
}

// unauthorized adds per reading operation one step without token, if the
// operation needs one and documents 401.
func (b *builder) unauthorized() {
	done := map[string]bool{}
	var extra []*scenario.Step
	for _, st := range b.steps {
		op := lookupOp(b.s, st.Call)
		if op == nil || op.Method != http.MethodGet || done[op.ID] || st.Expect/100 != 2 {
			continue
		}
		if !exec.ResolveAuth(op.Security, b.schemes).Needed() || !documents(op, http.StatusUnauthorized) {
			continue
		}
		done[op.ID] = true
		c := cloneStep(st)
		c.ID = b.id(st.ID + ".unauthorized")
		c.Token = scenario.TokenNone
		c.Expect = http.StatusUnauthorized
		c.Equals, c.Contains = "", nil
		c.Why = "without token: 401"
		extra = append(extra, c)
	}
	b.steps = append(b.steps, extra...)
}

// deletes deletes the created records in reverse order; a record stays
// when a record that refers to it cannot be deleted. The records of a node
// take its deletes in turn (delete, delete-by-code, delete-by-name, …). The
// first use of each delete of a node is followed by a read that must answer
// 404.
func (b *builder) deletes(order []*Record) {
	deleted := map[*Record]bool{}
	afterRead := map[string]bool{}
	turn := map[*Node]int{}
	for i := len(order) - 1; i >= 0; i-- {
		r := order[i]
		// the deletes of its node, else of a view of it (the write block at
		// GET /gardens/{gardenId} below the gardens): src gives the values
		src := r
		kinds := b.deleteKinds(r.Node)
		for _, c := range b.d.Records {
			if len(kinds) > 0 {
				break
			}
			if c != r && b.entity(c) == r {
				src, kinds = c, b.deleteKinds(c.Node)
			}
		}
		var kind string
		var w *Write
		var op *spec.Operation
		if len(kinds) > 0 {
			kind = kinds[turn[src.Node]%len(kinds)]
			w, op = b.writeOf(src.Node, kind)
		}
		if w == nil {
			continue
		}
		blocked := ""
		for other, deps := range b.deps {
			if slices.Contains(deps, r) && !deleted[other] {
				blocked = other.ID
			}
		}
		if blocked != "" {
			b.note("", r.ID+" is not deleted", blocked+" refers to it and is not deleted")
			continue
		}
		expect := w.Expect
		if expect == 0 {
			expect = lowestSuccess(op)
		}
		if expect == 0 {
			expect = http.StatusNoContent
		}
		st := &scenario.Step{ID: b.id(r.ID + "." + kind), Call: callOf(op), Expect: expect, Why: "deletes " + r.ID}
		if by, ok := strings.CutPrefix(kind, WriteDelete+"-by-"); ok {
			st.Why += " by " + by
		}
		b.params(st, op, w.Path, w.Query, w.Header, src, src.Node, nil)
		if p := ifMatch(op); p != nil && b.etag[r] != "" {
			if st.Header == nil {
				st.Header = map[string]any{}
			}
			st.Header[p.Name] = "{{" + b.etag[r] + "}}"
		}
		addSaves(st, w)
		b.add(st)
		deleted[r] = true
		turn[src.Node]++
		if afterRead[src.Node.Name+"."+kind] {
			continue
		}
		afterRead[src.Node.Name+"."+kind] = true
		rs := b.stepByID(b.readID[r])
		if rs == nil {
			continue
		}
		if rop := lookupOp(b.s, rs.Call); rop == nil || !documents(rop, http.StatusNotFound) {
			continue
		}
		again := cloneStep(rs)
		again.ID = b.id(r.ID + ".read.after" + camelKind(kind))
		again.Expect = http.StatusNotFound
		again.Equals, again.Contains = "", nil
		again.Why = "reads " + r.ID + " after the delete: 404"
		b.steps = append(b.steps, again)
	}
	for _, n := range slices.SortedFunc(maps.Keys(turn), func(a, c *Node) int { return strings.Compare(a.Name, c.Name) }) {
		kinds := b.deleteKinds(n)
		for _, kind := range kinds[min(turn[n], len(kinds)):] {
			b.note("", n.Name+": "+kind+" not run", fmt.Sprintf("the scenario deletes %d records of %s, one per delete; pick more to run it", turn[n], n.Name))
		}
	}
}

// afterSaves orders the steps that use a value a write saves ($receiptNo):
// a step no step before it gives the value runs right after the first
// step that saves it (the read of a receipt after the update that answers
// with it). Without such a step, the value of params stands in.
func (b *builder) afterSaves() {
	if len(b.stage) == 0 {
		return
	}
	savers := map[string][]int{}
	for i, st := range b.steps {
		for name := range st.Save {
			savers[name] = append(savers[name], i)
		}
	}
	for _, name := range sortedKeys(b.stage) {
		if len(savers[name]) > 0 {
			continue
		}
		b.note("", "$"+name+" from params", "no write that saves "+name+" runs in the scenario; its steps take the value of params")
		for _, st := range b.steps {
			fillName(st, name, b.stage[name])
		}
	}
	after := map[int][]*scenario.Step{}
	moved := map[*scenario.Step]bool{}
	for i, st := range b.steps {
		target := -1
		for _, name := range st.Uses() {
			idx := savers[name]
			if !b.writeSaved[name] || len(idx) == 0 || idx[0] < i {
				continue
			}
			j := -1
			for _, k := range idx {
				if k > i {
					j = k
					break
				}
			}
			if j < 0 {
				// only the step itself saves it: the value of params
				b.note(st.ID, "$"+name+" from params", "no step before it saves "+name)
				fillName(st, name, b.stage[name])
				continue
			}
			target = max(target, j)
		}
		if target >= 0 {
			moved[st] = true
			after[target] = append(after[target], st)
			b.note(st.ID, "runs after "+b.steps[target].ID, "it uses what that step saves")
		}
	}
	if len(moved) == 0 {
		return
	}
	var out []*scenario.Step
	for i, st := range b.steps {
		if !moved[st] {
			out = append(out, st)
		}
		out = append(out, after[i]...)
	}
	b.steps = out
}

// fillName puts a value in place of the placeholder of a name, in every
// value of a step.
func fillName(st *scenario.Step, name string, val any) {
	ph := "{{" + name + "}}"
	var fill func(v any) any
	fill = func(v any) any {
		switch x := v.(type) {
		case string:
			if x == ph {
				return val
			}
			return strings.ReplaceAll(x, ph, text(val))
		case map[string]any:
			for k, c := range x {
				x[k] = fill(c)
			}
		case []any:
			for i, c := range x {
				x[i] = fill(c)
			}
		}
		return v
	}
	for _, m := range []map[string]any{st.Path, st.Query, st.Header} {
		fill(m)
	}
	st.Body = fill(st.Body)
	if st.Contains != nil {
		st.Contains.Values, _ = fill(st.Contains.Values).([]any)
	}
}

// deleteKinds are the deletes of a node the scenario can send: delete and
// its variants by other keys.
func (b *builder) deleteKinds(n *Node) []string {
	return slices.DeleteFunc(n.KindsOf(WriteDelete), func(k string) bool { w, _ := b.writeOf(n, k); return w == nil })
}

// camelKind writes a kind for a step id: update-by-name → UpdateByName.
func camelKind(kind string) string {
	var sb strings.Builder
	for part := range strings.SplitSeq(kind, "-") {
		if part != "" {
			sb.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
	}
	return sb.String()
}

// paramExample is a value for a parameter from the spec: example, schema
// example, default, first enum value, else generated.
func paramExample(p *openapi3.Parameter) (any, bool) {
	if v, ok := documentedValue(p); ok {
		return v, true
	}
	if p.Schema == nil || p.Schema.Value == nil {
		return nil, false
	}
	g := value.Generate(p.Schema.Value, value.Context{Seed: 42, Path: "tree.param." + p.Name, Name: p.Name})
	if !g.OK {
		return nil, false
	}
	return spec.Normalize(g.Value), true
}

// documentedValue is the value the spec gives a parameter: its example,
// default or first enum value.
func documentedValue(p *openapi3.Parameter) (any, bool) {
	if p.Example != nil {
		return spec.Normalize(p.Example), true
	}
	for _, k := range sortedKeys(p.Examples) {
		if ex := p.Examples[k]; ex != nil && ex.Value != nil && ex.Value.Value != nil {
			return spec.Normalize(ex.Value.Value), true
		}
	}
	if p.Schema == nil || p.Schema.Value == nil {
		return nil, false
	}
	s := p.Schema.Value
	switch {
	case s.Example != nil:
		return spec.Normalize(s.Example), true
	case s.Default != nil:
		return spec.Normalize(s.Default), true
	case len(s.Enum) > 0:
		return spec.Normalize(s.Enum[0]), true
	}
	return nil, false
}

func singular(w string) string {
	switch {
	case strings.HasSuffix(w, "ies"):
		return strings.TrimSuffix(w, "ies") + "y"
	case strings.HasSuffix(w, "sses"), strings.HasSuffix(w, "xes"):
		return strings.TrimSuffix(w, "es")
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return strings.TrimSuffix(w, "s")
	}
	return w
}
