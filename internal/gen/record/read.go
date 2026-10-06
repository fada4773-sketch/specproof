package record

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// rec is the one record of a table the run follows.
type rec struct {
	table string
	data  map[string]any // the fields; an item GET adds to those of the list
	id    any            // local id (json.Number), nil without one
	newID any            // local id after the record was created again
	from  string         // where it was selected
	keys  map[string]bool
	ops   []string // the GETs whose answers the data holds
	path  string   // the POST path whose list it was selected from, "" for the first of a table
	seq   int      // the request it was selected from; 0 for a stored record
	note  string   // where it comes from, if not from a list: a copy the run created
	// again is the POST that created it again ("#181 POST /Planet/P1/Ship"),
	// "" while the instance holds it with its first id
	again string
	// oldIDs are the ids it had before the last time it was created again
	oldIDs []any
	// more marks a further record of a seed DTO ("count"): the environment
	// holds it, no path addresses it
	more bool
}

// ids are the local ids the record had during the run: the first one, the
// ones between and the current one.
func (r *rec) ids() []any {
	var out []any
	for _, id := range append(append([]any{r.id}, r.oldIDs...), r.newID) {
		if id != nil {
			out = append(out, id)
		}
	}
	return out
}

// hasID reports whether id is one of the ids the record had.
func (r *rec) hasID(id any) bool {
	for _, x := range r.ids() {
		if same(id, x) {
			return true
		}
	}
	return false
}

// origin names the record and the request it comes from, for the log.
func (r *rec) origin() string {
	op, u, _ := strings.Cut(r.from, " ")
	if r.note != "" {
		return r.note
	}
	sel := fmt.Sprintf("selected from #%d GET %s (%s)", r.seq, u, op)
	if r.seq == 0 {
		sel = fmt.Sprintf("stored in %q (%s %s)", RecordedKey, op, u)
	}
	if r.again != "" {
		return fmt.Sprintf("the %s %s created again with id %s, first %s", r.table, r.again, text(r.newID), sel)
	}
	return fmt.Sprintf("the %s %s", r.table, sel)
}

// known are the values the parameters can take: the fields of the
// selected records.
type known struct {
	scoped map[string]any    // "table.field" (lower case) → value
	plain  map[string]any    // "field" → value of the first table that has it
	names  map[string]string // "table.field" → field name as the record writes it
	from   map[string]string // table → the record its fields come from, for the log
	tables []string
}

func newKnown() *known {
	return &known{scoped: map[string]any{}, plain: map[string]any{}, names: map[string]string{}, from: map[string]string{}}
}

func (k *known) clone() *known {
	c := newKnown()
	for x, v := range k.scoped {
		c.scoped[x] = v
	}
	for x, v := range k.plain {
		c.plain[x] = v
	}
	for x, v := range k.names {
		c.names[x] = v
	}
	for x, v := range k.from {
		c.from[x] = v
	}
	c.tables = append(c.tables, k.tables...)
	return c
}

// addRec takes the simple fields of a record and where they come from.
func (k *known) addRec(r *rec) {
	k.from[r.table] = r.origin()
	k.add(r.table, r.data)
}

// use makes r the record of its table: its fields replace those of the
// record before, so a field r lacks (the id of a copy whose answer has
// none) takes no value of another record.
func (k *known) use(r *rec) {
	prefix := r.table + "."
	for key, v := range k.scoped {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		lf := key[len(prefix):]
		if p, ok := k.plain[lf]; ok && same(p, v) {
			delete(k.plain, lf)
		}
		delete(k.scoped, key)
		delete(k.names, key)
	}
	k.addRec(r)
}

// value is a field of table t as a parameter value, with its origin.
func (k *known) value(t, lf string) (pval, bool) {
	v, ok := k.scoped[t+"."+lf]
	if !ok {
		return pval{}, false
	}
	f := k.names[t+"."+lf]
	src := t + "." + f
	if from := k.from[t]; from != "" {
		src += " of " + from
	}
	return pval{v: v, table: t, field: f, src: src}, true
}

// add takes the simple fields of a record of table t.
func (k *known) add(t string, o map[string]any) {
	if !contains(k.tables, t) {
		k.tables = append(k.tables, t)
		// longer names first: "dockgroup" before "dock"
		sort.SliceStable(k.tables, func(i, j int) bool { return len(k.tables[i]) > len(k.tables[j]) })
	}
	for _, f := range sortedKeys(o) {
		v := o[f]
		if !scalar(v) {
			continue
		}
		lf := strings.ToLower(f)
		k.scoped[t+"."+lf] = v
		k.names[t+"."+lf] = f
		if _, ok := k.plain[lf]; !ok {
			k.plain[lf] = v
		}
	}
}

// replace sets a field of table t that changed (a new id); the value by
// name alone follows if it was the old one.
func (k *known) replace(t, f string, old, v any) {
	lf := strings.ToLower(f)
	if _, ok := k.scoped[t+"."+lf]; !ok {
		return
	}
	k.scoped[t+"."+lf] = v
	if p, ok := k.plain[lf]; ok && same(p, old) {
		k.plain[lf] = v
	}
}

// pval is a resolved parameter and the record field it came from.
type pval struct {
	v     any
	table string // "" for a value of the config or the spec
	field string
	// format and parts are set for a value put together from several
	// fields ("format" in "params")
	format string
	parts  []pval
	// src tells where the value comes from, for the log
	src string
}

// origin is one line per parameter: its value and where it comes from.
func origin(vals map[string]pval) []string {
	var out []string
	for _, name := range sortedKeys(vals) {
		v := vals[name]
		out = append(out, fmt.Sprintf("{%s} = %s ← %s", name, text(v.v), v.src))
	}
	return out
}

// field finds a field for a parameter: in the table the path segment in
// front of it names, in a table its name starts with (dockNumber), or
// by name alone unless the name is generic (id, code).
func (k *known) field(seg, name string) (pval, bool) { return k.fieldAt([]string{seg}, name) }

// fieldAt is field for the literal segments in front of a parameter, the
// nearest first: the first one names its table; if it names none
// (/Dock/pilot/id/{id}), a table its name starts with comes next, then
// the segments further left (Dock), then the name alone.
func (k *known) fieldAt(segs []string, name string) (pval, bool) {
	ln := strings.ToLower(name)
	try := func(t string, names ...string) (pval, bool) {
		for _, n := range names {
			if v, ok := k.value(t, n); ok {
				return v, true
			}
		}
		return pval{}, false
	}
	bySegment := func(seg string) (pval, bool) {
		if seg == "" {
			return pval{}, false
		}
		ls := strings.ToLower(seg)
		for _, t := range []string{ls, strings.TrimSuffix(ls, "s"), strings.TrimSuffix(ls, "es")} {
			names := []string{ln}
			if rest := strings.TrimPrefix(ln, t); rest != ln && rest != "" {
				names = append(names, rest)
			}
			if v, ok := try(t, names...); ok {
				return v, true
			}
		}
		return pval{}, false
	}
	if len(segs) > 0 {
		if v, ok := bySegment(segs[0]); ok {
			return v, true
		}
	}
	for _, t := range k.tables {
		if rest := strings.TrimPrefix(ln, t); rest != ln && rest != "" {
			if v, ok := try(t, ln, rest); ok {
				return v, true
			}
		}
	}
	for _, seg := range segs[min(1, len(segs)):] {
		if v, ok := bySegment(seg); ok {
			return v, true
		}
	}
	if !generic(ln) {
		if v, ok := k.plain[ln]; ok {
			for _, t := range k.tables {
				if x, ok := k.scoped[t+"."+ln]; ok && same(x, v) {
					return k.value(t, ln)
				}
			}
		}
	}
	return pval{}, false
}

// segmentsBefore are the literal segments in front of a path parameter:
// the one that names its resource first (model.SegmentBefore), then the
// others from right to left.
func segmentsBefore(path, param string) []string {
	first := model.SegmentBefore(path, param)
	out := []string{first}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	i := slices.Index(segs, "{"+param+"}")
	for j := i - 1; j >= 0; j-- {
		if seg := segs[j]; !strings.HasPrefix(seg, "{") && seg != first {
			out = append(out, seg)
		}
	}
	return out
}

func generic(name string) bool {
	return contains([]string{"id", "uuid", "key", "code", "name", "number", "nr", "no"}, name)
}

// fetched is the answer of one GET of the read phase.
type fetched struct {
	op   *spec.Operation
	url  string
	resp response
	vals map[string]pval
	tag  string // the tag whose step read it
}

// reader runs the read phase: every GET whose parameters are known, until
// no further one is.
type reader struct {
	n     namer
	ctx   context.Context
	cfg   *Config
	s     *spec.Spec
	c     *Client
	res   *Result
	ops   map[string]bool // the operations of the run
	cache map[string]response
	recs  map[string]*rec // table → its first record, the one the paths address
	// all are the records of each table: the first one, and one more for
	// every POST below a list of its own (a dock per planet and per moon)
	all map[string][]*rec
	// creates are the POSTs that create records: path → table
	creates map[string]string
	// forPath is the record a POST of that path creates
	forPath map[string]*rec
	k       *known
	gets    map[string]*fetched // operationId → its answer
	// failed are the GETs that could not be read: operationId → reason
	failed map[string]string
	// down is the error of the first request when the instance did not
	// answer at all
	down error
	// answered is set once a GET got an answer: a later error is no
	// instance that is down
	answered bool
	// tag is the tag whose step runs now, for the log
	tag string
	// volatile are the fields that changed between two reads: field →
	// operations
	volatile map[string][]string
	// order are the records in the order they were selected or loaded
	order []*rec
	// stored are the records of the last run; a table whose record is
	// selected again takes the same one
	stored map[string][]StoredRecord
	// reuse leaves out the GETs of unchanged operations whose records
	// the last run stored
	reuse bool
	needs map[string]bool
	// more is set while further records of a seed DTO are selected: an
	// element may refer to any seed record, not only the first one
	more bool
}

// get sends a GET once; a second request for the same URL takes the answer
// of the first. A probe checks a candidate for "select": an error answer
// only rejects it.
func (rd *reader) get(u, why string, vals map[string]pval, probe bool) (response, error) {
	if r, ok := rd.cache[u]; ok {
		return r, nil
	}
	var r response
	var err error
	if probe {
		r, err = rd.c.probe(rd.ctx, u, rd.tag, why, origin(vals))
	} else {
		r, err = rd.c.do(rd.ctx, http.MethodGet, u, nil, rd.tag, why, origin(vals)...)
	}
	if err != nil {
		return r, err
	}
	rd.answered = true
	rd.cache[u] = r
	return r, nil
}

// getOps are the GETs of the run in a stable order: fewer path parameters
// first.
func (rd *reader) getOps() []*spec.Operation {
	var out []*spec.Operation
	for _, op := range rd.s.Ops {
		if op.Method == http.MethodGet && (rd.ops[op.ID] || rd.isFrom(op)) {
			out = append(out, op)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.Count(out[i].Path, "{"), strings.Count(out[j].Path, "{")
		if a != b {
			return a < b
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// url fills the parameters of an operation; ok is false while a path
// parameter or a required query parameter has no value.
func (rd *reader) url(op *spec.Operation, k *known) (string, map[string]pval, bool) {
	return rd.urlOf(op, k, false)
}

// urlOf is url; with gen a path or required query parameter without value
// gets a generated one (-all).
func (rd *reader) urlOf(op *spec.Operation, k *known, gen bool) (string, map[string]pval, bool) {
	path := op.Path
	vals := map[string]pval{}
	q := url.Values{}
	for _, p := range op.Params {
		if p.In != openapi3.ParameterInPath && p.In != openapi3.ParameterInQuery {
			continue
		}
		v, ok := rd.value(op, p, k)
		if !ok && gen && (p.In == openapi3.ParameterInPath || p.Required) {
			v, ok = generatedParam(op, p)
		}
		if !ok {
			if p.In == openapi3.ParameterInPath || p.Required {
				return "", nil, false
			}
			continue
		}
		vals[p.Name] = v
		if p.In == openapi3.ParameterInPath {
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(text(v.v)))
		} else {
			q.Set(p.Name, text(v.v))
		}
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return path, vals, true
}

// value resolves one parameter: the config first, then the fields of the
// selected records, then (query only) the example, default or first enum
// value of the spec, as apitest would send them.
func (rd *reader) value(op *spec.Operation, p *openapi3.Parameter, k *known) (pval, bool) {
	var segs []string
	if p.In == openapi3.ParameterInPath {
		segs = segmentsBefore(op.Path, p.Name)
	}
	seg := ""
	if len(segs) > 0 {
		seg = segs[0]
	}
	if e, key, ok := rd.cfg.paramEntry(op.ID, p.Name); ok {
		where := fmt.Sprintf("\"params\".%s", key)
		var v pval
		switch {
		case e.Format != "":
			v, ok = rd.format(e.Format, k)
			if ok {
				var parts []string
				for _, x := range v.parts {
					parts = append(parts, x.src)
				}
				v.src = fmt.Sprintf("%s (format %q: %s)", where, e.Format, strings.Join(parts, "; "))
			}
		case e.Field != "":
			if dto, f, scoped := strings.Cut(e.Field, "."); scoped {
				// "Ship.id": the field of the record of that DTO, also for
				// generic names a path segment does not name
				v, ok = k.value(rd.n.table(dto), strings.ToLower(f))
			} else {
				v, ok = k.field(seg, e.Field)
			}
			if ok {
				v.src = fmt.Sprintf("%s (field %q): %s", where, e.Field, v.src)
			}
		default:
			v = pval{v: e.Value, src: where}
		}
		return v, ok
	}
	if v, ok := k.fieldAt(segs, p.Name); ok {
		return v, true
	}
	if p.In != openapi3.ParameterInQuery {
		return pval{}, false
	}
	switch {
	case p.Example != nil:
		return pval{v: spec.Normalize(p.Example), src: "the example of the parameter in the spec"}, true
	case p.Schema != nil && p.Schema.Value != nil && p.Schema.Value.Default != nil:
		return pval{v: spec.Normalize(p.Schema.Value.Default), src: "the default of the parameter in the spec"}, true
	case p.Required && p.Schema != nil && p.Schema.Value != nil && len(p.Schema.Value.Enum) > 0:
		return pval{v: spec.Normalize(p.Schema.Value.Enum[0]), src: "the first enum value of the parameter in the spec"}, true
	}
	return pval{}, false
}

// generatedParam is a value for a parameter no record and no config fills,
// generated from its schema as apitest-gen generates values (-all).
func generatedParam(op *spec.Operation, p *openapi3.Parameter) (pval, bool) {
	if p.Schema == nil || p.Schema.Value == nil {
		return pval{}, false
	}
	r := value.Generate(p.Schema.Value, value.Context{Seed: 1, Path: op.ID + "." + p.Name, Name: p.Name})
	if !r.OK {
		return pval{}, false
	}
	return pval{v: spec.Normalize(r.Value), src: "generated (-all): no record and no \"params\" has a value"}, true
}

// format puts a value together from fields of the selected records:
// {Dock.id} is the field id of the record of the DTO Dock, {dockCode} a
// field found by its name alone. The value belongs to the record of the
// last placeholder, so "{Planet.id}-{Dock.id}" addresses the dock.
func (rd *reader) format(format string, k *known) (pval, bool) {
	var parts []pval
	out, ok := fill(format, func(name string) (string, bool) {
		var v pval
		var ok bool
		if dto, f, scoped := strings.Cut(name, "."); scoped {
			v, ok = k.value(rd.n.table(dto), strings.ToLower(f))
		} else {
			v, ok = k.field("", name)
		}
		parts = append(parts, v)
		return text(v.v), ok
	})
	if !ok {
		return pval{}, false
	}
	last := parts[len(parts)-1]
	return pval{v: out, table: last.table, format: format, parts: parts}, true
}

// fill replaces every {name} of a format by its value; ok is false as soon
// as one has none.
func fill(format string, value func(name string) (string, bool)) (string, bool) {
	var b strings.Builder
	rest := format
	for {
		i := strings.Index(rest, "{")
		j := strings.Index(rest, "}")
		if i < 0 || j < i {
			b.WriteString(rest)
			return b.String(), true
		}
		v, ok := value(strings.TrimSpace(rest[i+1 : j]))
		if !ok {
			return "", false
		}
		b.WriteString(rest[:i] + v)
		rest = rest[j+1:]
	}
}

// read runs all GETs until no further one has all its parameters.
func (rd *reader) read() {
	rd.readOps(rd.getOps())
	rd.unread(rd.getOps())
}

// readOps runs these GETs until no further one has all its parameters;
// each one twice, to find fields that change between two reads.
func (rd *reader) readOps(ops []*spec.Operation) {
	for progress := true; progress; {
		progress = false
		for _, op := range ops {
			if rd.gets[op.ID] != nil || rd.failed[op.ID] != "" || rd.covered(op) {
				continue
			}
			u, vals, ok := rd.url(op, rd.k)
			if !ok {
				continue
			}
			progress = true
			r, err := rd.get(u, "read "+op.ID, vals, false)
			if err != nil && !rd.answered {
				rd.down = err
				return
			}
			switch {
			case err != nil:
				rd.failed[op.ID] = err.Error()
				rd.res.note(CodeFetch, op.ID, "GET %s: %v", u, err)
				continue
			case !r.ok():
				rd.failed[op.ID] = fmt.Sprintf("status %d (#%d)", r.Status, r.Seq)
				rd.res.note(CodeFetch, op.ID, "#%d GET %s answers %d%s", r.Seq, u, r.Status, short(r.Body))
				continue
			}
			f := &fetched{op: op, url: u, resp: r, vals: vals, tag: rd.tag}
			rd.gets[op.ID] = f
			rd.again(f)
			rd.used(vals)
			rd.take(f)
		}
	}
}

// seedPhase reads the records of the seed before anything else, in the
// order of "seed": each from the list "select" names in "from", else from
// the lists of its DTO, so the seed holds the record "select" chooses
// whatever the tags read before it.
func (rd *reader) seedPhase() {
	var ops []*spec.Operation
	for _, name := range rd.cfg.Seed {
		t := rd.n.table(name)
		from := rd.cfg.selection(t, rd.n).From
		for _, op := range rd.getOps() {
			items, _, list := listShape(responseSchema(op, 0))
			if !list || rd.n.of(items) != t || (from != "" && !strings.EqualFold(from, op.ID)) || slices.Contains(ops, op) {
				continue
			}
			ops = append(ops, op)
		}
	}
	if len(ops) == 0 {
		return
	}
	tag := rd.tag
	rd.tag = "seed"
	rd.readOps(ops)
	rd.tag = tag
}

// again reads a GET a second time; fields with another value are listed
// for IgnoreFields.
func (rd *reader) again(f *fetched) {
	r, err := rd.c.do(rd.ctx, http.MethodGet, f.url, nil, rd.tag, "read "+f.op.ID+" again: do fields change?", origin(f.vals)...)
	if err != nil || !r.ok() {
		return
	}
	if rd.volatile == nil {
		rd.volatile = map[string][]string{}
	}
	for _, name := range diffFields(sortLists(f.resp.Body), sortLists(r.Body), "") {
		entry := fmt.Sprintf("%s #%d/#%d", f.op.ID, f.resp.Seq, r.Seq)
		if !ignores(rd.cfg.Run.IgnoreFields, name) && !contains(rd.volatile[name], entry) {
			rd.volatile[name] = append(rd.volatile[name], entry)
		}
	}
}

// unread reports the GETs that could not be read.
func (rd *reader) unread(ops []*spec.Operation) {
	for _, op := range ops {
		if rd.gets[op.ID] != nil || rd.failed[op.ID] != "" || rd.covered(op) {
			continue
		}
		var missing []string
		for _, name := range rd.missing(op) {
			missing = append(missing, "{"+name+"}")
		}
		rd.failed[op.ID] = "no value for " + strings.Join(missing, ", ")
		rd.res.note(CodeParam, op.ID, "GET %s is not read: no record has a field for %s; set it in \"params\" (\"%s.%s\": <value> or {\"field\": \"<field>\"})",
			op.Path, strings.Join(missing, ", "), op.ID, strings.Trim(missing[0], "{}"))
	}
}

// from reports whether the record of t may be taken from the list of op:
// the one "select" names in "from", else any.
func (rd *reader) from(t string, op *spec.Operation) bool {
	f := rd.cfg.selection(t, rd.n).From
	return f == "" || strings.EqualFold(f, op.ID)
}

// isFrom reports whether "select" names op in a "from".
func (rd *reader) isFrom(op *spec.Operation) bool {
	for _, sel := range rd.cfg.Select {
		if strings.EqualFold(sel.From, op.ID) {
			return true
		}
	}
	return false
}

// used marks the fields of the records the paths address them by.
func (rd *reader) used(vals map[string]pval) {
	for _, v := range vals {
		for _, p := range append([]pval{v}, v.parts...) {
			if r := rd.recs[p.table]; r != nil && p.field != "" {
				r.keys[strings.ToLower(p.field)] = true
			}
		}
	}
}

// take selects the record of a list, or adds an object to its record.
func (rd *reader) take(f *fetched) {
	ref := responseSchema(f.op, f.resp.Status)
	if items, elems, _, ok := listOf(ref, f.resp.Body); ok {
		t := rd.n.of(items)
		// a POST at the path of the list creates its own record
		own := t != "" && rd.creates[f.op.Path] == t && rd.forPath[f.op.Path] == nil
		if t == "" || len(elems) == 0 || (rd.recs[t] != nil && !own) || (!own && !rd.from(t, f.op)) {
			return
		}
		var free []any
		for _, e := range elems {
			if o, ok := e.(map[string]any); ok && rd.taken(t, o) == nil {
				free = append(free, e)
			}
		}
		free = rd.preferred(t, free)
		i, reasons := rd.choose(t, free, rd.k, rd.k.tables, 0)
		if i < 0 {
			if rd.recs[t] == nil {
				rd.res.problem(CodeSelectNone, f.op.ID, "no %s of #%d GET %s passes \"select\": %d elements%s", t, f.resp.Seq, f.url, len(elems), list(reasons))
			}
			return
		}
		o, _ := free[i].(map[string]any)
		r := rd.selectRec(t, o, f)
		if own {
			rd.forPath[f.op.Path] = r
			r.path = f.op.Path
			return
		}
		if r == rd.recs[t] {
			rd.selectMore(t, r, free[i+1:], f)
		}
		return
	}
	t := rd.n.of(ref)
	o, ok := f.resp.Body.(map[string]any)
	if t == "" || !ok {
		return
	}
	if r := rd.recs[t]; r != nil {
		if rd.sameRecord(r, o, f.vals) {
			for k, v := range o {
				if v != nil || r.data[k] == nil { // null in a detail GET keeps the value of the list
					r.data[k] = v
				}
			}
			if !slices.Contains(r.ops, f.op.ID) {
				r.ops = append(r.ops, f.op.ID)
			}
			rd.k.add(t, o)
		}
		return
	}
	if rd.cfg.selection(t, rd.n).From != "" {
		return // the record comes from its list
	}
	if reason := rd.check(t, o, rd.k, nil, 0); reason != "" {
		rd.res.note(CodeSelectNone, f.op.ID, "#%d GET %s: the %s does not pass \"select\": %s", f.resp.Seq, f.url, t, reason)
		return
	}
	rd.selectRec(t, o, f)
}

// sameRecord reports whether an object is the record: the same id, or
// read with the values of the record.
func (rd *reader) sameRecord(r *rec, o map[string]any, vals map[string]pval) bool {
	if _, id := idOf(o); id != nil && r.id != nil {
		return r.hasID(id)
	}
	for _, v := range vals {
		if v.table == r.table {
			return true
		}
	}
	return false
}

// selectRec makes an element a record of t; the first one of a table is
// the one the paths address.
func (rd *reader) selectRec(t string, o map[string]any, f *fetched) *rec {
	data := map[string]any{}
	for k, v := range o {
		data[k] = v
	}
	r := &rec{table: t, data: data, from: f.op.ID + " " + f.url, keys: map[string]bool{}, ops: []string{f.op.ID}, seq: f.resp.Seq}
	if k, id := idOf(o); id != nil {
		r.id = id
		r.keys[strings.ToLower(k)] = true
	}
	rd.addRec(r)
	return r
}

// selectMore selects the further records of a seed DTO with "count": the
// elements after the first record that pass "select", in the order of the
// list, up to the count, every one with "*". They may refer to any record
// of the seed, not only to the first one of its table.
func (rd *reader) selectMore(t string, first *rec, elems []any, f *fetched) {
	count := rd.cfg.selection(t, rd.n).Count
	if !count.many() || !rd.cfg.seeded(t, rd.n) {
		return
	}
	rd.more = true
	defer func() { rd.more = false }()
	n := 1
	for _, e := range elems {
		if count != All && n >= int(count) {
			return
		}
		o, ok := e.(map[string]any)
		if !ok || rd.taken(t, o) != nil || rd.check(t, o, rd.k, rd.k.tables, 0) != "" {
			continue
		}
		r := rd.selectRec(t, o, f)
		r.more, r.keys = true, first.keys
		n++
	}
	if count != All && n < int(count) {
		rd.res.note(CodeSeedShort, f.op.ID, "\"count\" of the %s in \"select\" asks for %d records, #%d GET %s has %d that pass \"select\"; the seed holds these %d",
			rd.dto(t), int(count), f.resp.Seq, f.url, n, n)
	}
}

// seedRecs are the records of a seed table: the first one, then the
// further ones of "count".
func (rd *reader) seedRecs(t string) []*rec {
	first := rd.recs[t]
	if first == nil {
		return nil
	}
	out := []*rec{first}
	for _, r := range rd.all[t] {
		if r.more {
			out = append(out, r)
		}
	}
	return out
}

// inSeed reports whether a field of an element refers to a record of the
// seed of table t2 (dockId, dockCode), while further seed records are
// selected.
func (rd *reader) inSeed(t2, lf string, v any) bool {
	if !rd.more {
		return false
	}
	for _, r := range rd.seedRecs(t2) {
		f := fieldName(r.data, lf)
		if f == "" && lf == t2+"id" {
			f = fieldName(r.data, "id")
		}
		if f != "" && same(r.data[f], v) {
			return true
		}
	}
	return false
}

// addRec makes r a record of its table; the first one of a table is the
// one the paths address, unless "select" names another list in "from":
// then only a record of that list is.
func (rd *reader) addRec(r *rec) {
	t := r.table
	rd.all[t] = append(rd.all[t], r)
	rd.order = append(rd.order, r)
	from := rd.cfg.selection(t, rd.n).From
	opID, _, _ := strings.Cut(r.from, " ")
	if rd.recs[t] == nil && (from == "" || strings.EqualFold(from, opID)) {
		rd.recs[t] = r
		rd.k.addRec(r)
	}
}

// covered reports a GET the run leaves out: its operation is unchanged and
// the records it would select were stored by the last run.
func (rd *reader) covered(op *spec.Operation) bool {
	if !rd.reuse || rd.needs[op.ID] {
		return false
	}
	ref := responseSchema(op, 0)
	if items, _, ok := listShape(ref); ok {
		t := rd.n.of(items)
		if t != "" && rd.creates[op.Path] == t && rd.forPath[op.Path] == nil {
			return false // its POST creates a record of its own
		}
		return t == "" || rd.recs[t] != nil
	}
	t := rd.n.of(ref)
	return t == "" || rd.recs[t] != nil
}

// preferred puts the element the last run selected for t first, so a
// record read again (its DTO changed) stays the same one.
func (rd *reader) preferred(t string, elems []any) []any {
	for _, st := range rd.stored[t] {
		old := st.rec()
		for i, e := range elems {
			if o, ok := e.(map[string]any); ok && old.is(o) && (old.id != nil || old.matchKeys(o)) {
				return append(append([]any{e}, elems[:i]...), elems[i+1:]...)
			}
		}
	}
	return elems
}

// taken returns the record of t an element is, or nil.
func (rd *reader) taken(t string, o map[string]any) *rec {
	for _, r := range rd.all[t] {
		if r.is(o) {
			return r
		}
	}
	return nil
}

// is reports whether an object is the record: the same id, else the same
// keys the paths address it by.
func (r *rec) is(o map[string]any) bool {
	if _, id := idOf(o); id != nil && r.id != nil {
		return r.hasID(id)
	}
	n := 0
	for k := range r.keys {
		if k != "id" && fieldName(o, k) != "" {
			n++
		}
	}
	return n == 0 || r.matchKeys(o)
}

// choose returns the first element that passes, or -1 and the reasons of
// the elements it rejected.
func (rd *reader) choose(t string, elems []any, k *known, in []string, depth int) (int, []string) {
	var reasons []string
	for i, e := range elems {
		o, ok := e.(map[string]any)
		if !ok {
			continue
		}
		reason := rd.check(t, o, k, in, depth)
		if reason == "" {
			return i, nil
		}
		reasons = append(reasons, fmt.Sprintf("#%d: %s", i+1, reason))
	}
	return -1, reasons
}

// check returns why an element cannot be the record of t, "" if it can:
// the values of "params", the selected records its fields refer to (in: an
// element of a list must fit them; a single object read by its path is
// not compared), the fields of "select", its details, and the records
// below it that "select" asks for.
func (rd *reader) check(t string, o map[string]any, k *known, in []string, depth int) string {
	for name, p := range rd.cfg.Params {
		if strings.Contains(name, ".") || p.Field != "" || p.Format != "" {
			continue
		}
		if f := fieldName(o, name); f != "" && !same(o[f], p.Value) {
			return fmt.Sprintf("%s is %s, \"params\" sets %s", f, text(o[f]), text(p.Value))
		}
	}
	for _, f := range sortedKeys(o) {
		lf := strings.ToLower(f)
		for _, t2 := range in {
			if t2 == t || !strings.HasPrefix(lf, t2) || lf == t2 {
				continue
			}
			want, ok := k.scoped[t2+"."+lf]
			if !ok && lf == t2+"id" {
				want, ok = k.scoped[t2+".id"]
			}
			if ok && scalar(o[f]) && !same(o[f], want) && !rd.inSeed(t2, lf, o[f]) {
				return fmt.Sprintf("%s is %s, the selected %s has %s", f, text(o[f]), t2, text(want))
			}
		}
	}
	sel := rd.cfg.selection(t, rd.n)
	if reason := passes(o, sel.Equal, sel.Mandatory); reason != "" {
		return reason
	}
	k2 := k.clone()
	k2.add(t, o)
	k2.from[t] = fmt.Sprintf("the %s checked for \"select\"", t)
	for _, path := range sortedKeys(sel.Details) {
		op := rd.byPath(path)
		if op == nil {
			return fmt.Sprintf("details: no GET of the spec has the path %s", path)
		}
		u, vals, ok := rd.url(op, k2)
		if !ok {
			return fmt.Sprintf("details: no value for the parameters of %s", path)
		}
		r, err := rd.get(u, fmt.Sprintf("details of a %s for \"select\" (%s)", t, op.ID), vals, true)
		reason := ""
		switch {
		case err != nil:
			reason = fmt.Sprintf("GET %s: %v", u, err)
		case !r.ok():
			reason = fmt.Sprintf("#%d GET %s answers %d", r.Seq, u, r.Status)
		case !filled(r.Body):
			reason = fmt.Sprintf("#%d GET %s answers without data", r.Seq, u)
		default:
			c := sel.Details[path]
			if why := passes(r.Body, c.Equal, c.Mandatory); why != "" {
				reason = fmt.Sprintf("#%d GET %s: %s", r.Seq, u, why)
			}
		}
		rd.res.verdict(r.Seq, t, reason)
		if reason != "" {
			return reason
		}
	}
	if depth < 8 {
		if reason := rd.below(t, k, k2, depth); reason != "" {
			return reason
		}
	}
	return ""
}

// below checks the records "select" asks for that only this element makes
// readable: if a planet has no dock that passes, the next planet is
// taken.
func (rd *reader) below(t string, k, k2 *known, depth int) string {
	seen := map[string]bool{}
	for _, op := range rd.getOps() {
		if rd.gets[op.ID] != nil {
			continue
		}
		items, _, isList := listShape(responseSchema(op, 0))
		if !isList {
			continue
		}
		s := rd.n.of(items)
		sel := rd.cfg.selection(s, rd.n)
		if s == "" || s == t || seen[s] || rd.recs[s] != nil || !rd.from(s, op) || (len(sel.Equal) == 0 && len(sel.Mandatory) == 0 && len(sel.Details) == 0) {
			continue
		}
		if _, _, before := rd.url(op, k); before {
			continue
		}
		u, vals, ok := rd.url(op, k2)
		if !ok {
			continue
		}
		seen[s] = true
		r, err := rd.get(u, fmt.Sprintf("does a %s below this %s pass \"select\"? (%s)", s, t, op.ID), vals, true)
		if err != nil || !r.ok() {
			reason := fmt.Sprintf("#%d GET %s fails, so no %s can be selected", r.Seq, u, s)
			rd.res.verdict(r.Seq, t, reason)
			return reason
		}
		_, elems, _, _ := listOf(responseSchema(op, r.Status), r.Body)
		if i, reasons := rd.choose(s, elems, k2, k2.tables, depth+1); i < 0 {
			reason := fmt.Sprintf("no %s below it passes (#%d GET %s, %d elements%s)", s, r.Seq, u, len(elems), list(reasons))
			rd.res.verdict(r.Seq, t, reason)
			return reason
		}
		rd.res.verdict(r.Seq, t, "")
	}
	return ""
}

func (rd *reader) byPath(path string) *spec.Operation {
	for _, op := range rd.s.Ops {
		if op.Method == http.MethodGet && strings.EqualFold(op.Path, path) {
			return op
		}
	}
	return nil
}

// responseSchema is the JSON schema of the response with this status, else
// of the first 2xx response.
func responseSchema(op *spec.Operation, status int) *openapi3.SchemaRef {
	if op.Op.Responses == nil {
		return nil
	}
	m := op.Op.Responses.Map()
	pick := func(code string) *openapi3.SchemaRef {
		r := m[code]
		if r == nil || r.Value == nil {
			return nil
		}
		for _, mt := range sortedKeys(r.Value.Content) {
			if c := r.Value.Content[mt]; spec.IsJSON(mt) && c != nil && c.Schema != nil {
				return c.Schema
			}
		}
		return nil
	}
	if status != 0 {
		if s := pick(fmt.Sprint(status)); s != nil {
			return s
		}
	}
	for _, code := range sortedKeys(m) {
		if strings.HasPrefix(code, "2") {
			if s := pick(code); s != nil {
				return s
			}
		}
	}
	return nil
}

// list formats the first reasons of a rejection.
func list(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	if len(reasons) > 5 {
		reasons = append(reasons[:5:5], fmt.Sprintf("… %d more", len(reasons)-5))
	}
	return "\n" + strings.Join(reasons, "\n")
}
