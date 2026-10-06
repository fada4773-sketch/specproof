package record

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
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
	post.body = w.requestBody(cr.c.Op, cp)
	var names []string
	for _, v := range vars {
		names = append(names, v.field)
	}
	post.bodySrc = "a copy of " + r.origin()
	if len(names) > 0 {
		post.bodySrc += fmt.Sprintf(" with other %s (token %q)", strings.Join(names, ", "), w.in.Token)
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
	made := &rec{table: r.table, data: cp, keys: r.keys, note: fmt.Sprintf("the copy #%d POST %s created", resp.Seq, post.url)}
	if o, ok := resp.Body.(map[string]any); ok {
		made.data, _ = fillIn(o, cp).(map[string]any)
	}
	if _, id := idOf(made.data); id != nil {
		made.id = id
	}
	if post == cr {
		w.asRecord(post, r, cp, vars)
	}
	u, vals, ok := w.urlFor(d.c.Op, made)
	orig, realVals, _ := w.urlFor(d.c.Op, r)
	if !ok || u == orig {
		w.res.problem(CodeCopyLeft, d.c.Op.ID, "#%d created a copy of the %s, but %s cannot address it apart from the record; delete the copy by hand: %s",
			resp.Seq, r.table, d.c.Op.Path, clip(text(resp.Body)))
		return
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
func (w *writes) asRecord(x *wop, r *rec, cp map[string]any, vars []variant) {
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
	resp := *x.resp
	resp.Body = out
	x.resp = &resp
	if seed {
		// the environment holds the seed record: the POST creates another
		// one, with the next id of its table
		x.rec = nil
		return
	}
	if f, _ := idOf(out); f != "" && r.id != nil {
		out[f] = r.id
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
