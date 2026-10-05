// Package model detects the resources of a spec: which DTOs describe the
// same thing (BookRead, BookUpdate → Book), which fields identify a record
// (the path parameters /Book/id/{id} and /Book/{Code} → Id and Code), which
// operations list, read, create, update and delete it, which resource
// another one belongs to (/Book/{Code}/Article → Article below Book) and
// which fields refer to a record of another resource (Article.BookId).
//
// The generator uses the model to give all examples of a resource the
// values of one record, so a GET after an update expects what the update
// sent. Nothing of the model is written into the spec.
package model

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Role is what an operation does with its resource.
type Role string

// Roles.
const (
	RoleList   Role = "list"
	RoleRead   Role = "read"
	RoleCreate Role = "create"
	RoleUpdate Role = "update"
	RoleDelete Role = "delete"
)

// Resource is one kind of record.
type Resource struct {
	Name string
	// Schemas are the DTOs of the resource, the one its GETs return first.
	Schemas []string
	// Keys identify a record; updates never change them.
	Keys []string
	// Parent is the resource whose record a list of this one is fetched
	// below, e.g. Book for /Book/{Code}/Article.
	Parent *Resource
	// Relations are fields that refer to a record of another resource: of
	// the parent or a resource above it (BookCode in an Article), or of any
	// other resource by its name and key (AuthorId). Updates do not change
	// them.
	Relations []string
	Ops       []*Op

	fields openapi3.Schemas
	refs   map[string]relation // relation field → the key it holds
}

// relation is the key of another resource a field holds.
type relation struct {
	to  *Resource
	key string
}

// Op is an operation on a resource.
type Op struct {
	Op       *spec.Operation
	Resource *Resource
	Role     Role
	// Items is the JSON pointer of the list in the response of a list
	// operation, "" for a top-level array.
	Items string
	// Params are the path parameters that identify a record, in the order
	// of the path.
	Params []Param

	via *spec.Operation // the read at the same path the resource comes from
}

// Param is a path parameter that holds a key of a record.
type Param struct {
	Name     string
	Resource *Resource
	Field    string
}

// Note is something the detection could not decide.
type Note struct {
	Where, Message string
}

// Model is the detected model of a spec.
type Model struct {
	Resources []*Resource
	Notes     []Note
	ops       map[*spec.Operation]*Op
	touched   map[*spec.Operation][]*Resource
}

// Op returns the model of an operation, or nil if it belongs to no
// resource.
func (m *Model) Op(op *spec.Operation) *Op {
	if m == nil {
		return nil
	}
	return m.ops[op]
}

// OpByID returns the model of an operation by its operationId.
func (m *Model) OpByID(id string) *Op {
	if m == nil {
		return nil
	}
	for op, o := range m.ops {
		if op.ID == id {
			return o
		}
	}
	return nil
}

// Resource returns a resource by name, ignoring case.
func (m *Model) Resource(name string) *Resource {
	if m == nil {
		return nil
	}
	for _, r := range m.Resources {
		if strings.EqualFold(r.Name, name) {
			return r
		}
	}
	return nil
}

// Touched returns the records a writing operation of no resource changes,
// by the keys in its path: PUT /Dock/{code}/Ship/{id}/link changes a Dock
// and a Ship. A key of a resource above another one in the path only
// addresses it (/Planet/{p}/Dock/{code} touches the Dock, not the Planet).
func (m *Model) Touched(op *spec.Operation) []*Resource {
	if m == nil {
		return nil
	}
	return m.touched[op]
}

// Param returns the parameter of o with the name, or nil.
func (o *Op) Param(name string) *Param {
	for i := range o.Params {
		if o.Params[i].Name == name {
			return &o.Params[i]
		}
	}
	return nil
}

// Field returns the name of the field of r that matches name, ignoring
// case, or "".
func (r *Resource) Field(name string) string {
	if r.fields[name] != nil {
		return name
	}
	for _, k := range sortedKeys(r.fields) {
		if strings.EqualFold(k, name) {
			return k
		}
	}
	return ""
}

// FieldNames returns the fields of all DTOs of r, sorted.
func (r *Resource) FieldNames() []string { return sortedKeys(r.fields) }

// Schema returns the schema of a field of r.
func (r *Resource) Schema(field string) *openapi3.SchemaRef {
	if f := r.Field(field); f != "" {
		return r.fields[f]
	}
	return nil
}

// Fixed reports whether a field identifies the record or refers to another
// record; updates leave such fields alone.
func (r *Resource) Fixed(field string) bool {
	match := func(k string) bool { return strings.EqualFold(k, field) }
	return slices.ContainsFunc(r.Keys, match) || slices.ContainsFunc(r.Relations, match)
}

// Ref returns the resource and the key a relation field holds, or nil.
func (r *Resource) Ref(field string) (*Resource, string) {
	for _, f := range sortedKeys(r.refs) {
		if strings.EqualFold(f, field) {
			return r.refs[f].to, r.refs[f].key
		}
	}
	return nil, ""
}

// Ancestors returns the parent, its parent and so on.
func (r *Resource) Ancestors() []*Resource {
	var out []*Resource
	for p := r.Parent; p != nil && !slices.Contains(out, p) && p != r; p = p.Parent {
		out = append(out, p)
	}
	return out
}

// OpsWith returns the operations of r with the role, in spec order.
func (r *Resource) OpsWith(role Role) []*Op {
	var out []*Op
	for _, o := range r.Ops {
		if o.Role == role {
			out = append(out, o)
		}
	}
	return out
}

// Read is the DTO the GETs of r return.
func (r *Resource) Read() string { return r.Schemas[0] }

// suffixes are removed from DTO names to find the resource they describe.
var suffixes = []string{"Read", "Update", "Upsert", "Create", "Write", "Patch", "Put", "Post", "Dto", "DTO",
	"Request", "Response", "Details", "Detail", "View", "Model", "Input", "Output", "Summary",
	"Base", "Data", "Body", "Payload", "Item", "Entity"}

// Stem returns the resource name of a DTO name: BookRead → Book.
func Stem(name string) string {
	s := Stems(name)
	return s[len(s)-1]
}

// Stems returns name and every shorter name the removal of its suffixes
// passes through: BookDetailRead, BookDetail, Book. A suffix stays after a
// joining word: ShipsWithDetails is no Details of ShipsWith.
func Stems(name string) []string {
	out := []string{name}
	for changed := true; changed; {
		changed = false
		for _, s := range suffixes {
			rest := strings.TrimSuffix(name, s)
			if strings.HasSuffix(name, s) && len(name) > len(s) && !joined(rest) {
				name, changed = rest, true
				out = append(out, name)
			}
		}
	}
	return out
}

// joined reports whether a name ends with a word that joins it to what
// follows: ShipsWith, DockAnd, ListOf, SortBy.
func joined(name string) bool {
	for _, w := range []string{"With", "And", "Of", "By"} {
		if len(name) > len(w) && strings.HasSuffix(name, w) {
			if c := name[len(name)-len(w)-1]; c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
				return true
			}
		}
	}
	return false
}

// Detect builds the model of s. fixes are the "$model" entries.
func Detect(s *spec.Spec, fixes map[string]defaults.ModelFix) *Model {
	m := &Model{ops: map[*spec.Operation]*Op{}, touched: map[*spec.Operation][]*Resource{}}
	d := &detector{s: s, m: m, family: map[string]string{}, fixes: fixes}
	d.families()
	d.resources()
	d.roles()
	open := d.params()
	d.split()
	d.keys()
	d.match(open)
	d.parents()
	d.lookups()
	d.qualified()
	d.relations()
	d.touches()
	sort.SliceStable(m.Resources, func(i, j int) bool { return m.Resources[i].Name < m.Resources[j].Name })
	return m
}

type detector struct {
	s      *spec.Spec
	m      *Model
	fixes  map[string]defaults.ModelFix
	family map[string]string // DTO → resource name
}

func (d *detector) schemas() openapi3.Schemas {
	if d.s.Doc.Components == nil {
		return nil
	}
	return d.s.Doc.Components.Schemas
}

// families groups the DTOs by stem; "$model" schemas override it. A shorter
// stem is taken only if no path names a longer one: with a path
// /DockDetail/{id}, DockDetailRead is DockDetail, else Dock.
func (d *detector) families() {
	segs := map[string]bool{}
	for _, op := range d.s.Ops {
		for _, seg := range strings.Split(strings.Trim(op.Path, "/"), "/") {
			if seg != "" && !strings.HasPrefix(seg, "{") {
				segs[strings.ToLower(seg)] = true
				segs[strings.ToLower(singular(seg))] = true
			}
		}
	}
	for name := range d.schemas() {
		stems := Stems(name)
		d.family[name] = stems[len(stems)-1]
		for _, s := range stems[:len(stems)-1] {
			if segs[strings.ToLower(s)] {
				d.family[name] = s
				break
			}
		}
	}
	for res, f := range d.fixes {
		for _, name := range f.Schemas {
			if d.schemas()[name] == nil {
				d.m.Notes = append(d.m.Notes, Note{"$model." + res, fmt.Sprintf("schema %q does not exist", name)})
				continue
			}
			d.family[name] = res
		}
	}
}

// resources creates a resource for every family a GET returns.
func (d *detector) resources() {
	for _, op := range d.s.Ops {
		if op.Method != http.MethodGet {
			continue
		}
		dto, _, _ := listOrItem(op)
		if dto == "" {
			continue
		}
		name := d.family[dto]
		r := d.m.Resource(name)
		if r == nil {
			r = &Resource{Name: name}
			d.m.Resources = append(d.m.Resources, r)
		}
		if !slices.Contains(r.Schemas, dto) {
			r.Schemas = append(r.Schemas, dto)
		}
	}
	for _, r := range d.m.Resources {
		if f, ok := d.fix(r.Name); ok && len(f.Schemas) > 0 {
			r.Schemas = append([]string(nil), f.Schemas...)
		}
		for _, name := range sortedKeys(d.schemas()) {
			if d.family[name] == r.Name && !slices.Contains(r.Schemas, name) {
				r.Schemas = append(r.Schemas, name)
			}
		}
		r.fields = openapi3.Schemas{}
		for _, name := range r.Schemas { // the read DTO first: its fields win
			if ref := d.schemas()[name]; ref != nil && ref.Value != nil {
				props, _ := dict.Properties(ref.Value)
				for k, v := range props {
					if r.Field(k) == "" {
						r.fields[k] = v
					}
				}
			}
		}
	}
	for res := range d.fixes {
		if d.m.Resource(res) == nil {
			d.m.Notes = append(d.m.Notes, Note{"$model." + res, "no GET returns this resource; the entry has no effect"})
		}
	}
}

func (d *detector) fix(name string) (defaults.ModelFix, bool) {
	for k, f := range d.fixes {
		if strings.EqualFold(k, name) {
			return f, true
		}
	}
	return defaults.ModelFix{}, false
}

// roles assigns every operation to a resource: the GETs first, then the
// writes, the DELETEs last, because an update without a body of a resource
// takes the resource read at its path and a DELETE the one read or updated
// there.
func (d *detector) roles() {
	rank := func(op *spec.Operation) int {
		switch op.Method {
		case http.MethodGet:
			return 0
		case http.MethodDelete:
			return 2
		}
		return 1
	}
	ops := slices.Clone(d.s.Ops)
	sort.SliceStable(ops, func(i, j int) bool { return rank(ops[i]) < rank(ops[j]) })
	for _, op := range ops {
		var o *Op
		switch op.Method {
		case http.MethodGet:
			if dto, items, list := listOrItem(op); dto != "" {
				o = &Op{Op: op, Resource: d.m.Resource(d.family[dto]), Role: RoleRead, Items: items}
				if list {
					o.Role = RoleList
				}
			}
		case http.MethodPut, http.MethodPatch, http.MethodPost:
			role := RoleUpdate
			if op.Method == http.MethodPost {
				role = RoleCreate
			}
			if r := d.m.Resource(d.family[requestDTO(op)]); r != nil {
				o = &Op{Op: op, Resource: r, Role: role}
			} else if read := d.at(op.Path, RoleRead); read != nil && role == RoleUpdate {
				// a body of no resource ({Note: …}) changes the record
				// read at the same path
				o = &Op{Op: op, Resource: read.Resource, Role: role, via: read.Op}
			}
		case http.MethodDelete:
			if read := d.at(op.Path, RoleRead); read != nil {
				o = &Op{Op: op, Resource: read.Resource, Role: RoleDelete, via: read.Op}
			} else if upd := d.at(op.Path, RoleUpdate); upd != nil {
				o = &Op{Op: op, Resource: upd.Resource, Role: RoleDelete, via: upd.via}
			} else if last := lastParam(op.Path); last != "" {
				if r := d.byName(segmentBefore(op.Path, last)); r != nil {
					o = &Op{Op: op, Resource: r, Role: RoleDelete}
				}
			}
		}
		if o != nil && o.Resource != nil {
			d.m.ops[op] = o
		}
	}
	for _, op := range d.s.Ops { // spec order
		if o := d.m.ops[op]; o != nil {
			o.Resource.Ops = append(o.Resource.Ops, o)
		}
	}
}

// at returns the operation with the role at a path, or nil.
func (d *detector) at(path string, role Role) *Op {
	for _, other := range d.s.Ops {
		if other.Path == path {
			if o := d.m.ops[other]; o != nil && o.Role == role {
				return o
			}
		}
	}
	return nil
}

func (d *detector) byName(seg string) *Resource {
	if seg == "" {
		return nil
	}
	for _, r := range d.m.Resources {
		if strings.EqualFold(r.Name, seg) || strings.EqualFold(r.Name, singular(seg)) {
			return r
		}
	}
	return nil
}

// open is a path parameter params found no key for; match tries the keys
// of all resources.
type open struct {
	o     *Op
	name  string
	named *Resource // the resource the path names, without a field for it
}

// params maps the path parameters of each operation to record keys: the
// resource named in front of the parameter, else the resource of the
// operation for its last parameter or a field of the same name. A
// parameter at the end of a path that names another resource holds the key
// of the operation's own resource if both have that key
// (/Ship/{code} returning a ShipCard is a ShipCard by its Code).
func (d *detector) params() []open {
	var rest []open
	for _, op := range d.s.Ops {
		o := d.m.ops[op]
		if o == nil {
			continue
		}
		last := lastParam(op.Path)
		for _, p := range op.Params {
			if p.In != openapi3.ParameterInPath {
				continue
			}
			r := d.byName(segmentBefore(op.Path, p.Name))
			switch {
			case r == nil && p.Name == last && o.Role != RoleList && o.Role != RoleCreate:
				r = o.Resource
			case r == nil && o.Resource.Field(p.Name) != "":
				r = o.Resource
			case r != nil && r != o.Resource && d.ownKey(o, p.Name, r):
				r = o.Resource
			}
			field := ""
			if r != nil {
				field = d.paramField(r, p.Name)
			}
			if field == "" {
				rest = append(rest, open{o, p.Name, r})
				continue
			}
			o.Params = append(o.Params, Param{Name: p.Name, Resource: r, Field: field})
		}
	}
	return rest
}

// ownKey reports whether the last segment of o's path is the parameter,
// which names another resource r, while o reads, updates or deletes its own
// resource, a variant of r by its name (ShipCard of Ship), and both have the
// same field for it.
func (d *detector) ownKey(o *Op, param string, r *Resource) bool {
	if o.Role == RoleList || o.Role == RoleCreate || !strings.HasSuffix(o.Op.Path, "{"+param+"}") || !hasPrefixFold(o.Resource.Name, r.Name) {
		return false
	}
	own := d.paramField(o.Resource, param)
	return own != "" && strings.EqualFold(own, d.paramField(r, param))
}

// paramField is the field of r a path parameter holds: by its name, or as
// "$model" maps it.
func (d *detector) paramField(r *Resource, param string) string {
	if f := keyField(r, param); f != "" {
		return f
	}
	if fix, ok := d.fix(r.Name); ok {
		for p, field := range fix.Params {
			if strings.EqualFold(p, param) {
				return r.Field(field)
			}
		}
	}
	return ""
}

// split divides a resource that is created below records of different
// resources (/Person/Planet/{p} and /Person/Planet/{p}/Moon/{m}) into one
// resource per place, PlanetPerson and MoonPerson: a person of a planet is not
// reachable through the paths of a moon. Operations at no such place stay
// with the resource.
func (d *detector) split() {
	for _, r := range slices.Clone(d.m.Resources) {
		places := map[*Resource]bool{}
		for _, o := range r.OpsWith(RoleCreate) {
			if p := d.chain(o, true); p != nil {
				places[p] = true
			}
		}
		if len(places) < 2 {
			continue
		}
		parts := map[*Resource]*Resource{}
		var kept []*Op
		for _, o := range r.Ops {
			place := d.chain(o, true)
			if place == nil {
				kept = append(kept, o)
				continue
			}
			part := parts[place]
			if part == nil {
				name := place.Name + r.Name
				if d.m.Resource(name) != nil {
					name = r.Name + "@" + place.Name
				}
				part = &Resource{Name: name, Schemas: r.Schemas, Parent: place, fields: r.fields}
				parts[place] = part
				d.m.Resources = append(d.m.Resources, part)
			}
			o.Resource = part
			for i := range o.Params {
				if o.Params[i].Resource == r {
					o.Params[i].Resource = part
				}
			}
			part.Ops = append(part.Ops, o)
		}
		r.Ops = kept
		if len(kept) == 0 {
			d.m.Resources = slices.DeleteFunc(d.m.Resources, func(x *Resource) bool { return x == r })
		}
	}
}

// keys collects the key fields of every resource. A parameter of a list
// filters it (/Book/Genre/{genre}) and is no key.
func (d *detector) keys() {
	for _, r := range d.m.Resources {
		for _, o := range r.Ops {
			if o.Role == RoleList {
				continue
			}
			for _, p := range o.Params {
				if p.Resource == r && !slices.Contains(r.Keys, p.Field) {
					r.Keys = append(r.Keys, p.Field)
				}
			}
		}
		if f, ok := d.fix(r.Name); ok {
			for _, k := range f.Keys {
				if field := r.Field(k); field != "" && !slices.Contains(r.Keys, field) {
					r.Keys = append(r.Keys, field)
				} else if field == "" {
					d.m.Notes = append(d.m.Notes, Note{"$model." + r.Name, fmt.Sprintf("%s has no field %q", r.Name, k)})
				}
			}
		}
	}
}

// match maps the parameters params found no key for to the one resource
// that has a key of that name (/view/{dockCode} → Dock.DockCode or
// Dock.Code).
func (d *detector) match(rest []open) {
	for _, x := range rest {
		r, field := d.byKey(x.name)
		if r == nil {
			where := fmt.Sprintf("%s.parameters[%s]", x.o.Op.Where, x.name)
			if n := x.named; n != nil {
				d.m.Notes = append(d.m.Notes, Note{where, fmt.Sprintf("%s has no field for {%s}; map it with \"$model\": {%q: {\"params\": {%q: \"<field>\"}}}",
					n.Name, x.name, n.Name, x.name)})
				continue
			}
			d.m.Notes = append(d.m.Notes, Note{where, fmt.Sprintf("no resource found for {%s}; its example is not taken from a record", x.name)})
			continue
		}
		x.o.Params = append(x.o.Params, Param{Name: x.name, Resource: r, Field: field})
		path := x.o.Op.Path
		sort.SliceStable(x.o.Params, func(i, j int) bool {
			return strings.Index(path, "{"+x.o.Params[i].Name+"}") < strings.Index(path, "{"+x.o.Params[j].Name+"}")
		})
	}
}

// byKey returns the only resource with a key named like the parameter, by
// itself or after the resource name.
func (d *detector) byKey(param string) (*Resource, string) {
	var found *Resource
	field := ""
	for _, r := range d.m.Resources {
		for _, k := range r.Keys {
			if strings.EqualFold(k, param) || strings.EqualFold(r.Name+k, param) {
				if found != nil && found != r {
					return nil, ""
				}
				found, field = r, k
			}
		}
	}
	return found, field
}

// parents sets the resource each one belongs to: the last resource of the
// chain of keys its path starts with (/Planet/{p}/Moon/{m}/Garden: Moon),
// from a list or a create first, else from a read. The longest chain wins:
// a Garden listed below a Planet and below a Moon belongs to the Moon.
func (d *detector) parents() {
	for _, r := range d.m.Resources {
		if r.Parent != nil {
			continue
		}
		for _, role := range []Role{RoleList, RoleCreate, RoleRead} {
			best, depth := (*Resource)(nil), 0
			for _, o := range r.OpsWith(role) {
				p, n := d.chainDepth(o, false)
				if p != nil && p != r && n > depth && !slices.Contains(p.Ancestors(), r) {
					best, depth = p, n
				}
			}
			if best != nil {
				r.Parent = best
				break
			}
		}
	}
}

// chain returns the last resource of the chain of keys at the start of o's
// path: every key follows the segment that names its resource
// (/Planet/{p}/Moon/id/{id}/Garden: Moon). The chain ends at the segment of
// o's own resource, at a resource segment without key and at a word after
// the first key; words before it are a prefix (/api/v1). With head, o's own
// resource may come first (/Person/Planet/{p}/Moon/{m}: Moon).
func (d *detector) chain(o *Op, head bool) *Resource {
	r, _ := d.chainDepth(o, head)
	return r
}

// chainDepth is chain with the number of keys in the chain.
func (d *detector) chainDepth(o *Op, head bool) (*Resource, int) {
	segs := strings.Split(strings.Trim(o.Op.Path, "/"), "/")
	var last *Resource
	lit := ""
	keyed := false
	depth := 0
	for i, seg := range segs {
		if name, ok := placeholder(seg); ok {
			mp := o.Param(name)
			if mp == nil || mp.Resource == o.Resource || d.byName(lit) != mp.Resource {
				break
			}
			last, lit, keyed, head = mp.Resource, "", true, false
			depth++
			continue
		}
		if skipped(seg) || i+1 < len(segs) && strings.EqualFold(segs[i+1], "{"+seg+"}") {
			continue
		}
		r := d.byName(seg)
		if r != nil && r == o.Resource {
			if head && lit == "" && !keyed {
				head = false
				continue
			}
			break
		}
		if r != nil && d.byName(lit) != nil || r == nil && keyed {
			break
		}
		lit = seg
	}
	return last, depth
}

// lookups removes reads that address no record of their resource: without
// a key of it and of the resources above it, GET /Dock/Planet/{p}/Ship/{s}
// returns the dock a ship lies at, not one the model can name. An update or
// delete that took its resource from such a read goes with it.
func (d *detector) lookups() {
	gone := map[*spec.Operation]bool{}
	for _, r := range d.m.Resources {
		above := r.Ancestors()
		for _, o := range r.OpsWith(RoleRead) {
			related := len(o.Params) == 0
			for _, mp := range o.Params {
				related = related || mp.Resource == r || slices.Contains(above, mp.Resource)
			}
			if !related {
				gone[o.Op] = true
			}
		}
	}
	if len(gone) == 0 {
		return
	}
	for op, o := range d.m.ops {
		if gone[op] || o.via != nil && gone[o.via] {
			delete(d.m.ops, op)
			o.Resource.Ops = slices.DeleteFunc(o.Resource.Ops, func(x *Op) bool { return x == o })
		}
	}
}

// qualified moves the reads that name a record of one other resource
// besides the record and the resources above it
// (/Planet/{p}/Report/year/{yearCode}) into a resource of their own,
// YearReport below Planet, when the resource has reads without it too: the
// report of a year is another record than the report of the planet. The
// updates and deletes at their paths go with them.
func (d *detector) qualified() {
	for _, r := range slices.Clone(d.m.Resources) {
		above := r.Ancestors()
		extra := func(o *Op) (*Resource, int) {
			var q *Resource
			n := 0
			for _, mp := range o.Params {
				if mp.Resource == r || slices.Contains(above, mp.Resource) || mp.Resource == q {
					continue
				}
				q = mp.Resource
				n++
			}
			return q, n
		}
		plain := false
		for _, o := range r.OpsWith(RoleRead) {
			if _, n := extra(o); n == 0 {
				plain = true
			}
		}
		if !plain {
			continue
		}
		parts := map[*Resource]*Resource{}
		moved := map[*spec.Operation]*Resource{}
		for _, o := range r.OpsWith(RoleRead) {
			q, n := extra(o)
			if n != 1 {
				continue
			}
			part := parts[q]
			if part == nil {
				name := q.Name + r.Name
				if d.m.Resource(name) != nil {
					name = r.Name + "@" + q.Name
				}
				part = &Resource{Name: name, Schemas: r.Schemas, Keys: slices.Clone(r.Keys), Parent: r.Parent, fields: r.fields}
				parts[q] = part
				d.m.Resources = append(d.m.Resources, part)
			}
			moved[o.Op] = part
		}
		if len(moved) == 0 {
			continue
		}
		var kept []*Op
		for _, o := range r.Ops {
			part := moved[o.Op]
			if part == nil && o.via != nil {
				part = moved[o.via]
			}
			if part == nil {
				kept = append(kept, o)
				continue
			}
			o.Resource = part
			for i := range o.Params {
				if o.Params[i].Resource == r {
					o.Params[i].Resource = part
				}
			}
			part.Ops = append(part.Ops, o)
		}
		r.Ops = kept
	}
}

// relations finds the fields that refer to a record of another resource.
func (d *detector) relations() {
	for _, r := range d.m.Resources {
		r.refs = map[string]relation{}
		above := r.Ancestors()
		for _, f := range r.FieldNames() {
			if slices.Contains(r.Keys, f) {
				continue
			}
			if s := r.fields[f]; s == nil || s.Value == nil {
				continue
			} else if _, ok := dict.Primitive(s.Value); !ok {
				continue
			}
			if to, key := d.refers(r, f, above); to != nil {
				r.Relations = append(r.Relations, f)
				r.refs[f] = relation{to, key}
			}
		}
	}
}

// refers finds the key of another resource a field of r holds: the
// resource name and its key or Id (DockId, DockCode), a key that starts with
// the resource name (DockCode as key of Dock), or a key of a resource above
// r by its own name (Code in a Ship below a Dock). A generic key (Id, Code)
// only counts by its own name in a resource without keys: a view of the
// record above it. The nearest resource above wins, else the longest name;
// two resources that fit equally are no relation.
func (d *detector) refers(r *Resource, field string, above []*Resource) (*Resource, string) {
	var best *Resource
	bestKey, score, tie := "", -1, false
	for _, x := range d.m.Resources {
		if x == r {
			continue
		}
		up := slices.Index(above, x)
		keys := slices.Clone(x.Keys)
		if id := x.Field("Id"); id != "" && !slices.Contains(keys, id) {
			keys = append(keys, id)
		}
		for _, k := range keys {
			isKey := slices.Contains(x.Keys, k)
			ok := strings.EqualFold(field, x.Name+k) ||
				isKey && strings.EqualFold(field, k) && (hasPrefixFold(k, x.Name) || up >= 0 && (len(r.Keys) == 0 || !generic(k)))
			if !ok {
				continue
			}
			s := len(x.Name)
			if up >= 0 {
				s = 1000 - up
			}
			switch {
			case s > score:
				best, bestKey, score, tie = x, k, s, false
			case s == score && x != best:
				tie = true
			}
		}
	}
	if tie {
		return nil, ""
	}
	return best, bestKey
}

// touches finds the records each writing operation of no resource changes.
func (d *detector) touches() {
	for _, op := range d.s.Ops {
		if op.Method == http.MethodGet || d.m.ops[op] != nil {
			continue
		}
		var found []*Resource
		for _, p := range op.Params {
			if p.In != openapi3.ParameterInPath {
				continue
			}
			r := d.byName(segmentBefore(op.Path, p.Name))
			if r == nil || keyField(r, p.Name) == "" {
				r, _ = d.byKey(p.Name)
			}
			if r != nil && !slices.Contains(found, r) {
				found = append(found, r)
			}
		}
		var out []*Resource
		for _, r := range found {
			if !slices.ContainsFunc(found, func(x *Resource) bool { return x != r && slices.Contains(x.Ancestors(), r) }) {
				out = append(out, r)
			}
		}
		if len(out) > 0 {
			d.m.touched[op] = out
		}
	}
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) > len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// generic reports a key name that says nothing about its resource.
func generic(k string) bool {
	switch strings.ToLower(k) {
	case "id", "uuid", "key", "code", "name", "number":
		return true
	}
	return false
}

// keyField finds the field of r a path parameter holds: the same name, or
// Id for an id-like name.
func keyField(r *Resource, param string) string {
	if f := r.Field(param); f != "" {
		return f
	}
	lower := strings.ToLower(param)
	if lower == "id" || lower == "uuid" || lower == "key" || strings.HasSuffix(lower, "id") {
		if f := r.Field("id"); f != "" {
			return f
		}
	}
	return r.Field(r.Name + param)
}

// listOrItem returns the DTO of the lowest 2xx JSON response of op, the
// pointer of the list inside it and whether the response is a list.
func listOrItem(op *spec.Operation) (dto, items string, list bool) {
	ref := successSchema(op)
	if ref == nil || ref.Value == nil {
		return "", "", false
	}
	if ref.Value.Items != nil && isArray(ref.Value) {
		return dict.DTORef(ref.Value.Items), "", true
	}
	name := dict.DTORef(ref)
	if name == "" {
		return "", "", false
	}
	// a page: an object with one list of DTOs, e.g. {items: [...], total: 3}
	props, _ := dict.Properties(ref.Value)
	var lists []string
	for _, k := range sortedKeys(props) {
		if p := props[k]; p.Value != nil && isArray(p.Value) && p.Value.Items != nil && dict.DTORef(p.Value.Items) != "" {
			lists = append(lists, k)
		}
	}
	if len(lists) == 1 && len(props) <= 4 && page(name, lists[0]) {
		return dict.DTORef(props[lists[0]].Value.Items), "/" + lists[0], true
	}
	return name, "", false
}

// Page reports whether a DTO with one list is a page of that list.
func Page(dto, field string) bool { return page(dto, field) }

// page reports whether a DTO with one list is a page of that list: by the
// name of the list field or of the DTO. A Pilot with a list of Ships is no
// page of Ships.
func page(dto, field string) bool {
	switch strings.ToLower(field) {
	case "items", "data", "content", "results", "records", "values", "elements", "list", "entries", "rows":
		return true
	}
	for _, s := range []string{"Page", "List", "Result", "Results", "Collection", "Paged"} {
		if strings.HasSuffix(dto, s) {
			return true
		}
	}
	return false
}

func isArray(s *openapi3.Schema) bool {
	return s.Type != nil && s.Type.Is("array")
}

// successSchema is the schema of the lowest 2xx JSON response.
func successSchema(op *spec.Operation) *openapi3.SchemaRef {
	if op.Op.Responses == nil {
		return nil
	}
	codes := sortedKeys(op.Op.Responses.Map())
	for _, code := range codes {
		if len(code) != 3 || code[0] != '2' {
			continue
		}
		r := op.Op.Responses.Map()[code].Value
		if r == nil {
			continue
		}
		for _, mt := range sortedKeys(r.Content) {
			if spec.IsJSON(mt) && r.Content[mt].Schema != nil {
				return r.Content[mt].Schema
			}
		}
	}
	return nil
}

// requestDTO is the DTO of the JSON request body of op.
func requestDTO(op *spec.Operation) string {
	rb := op.Op.RequestBody
	if rb == nil || rb.Value == nil {
		return ""
	}
	for _, mt := range sortedKeys(rb.Value.Content) {
		if m := rb.Value.Content[mt]; spec.IsJSON(mt) && m.Schema != nil {
			return dict.DTORef(m.Schema)
		}
	}
	return ""
}

// skipped reports a path segment that only says what kind of key follows.
func skipped(seg string) bool {
	return slices.Contains([]string{"id", "ids", "code", "key", "uuid", "by-id", "byid", "name"}, strings.ToLower(seg))
}

// SegmentBefore returns the literal path segment that names the resource of
// a parameter, as the model reads it: /Book/id/{id} → Book.
func SegmentBefore(path, param string) string { return segmentBefore(path, param) }

// segmentBefore returns the literal path segment that names the resource of
// a parameter: the one in front of it, skipping words like "id" or "code"
// and a segment equal to the parameter name (/Book/id/{id}, /Book/code/{code}).
func segmentBefore(path, param string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	i := slices.Index(segs, "{"+param+"}")
	for j := i - 1; j >= 0; j-- {
		seg := segs[j]
		switch {
		case strings.HasPrefix(seg, "{"):
			return ""
		case skipped(seg), strings.EqualFold(seg, param):
			continue
		}
		return seg
	}
	return ""
}

func placeholder(seg string) (string, bool) {
	if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && len(seg) > 2 {
		return seg[1 : len(seg)-1], true
	}
	return "", false
}

func lastParam(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if strings.HasPrefix(segs[i], "{") && strings.HasSuffix(segs[i], "}") {
			return segs[i][1 : len(segs[i])-1]
		}
	}
	return ""
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

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Describe returns one line per resource, for the review.
func (m *Model) Describe() []string {
	var out []string
	for _, r := range m.Resources {
		parts := []string{"schemas " + strings.Join(r.Schemas, ", ")}
		if len(r.Keys) > 0 {
			parts = append(parts, "keys "+strings.Join(r.Keys, ", "))
		}
		if r.Parent != nil {
			parts = append(parts, "below "+r.Parent.Name)
		}
		for _, role := range []Role{RoleList, RoleRead, RoleCreate, RoleUpdate, RoleDelete} {
			var ids []string
			for _, o := range r.OpsWith(role) {
				ids = append(ids, o.Op.ID)
			}
			if len(ids) > 0 {
				parts = append(parts, string(role)+" "+strings.Join(ids, ", "))
			}
		}
		out = append(out, fmt.Sprintf("%s: %s", r.Name, strings.Join(parts, "; ")))
	}
	return out
}
