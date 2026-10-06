package record

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/regexgen"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// exampleToken changes the unique values of a copy in the examples; the
// requests take the token of the run, so a copy of an earlier run (kept
// by a soft delete) never collides.
const exampleToken = "copy"

// copied reports whether the writes of a table go through a copy: "tables"
// has an entry for it. The run then never deletes a record it read.
func (w *writes) copied(t string) bool {
	return w.in.Config != nil && w.in.Config.table(t, w.rd.n) != nil
}

// cascades returns a table whose rows a DELETE of a record of t removes
// too (a reference with "onDelete": "CASCADE"), "" if none.
func (w *writes) cascades(t string) string {
	if w.in.Config == nil {
		return ""
	}
	for _, name := range sortedKeys(w.in.Config.Tables) {
		tb := w.in.Config.Tables[name]
		for _, f := range sortedKeys(tb.Refs) {
			if r := tb.Refs[f]; r.cascade() && w.rd.n.table(r.To) == t {
				return name
			}
		}
	}
	return ""
}

// variant is a field the copy changes: what the request sends and what the
// example shows.
type variant struct {
	field          string
	sent, example  any
	original       any
	exampleChanged bool // the example shows another value than the record
}

// copyOf is the data of a copy of r a POST can create next to r: the
// fields of every unique index and the keys the paths address r by get
// other values. It returns why there is none.
func (w *writes) copyOf(op *spec.Operation, r *rec) (map[string]any, []variant, string) {
	tb := w.in.Config.table(r.table, w.rd.n)
	var props openapi3.Schemas
	if ref := requestSchema(op); ref != nil && ref.Value != nil {
		props, _ = dict.Properties(ref.Value)
	}
	// a seed record exists in the empty environment too: there the POST
	// must not take its values either
	seed := w.in.Config.seeded(r.table, w.rd.n) && w.rd.recs[r.table] == r
	var fields []string
	add := func(k string) {
		if !contains(fields, k) {
			fields = append(fields, k)
		}
	}
	for _, u := range tb.Unique {
		var cands []string
		sets := true
		for _, f := range u.Fields {
			k := dataField(r.data, f)
			if k == "" || !filled(r.data[k]) || propOf(props, k) == nil {
				sets = false // null, or set by the server: no conflict
				break
			}
			if !tb.ref(k) {
				cands = append(cands, k)
			}
		}
		if !sets {
			continue
		}
		if len(cands) == 0 {
			return nil, nil, fmt.Sprintf("its unique index (%s) holds only references, so a copy next to the %s would violate it", strings.Join(u.Fields, ", "), r.table)
		}
		add(cands[0])
	}
	for _, k := range sortedKeys(r.keys) {
		if f := fieldName(r.data, k); f != "" && !strings.EqualFold(f, "id") && !tb.ref(f) && filled(r.data[f]) && propOf(props, f) != nil {
			add(f)
		}
	}
	cp := make(map[string]any, len(r.data))
	for k, v := range r.data {
		cp[k] = v
	}
	var out []variant
	for _, k := range fields {
		var s *openapi3.Schema
		if p := propOf(props, k); p != nil {
			s = p.Value
		}
		sent, ok := vary(r.data[k], s, w.in.Token)
		if !ok {
			return nil, nil, fmt.Sprintf("%s (%s) cannot get another value that fits its schema", k, text(r.data[k]))
		}
		v := variant{field: k, sent: sent, original: r.data[k], example: r.data[k]}
		if seed {
			v.example, _ = vary(r.data[k], s, exampleToken)
			v.exampleChanged = true
		}
		cp[k] = sent
		out = append(out, v)
	}
	return cp, out, ""
}

// child is a record the POST of a copy created below it (a crew member in
// its "crew"), as its answer shows it.
type child struct {
	table string
	id    any
	data  map[string]any
}

// childrenOf are the records with an id below the top level of the answer
// to the POST of a copy: the elements of its lists and its objects.
func (w *writes) childrenOf(op *spec.Operation, status int, body any) []child {
	o, ok := body.(map[string]any)
	if !ok {
		return nil
	}
	props := responseProps(op, status)
	var out []child
	for _, k := range sortedKeys(o) {
		var elems []any
		var ref *openapi3.SchemaRef
		p := propOf(props, k)
		switch x := o[k].(type) {
		case []any:
			elems = x
			if p != nil && p.Value != nil {
				ref = p.Value.Items
			}
		case map[string]any:
			elems, ref = []any{x}, p
		}
		t := w.rd.n.of(ref)
		if t == "" {
			t = tableName(strings.TrimSuffix(k, "s"))
		}
		for _, e := range elems {
			if m, ok := e.(map[string]any); ok {
				if _, id := idOf(m); id != nil {
					out = append(out, child{table: t, id: id, data: m})
				}
			}
		}
	}
	return out
}

// deleteChild deletes a record a copy created below it with a DELETE of
// the spec that addresses it by its id; a key it shares with a record of
// the instance (a name) could delete that one. It reports whether the
// record is gone.
func (w *writes) deleteChild(ch child, copySeq int, tag string) bool {
	r := &rec{table: ch.table, data: ch.data, id: ch.id, keys: map[string]bool{},
		note: fmt.Sprintf("the %s %s the copy #%d created", ch.table, text(ch.id), copySeq)}
	for _, op := range w.rd.s.Ops {
		if op.Method != http.MethodDelete || !names(op.Path, ch.table) {
			continue
		}
		u, vals, ok := w.urlFor(op, r)
		if !ok {
			continue
		}
		byID := false
		for _, v := range vals {
			byID = byID || (v.table == ch.table && strings.EqualFold(v.field, "id") && same(v.v, ch.id))
		}
		if !byID {
			continue
		}
		resp, err := w.rd.c.do(w.rd.ctx, http.MethodDelete, u, nil, tag,
			fmt.Sprintf("delete the %s %s the copy #%d created below it (%s)", ch.table, text(ch.id), copySeq, op.ID), origin(vals)...)
		return err == nil && (resp.ok() || resp.Status == http.StatusNotFound)
	}
	return false
}

// leave marks a record the run created and could not delete: the check
// after the writes leaves it out of the lists.
func (w *writes) leave(t string, id any) {
	if w.left == nil {
		w.left = map[string]map[string]bool{}
	}
	if w.left[t] == nil {
		w.left[t] = map[string]bool{}
	}
	w.left[t][text(id)] = true
}

// dropLeft leaves out of an answer the elements of its lists that are
// records the run created and could not delete, and rows that refer to a
// copy the run created (dockId of the copy): they are no data the writes
// changed, the copy left them. Those not named yet are kept in stray.
func (w *writes) dropLeft(v any, ref *openapi3.SchemaRef) any {
	if len(w.left) == 0 && len(w.copies) == 0 {
		return v
	}
	var s *openapi3.Schema
	if ref != nil {
		s = ref.Value
	}
	switch x := v.(type) {
	case map[string]any:
		var props openapi3.Schemas
		if s != nil {
			props, _ = dict.Properties(s)
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = w.dropLeft(e, propOf(props, k))
		}
		return out
	case []any:
		var items *openapi3.SchemaRef
		if s != nil {
			items = s.Items
		}
		t := w.rd.n.of(items)
		out := []any{}
		for _, e := range x {
			if m, ok := e.(map[string]any); ok && t != "" {
				if _, id := idOf(m); id != nil && w.left[t][text(id)] {
					continue
				}
				if of := w.ofCopy(m); of != "" {
					if w.stray == nil {
						w.stray = map[string]string{}
					}
					_, id := idOf(m)
					w.stray[t+" "+text(id)] = fmt.Sprintf("%s %s (%s)", t, text(id), of)
					continue
				}
			}
			out = append(out, w.dropLeft(e, items))
		}
		return out
	}
	return v
}

// ofCopy names the copy a row refers to ("dockId 101: the copy #8
// created"), "" if it refers to none.
func (w *writes) ofCopy(o map[string]any) string {
	for _, k := range sortedKeys(o) {
		for _, c := range w.copies {
			if c.id != nil && refTable(k) == c.table && same(o[k], c.id) {
				return fmt.Sprintf("%s %s: the copy #%d created", k, text(o[k]), c.seq)
			}
		}
	}
	return ""
}

// responseProps are the properties of the response of an operation.
func responseProps(op *spec.Operation, status int) openapi3.Schemas {
	ref := responseSchema(op, status)
	if ref == nil || ref.Value == nil {
		return nil
	}
	props, _ := dict.Properties(ref.Value)
	return props
}

// ref reports a field that refers to another record: one of "refs", an id,
// or a field like planetId.
func (t *Table) ref(field string) bool {
	if strings.EqualFold(field, "id") || refTable(field) != "" {
		return true
	}
	for f := range t.Refs {
		if norm(f) == norm(field) {
			return true
		}
	}
	return false
}

// norm is a field name without case and underscores: planet_id is
// planetId.
func norm(s string) string { return strings.ReplaceAll(strings.ToLower(s), "_", "") }

// dataField finds the key of an object for a field named as the API or as
// a column names it.
func dataField(o map[string]any, name string) string {
	if k := fieldName(o, name); k != "" {
		return k
	}
	for _, k := range sortedKeys(o) {
		if norm(k) == norm(name) {
			return k
		}
	}
	return ""
}

// vary returns another value than v that fits the schema: a text with the
// token appended (or in its last characters, or generated from its
// pattern), a number moved by an amount the token sets, a new uuid.
func vary(v any, s *openapi3.Schema, token string) (any, bool) {
	val := spec.NewValidator()
	fits := func(x any) bool { return s == nil || len(val.Validate(s, x, spec.ModePlain)) == 0 }
	h := sha256.Sum256([]byte(text(v) + "\x00" + token))
	seed := binary.BigEndian.Uint64(h[:8])
	switch x := v.(type) {
	case string:
		if s != nil && len(s.Enum) > 0 {
			return nil, false
		}
		if s != nil && s.Format == "uuid" {
			b := h[:16]
			b[6] = b[6]&0x0f | 0x40 // version 4
			b[8] = b[8]&0x3f | 0x80 // RFC 9562 variant
			u := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
			return u, fits(u)
		}
		cands := []string{x + "-" + token}
		if s != nil && s.MaxLength != nil {
			if keep := int(*s.MaxLength) - len(token) - 1; keep > 0 && keep < len(x) {
				cands = append(cands, x[:keep]+"-"+token)
			}
		}
		cands = append(cands, token, x+token)
		for _, c := range cands {
			if c != x && fits(c) {
				return c, true
			}
		}
		if s != nil && s.Pattern != "" {
			lim := regexgen.Limits{Min: int(s.MinLength), Max: -1}
			if s.MaxLength != nil {
				lim.Max = int(*s.MaxLength)
			}
			rnd := rand.New(rand.NewPCG(seed, seed>>1))
			for range 5 {
				if c, ok := regexgen.Generate(s.Pattern, lim, rnd, 200); ok && c != x && fits(c) {
					return c, true
				}
			}
		}
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return nil, false
		}
		step := int64(seed%900000) + 100000
		for _, c := range []int64{n + step, n - step, n + 1, n - 1} {
			if num := json.Number(strconv.FormatInt(c, 10)); fits(num) {
				return num, true
			}
		}
	}
	return nil, false
}

// cycle writes the record r through a copy: the POST cr creates a copy
// with other unique values, the DELETE d deletes that copy. The record
// itself stays as it is. The examples show what apitest sends in the
// empty environment: the POST the record (or, for a record of the seed,
// the copy's values for the example), the DELETE the record.
func (w *writes) cycle(d, cr *wop, r *rec) {
	cp, vars, why := w.copyOf(cr.c.Op, r)
	if why != "" {
		w.res.note(CodeBuilt, cr.c.Op.ID, "%s %s and %s %s are not sent: %s; their examples are built from the data the GETs read",
			cr.c.Op.Method, cr.c.Op.Path, d.c.Op.Method, d.c.Op.Path, why)
		d.held, cr.held = true, true
		return
	}
	post := cr
	if cr.sent || (cr.rec != nil && cr.rec != r) {
		c := *cr
		post = &c
	}
	if post.rec != r || post.url == "" {
		post.rec = r
		post.url, post.vals, _ = w.urlFor(cr.c.Op, r)
	}
	send, dropped := w.withoutChildren(cr.c.Op, cp)
	post.body = w.requestBody(cr.c.Op, send)
	var names []string
	for _, v := range vars {
		names = append(names, v.field)
	}
	post.bodySrc = "a copy of " + r.origin()
	if len(names) > 0 {
		post.bodySrc += fmt.Sprintf(" with other %s (token %q)", strings.Join(names, ", "), w.in.Token)
	}
	if len(dropped) > 0 {
		var what []string
		for _, f := range sortedKeys(dropped) {
			what = append(what, dropped[f])
		}
		post.bodySrc += fmt.Sprintf(", without %s: the server would create them for the copy, and a DELETE that only marks the copy leaves them",
			strings.Join(what, ", "))
	}
	reason := fmt.Sprintf("create a copy of the %s, %s deletes it", r.table, d.c.Op.ID)
	if len(names) > 0 {
		reason = fmt.Sprintf("create a copy of the %s with other %s, %s deletes it", r.table, strings.Join(names, ", "), d.c.Op.ID)
	}
	resp, ok := w.send(post, http.MethodPost, reason)
	if !ok {
		w.conflict(post, resp, r, false)
		return
	}
	// the copy has the values the POST sent and answered, never the id of
	// the record: a DELETE must not reach the record through it
	idField := fieldName(r.data, "id") // a number or a uuid
	base := make(map[string]any, len(cp))
	for k, v := range cp {
		if k != idField {
			base[k] = v
		}
	}
	made := &rec{table: r.table, data: base, keys: r.keys, note: fmt.Sprintf("the copy #%d POST %s created", resp.Seq, post.url), seq: resp.Seq}
	if o, ok := resp.Body.(map[string]any); ok {
		made.data, _ = fillIn(o, base).(map[string]any)
	}
	if _, id := idOf(made.data); id != nil {
		made.id = id
	} else if idField != "" && fieldName(made.data, "id") == "" && r.from != "" {
		// an answer without id: the list the record came from shows it,
		// found by the values the copy changed, which tell it from the record
		keys := map[string]bool{}
		for k := range r.keys {
			keys[k] = true
		}
		for _, v := range vars {
			keys[strings.ToLower(v.field)] = true
		}
		if id := w.findID(r, made.data, keys, post); id != nil {
			made.id, made.data[idField] = id, id
			made.note = fmt.Sprintf("the copy #%d POST %s created (its id from the list %s)", resp.Seq, post.url, r.from[strings.Index(r.from, " ")+1:])
		}
	}
	w.copies = append(w.copies, made)
	if post == cr {
		w.asRecord(post, r, cp, vars, dropped)
	}
	if made.id != nil {
		w.leave(r.table, made.id) // a list that shows deleted rows shows the copy
	}
	u, vals, ok := w.urlFor(d.c.Op, made)
	orig, realVals, _ := w.urlFor(d.c.Op, r)
	if !ok || u == orig {
		w.res.problem(CodeCopyLeft, d.c.Op.ID, "#%d created a copy of the %s, but %s cannot address it apart from the record; delete the copy by hand: %s",
			resp.Seq, r.table, d.c.Op.Path, clip(text(resp.Body)))
		return
	}
	// the records the copy created below it (its crew) go first: a DELETE
	// that only marks the copy (soft delete) leaves them
	var left []string
	for _, ch := range w.rowsOf(made, w.childrenOf(cr.c.Op, resp.Status, resp.Body), post.tag) {
		if !w.deleteChild(ch, resp.Seq, post.tag) {
			w.leave(ch.table, ch.id)
			left = append(left, ch.table+" "+text(ch.id))
		}
	}
	if tb := w.in.Config.table(r.table, w.rd.n); len(left) > 0 && tb != nil && tb.SoftDelete {
		w.res.problem(CodeCopyLeft, cr.c.Op.ID, "#%d created a copy of the %s with records below it that no DELETE of the spec removes by their id, "+
			"and the DELETE of the copy only marks it (soft delete), so they stay: %s; delete them by hand, or let the service delete them with the copy. "+
			"The check after the writes leaves them out", resp.Seq, r.table, strings.Join(left, ", "))
	}
	del := d
	if d.sent {
		c := *d
		del = &c
	}
	keep := del.vals
	del.url, del.vals = u, vals // the log shows the values that address the copy
	_, ok = w.send(del, http.MethodDelete, fmt.Sprintf("delete the copy #%d created", resp.Seq))
	del.vals = keep
	if !ok {
		w.res.problem(CodeCopyLeft, d.c.Op.ID, "#%d DELETE %s did not delete the copy #%d created; delete it by hand", del.seq, u, resp.Seq)
		return
	}
	if del == d && (d.rec == nil || d.rec == r) {
		d.rec, d.vals = r, realVals // the example deletes the record
		if d.vals == nil {
			d.vals = vals
		}
	}
}

// asRecord turns the answer to the copy into the example of the POST: the
// body and the answer show the record (a seed record: the example values
// of the copy), with the id of the record.
func (w *writes) asRecord(x *wop, r *rec, cp map[string]any, vars []variant, dropped map[string]string) {
	data := make(map[string]any, len(cp))
	for k, v := range cp {
		data[k] = v
	}
	seed := false
	for _, v := range vars {
		data[v.field] = v.example
		seed = seed || v.exampleChanged
	}
	x.body = w.requestBody(x.c.Op, data)
	o, ok := x.resp.Body.(map[string]any)
	if !ok {
		return
	}
	out := make(map[string]any, len(o)) // the log keeps the answer as it came
	for k, v := range o {
		out[k] = v
	}
	for _, v := range vars {
		if f := fieldName(out, v.field); f != "" && same(out[f], v.sent) {
			out[f] = v.example
		}
	}
	// the lists the copy was sent without: the POST of apitest sends them
	for f := range dropped {
		out[f] = r.data[f]
	}
	resp := *x.resp
	resp.Body = out
	x.resp = &resp
	if seed {
		// the environment holds the seed record: the POST creates another
		// one, with the next id of its table
		x.rec = nil
		return
	}
	if f, _ := idOf(r.data); f != "" && r.id != nil {
		out[f] = r.id // also into an answer without id
	}
}

// conflict reports a POST the instance rejected; one that violates a
// unique index gets the cause and what to change.
func (w *writes) conflict(x *wop, resp response, r *rec, recreated bool) {
	body := strings.ToLower(text(resp.Body))
	if resp.Status != http.StatusConflict && !strings.Contains(body, "duplicate") && !strings.Contains(body, "23505") && !strings.Contains(body, "unique") {
		return
	}
	tb := w.in.Config.table(r.table, w.rd.n)
	name := r.table
	if tb != nil && tb.Name != "" {
		name = tb.Name
	}
	hint := fmt.Sprintf("give the unique indexes of %s in \"tables\" (\"tables\": {\"%s\": {\"unique\": [{\"fields\": [...]}]}}), so the run creates a copy with other values", r.table, w.rd.dto(r.table))
	if tb != nil {
		hint = "a unique field of the table is missing in \"tables\".unique"
	}
	if recreated {
		hint = fmt.Sprintf("the DELETE before only marked the row as deleted (soft delete) and the unique index still counts it. "+
			"Restore it (e.g. UPDATE %s SET deleted_at = NULL WHERE id = %s), make the index partial (WHERE deleted_at IS NULL; GORM: where:deleted_at IS NULL, under a new index name) and %s",
			name, text(r.id), hint)
	}
	w.res.problem(CodeConflict, x.c.Op.ID, "#%d %s %s answers %d: it violates a unique index; %s", resp.Seq, x.c.Op.Method, x.url, resp.Status, hint)
}

// softUnique reports, before any write, the unique indexes that count the
// rows a soft delete keeps: a POST with the values of a deleted record
// fails there.
func (w *writes) softUnique() {
	if w.in.Config == nil {
		return
	}
	for _, name := range sortedKeys(w.in.Config.Tables) {
		tb := w.in.Config.Tables[name]
		if !tb.SoftDelete {
			continue
		}
		for _, u := range tb.Unique {
			if u.ignoresDeleted() {
				continue
			}
			idx := strings.Join(u.Fields, ", ")
			if u.Name != "" {
				idx = u.Name + " (" + idx + ")"
			}
			w.res.note(CodeSoftUnique, name, "the unique index %s counts the rows a DELETE only marks as deleted: a POST with the values of a deleted %s fails (duplicate key). "+
				"The run writes %s through copies with other values; in the service make the index partial: WHERE deleted_at IS NULL (GORM: where:deleted_at IS NULL, under a new index name)",
				idx, name, name)
		}
	}
}

// withoutChildren leaves out of the data of a copy the lists of records
// its request schema does not require (crew): the server would create them
// for the copy too, n more rows, and a DELETE that only marks the copy
// (soft delete) leaves them. A required list keeps its first minItems
// elements, at least one. It returns the data to send and the lists cut:
// field → what was left out.
func (w *writes) withoutChildren(op *spec.Operation, data map[string]any) (map[string]any, map[string]string) {
	ref := requestSchema(op)
	if ref == nil || ref.Value == nil {
		return data, nil
	}
	props, req := dict.Properties(ref.Value)
	out := make(map[string]any, len(data))
	dropped := map[string]string{}
	for _, k := range sortedKeys(data) {
		out[k] = data[k]
		l, ok := data[k].([]any)
		p := propOf(props, k)
		if !ok || len(l) == 0 || p == nil || p.Value == nil || p.Value.Items == nil || p.Value.Items.Value == nil || isPrimitive(p.Value.Items.Value) {
			continue
		}
		required := slices.ContainsFunc(req, func(r string) bool { return strings.EqualFold(r, k) })
		if !required && p.Value.MinItems == 0 {
			delete(out, k)
			dropped[k] = fmt.Sprintf("its %s (%d)", k, len(l))
			continue
		}
		if n := max(1, int(p.Value.MinItems)); n < len(l) {
			out[k] = l[:n]
			dropped[k] = fmt.Sprintf("%d of its %d %s", len(l)-n, len(l), k)
		}
	}
	return out, dropped
}

// rowsOf adds to the records the answer to the POST of a copy shows below
// it the rows the server created for the copy without showing them (an
// audit row): the elements of every list read that refer to the copy
// (dockId of the copy), read again now.
func (w *writes) rowsOf(made *rec, shown []child, tag string) []child {
	if made.id == nil {
		return shown
	}
	seen := map[string]bool{}
	for _, ch := range shown {
		seen[ch.table+" "+text(ch.id)] = true
	}
	urls := map[string]bool{}
	ids := sortedKeys(w.rd.gets)
	slices.SortStableFunc(ids, func(a, b string) int { return w.rd.gets[a].resp.Seq - w.rd.gets[b].resp.Seq })
	for _, id := range ids {
		f := w.rd.gets[id]
		items, _, ok := listShape(responseSchema(f.op, f.resp.Status))
		if !ok || items == nil || items.Value == nil || urls[f.url] {
			continue
		}
		t := w.rd.n.of(items)
		props, _ := dict.Properties(items.Value)
		refers := ""
		for _, k := range sortedKeys(props) {
			if refTable(k) == made.table {
				refers = k
			}
		}
		if t == "" || t == made.table || refers == "" {
			continue
		}
		urls[f.url] = true
		resp, err := w.rd.c.do(w.rd.ctx, http.MethodGet, f.url, nil, tag,
			fmt.Sprintf("find the %s rows the copy #%d created without showing them", t, made.seq))
		if err != nil || !resp.ok() {
			continue
		}
		_, elems, _, _ := listOf(responseSchema(f.op, resp.Status), resp.Body)
		for _, e := range elems {
			o, ok := e.(map[string]any)
			if !ok {
				continue
			}
			k := fieldName(o, refers)
			_, cid := idOf(o)
			if k == "" || cid == nil || !same(o[k], made.id) || seen[t+" "+text(cid)] {
				continue
			}
			seen[t+" "+text(cid)] = true
			shown = append(shown, child{table: t, id: cid, data: o})
		}
	}
	return shown
}
