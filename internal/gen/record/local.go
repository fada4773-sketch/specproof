package record

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Kinds of writing operations.
const (
	kindUpdate = "update" // PUT or PATCH of a record or of a part of it
	kindCreate = "create" // POST that creates a record
	kindDelete = "delete"
	kindAction = "action" // POST that does something else
	kindQuery  = "query"  // POST that only reads (GetShipsAsTable, SearchShips)
)

// wop is a writing operation of the run.
type wop struct {
	c        *cases.Case
	tag      string
	kind     string
	table    string // the record it writes
	rec      *rec   // that record; nil if none was read
	part     bool   // writes a part of the record (/Dock/{code}/Config)
	resolved bool
	url      string // "" while a parameter has no value
	vals     map[string]pval
	body     any       // the request body
	resp     *response // the answer; nil if it was not sent and not built
	built    bool      // built from the data the GETs read, not sent
	sent     bool      // sent and answered with 2xx (a DELETE also with 404)
	seq      int       // the number of its request in the log
	failed   bool      // sent and rejected
	why      string    // why it has no record, for the notes
}

// writes runs the writing operations against the instance.
type writes struct {
	rd    *reader
	in    Input
	res   *Result
	needs map[string]bool
	ops   []*wop
	byOp  map[string]*wop
	retry map[*rec][]*wop // updates answered 404, sent again after the POST
	later []*wop          // writes without a value for a parameter yet
	late  bool            // the late reads are done: nothing waits any more
}

// classify takes the writing cases of the run, in its order.
func (w *writes) classify(run []*cases.Case) {
	w.byOp = map[string]*wop{}
	w.retry = map[*rec][]*wop{}
	for _, c := range run {
		op := c.Op
		if op.Method == http.MethodGet || w.byOp[op.ID] != nil {
			continue
		}
		x := &wop{c: c, tag: c.Group}
		switch op.Method {
		case http.MethodPost:
			x.kind = kindAction
			if t := createTable(op, w.rd.n); t != "" {
				x.kind, x.table = kindCreate, t
			} else if query(op) {
				x.kind = kindQuery
			}
		case http.MethodDelete:
			x.kind = kindDelete
		default:
			x.kind = kindUpdate
		}
		w.ops = append(w.ops, x)
		w.byOp[op.ID] = x
	}
}

// resolve finds the record an operation writes and its URL, once the GETs
// of its tag have been read. An operation below a record of its own POST
// (/Planet/{p}/Dock/{dockCode}/Ship/{shipCode} below POST /Planet/{p}/Dock/{dockCode}/Ship)
// writes that record, not the first one of the table.
func (w *writes) resolve(x *wop) {
	if x.resolved {
		return
	}
	x.resolved = true
	op := x.c.Op
	switch x.kind {
	case kindCreate:
		x.rec = w.rd.forPath[op.Path]
		switch {
		case x.rec != nil:
		case w.ownList(op.Path):
			x.why = fmt.Sprintf("its list GET %s holds no %s that is not taken already", op.Path, x.table)
		case recreates(op, x.table):
			x.rec = w.rd.recs[x.table]
		default:
			x.why = "it copies or derives a record (" + op.Path + ")"
		}
	case kindUpdate, kindDelete:
		if last := lastParam(op.Path); last != "" {
			if _, vals, ok := w.rd.url(op, w.rd.k); ok {
				x.table = vals[last].table
			}
		}
		if x.table == "" {
			x.table = w.rd.n.of(requestSchema(op))
		}
		if x.table == "" {
			x.table = w.rd.n.of(responseSchema(op, 0))
		}
		x.part = !strings.HasSuffix(op.Path, "}")
		x.rec, x.why = w.recFor(op, x.table)
	}
	k := w.rd.k
	if x.rec != nil && x.rec != w.rd.recs[x.table] {
		k = k.clone()
		k.add(x.table, x.rec.data)
	}
	x.url, x.vals, _ = w.rd.url(op, k)
	if x.why == "" && x.url == "" {
		x.why = "a parameter of " + op.Path + " has no value"
	}
}

// recFor is the record an update or a DELETE addresses: the one of the
// longest POST path its path continues, else the first one of the table.
func (w *writes) recFor(op *spec.Operation, t string) (*rec, string) {
	best := ""
	for path, ct := range w.rd.creates {
		if ct == t && strings.HasPrefix(op.Path, path+"/") && len(path) > len(best) {
			best = path
		}
	}
	if best != "" && w.ownList(best) {
		if r := w.rd.forPath[best]; r != nil {
			return r, ""
		}
		return nil, fmt.Sprintf("the list GET %s holds no %s of its own", best, t)
	}
	if r := w.rd.recs[t]; r != nil {
		return r, ""
	}
	return nil, "no " + t + " was read"
}

// ownList reports whether a GET lists the records at the path of a POST.
func (w *writes) ownList(path string) bool {
	op := w.rd.byPath(path)
	if op == nil {
		return false
	}
	_, _, ok := listShape(responseSchema(op, 0))
	return ok
}

// urlFor fills the parameters of an operation for one record r of its
// table: the fields of r, the other values as known.
func (w *writes) urlFor(op *spec.Operation, r *rec) (string, map[string]pval, bool) {
	k := w.rd.k
	if r != w.rd.recs[r.table] {
		k = k.clone()
		k.add(r.table, r.data)
	}
	return w.rd.url(op, k)
}

// remover finds a DELETE that removes the record r, so a POST can create it
// again: the DELETE of r itself, else one "select" names in "delete", else
// any DELETE of its table whose parameters the fields of r fill
// (/Ship/id/{id} for a ship of /Planet/{p}/Dock/{d}/Ship).
func (w *writes) remover(t string, r *rec) (*wop, string, map[string]pval) {
	var own, chosen, other []*wop
	for _, x := range w.ops {
		if x.kind != kindDelete || x.c.Example != cases.DefaultExample {
			continue
		}
		w.resolve(x)
		switch {
		case x.table != t:
		case x.rec == r && x.url != "":
			own = append(own, x)
		case contains(w.rd.cfg.selection(t, w.rd.n).Delete, x.c.Op.ID):
			chosen = append(chosen, x)
		default:
			other = append(other, x)
		}
	}
	for _, x := range append(append(own, chosen...), other...) {
		if x.rec == r && x.url != "" {
			return x, x.url, x.vals
		}
		if u, vals, ok := w.urlFor(x.c.Op, r); ok {
			return x, u, vals
		}
	}
	return nil, "", nil
}

// creator finds a POST that creates the record r again: the POST of r
// itself, else one of its table at a path that is not the list of another
// record.
func (w *writes) creator(t string, r *rec) *wop {
	var other *wop
	for _, x := range w.ops {
		if x.kind != kindCreate || x.c.Example != cases.DefaultExample {
			continue
		}
		w.resolve(x)
		switch {
		case x.table != t:
		case x.rec == r:
			return x
		case other == nil && r.path == "" && w.rd.forPath[x.c.Op.Path] == nil:
			if _, _, ok := w.urlFor(x.c.Op, r); ok {
				other = x
			}
		}
	}
	return other
}

// createTable returns the table a POST creates a record in: the DTO of its
// body or of its answer, if a segment of its path names it; "" for a POST
// that does something else.
func createTable(op *spec.Operation, n namer) string {
	if query(op) {
		return ""
	}
	for _, ref := range []*openapi3.SchemaRef{requestSchema(op), responseSchema(op, 0)} {
		if t := n.of(ref); t != "" && names(op.Path, t) {
			return t
		}
	}
	return ""
}

// query reports a POST that only reads, by its name: GetShipsAsTable,
// SearchShips, FindDocks.
func query(op *spec.Operation) bool {
	id := strings.ToLower(op.ID)
	for _, p := range []string{"get", "list", "search", "find", "query", "filter"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// recreates reports whether a POST can create a record again: its path ends
// with the collection (/Planet/{code}/Dock) or with a key of the record
// above (/Dock/Planet/{code}); not /Dock/{id}/clone or /docks/create.
func recreates(op *spec.Operation, t string) bool {
	segs := strings.Split(strings.Trim(op.Path, "/"), "/")
	last := segs[len(segs)-1]
	return strings.HasPrefix(last, "{") || names("/"+last, t)
}

// names reports whether a literal segment of the path names the table.
func names(path, t string) bool {
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if strings.HasPrefix(seg, "{") {
			continue
		}
		s := strings.ToLower(seg)
		if s == t || strings.TrimSuffix(s, "s") == t || strings.TrimSuffix(s, "es") == t || tableName(seg) == t {
			return true
		}
	}
	return false
}

func lastParam(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if s := segs[i]; strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			return s[1 : len(s)-1]
		}
	}
	return ""
}

// of returns the writes of one kind of a tag in the order of the run.
func (w *writes) of(tag, kind string) []*wop {
	var out []*wop
	for _, x := range w.ops {
		if x.tag == tag && x.kind == kind && x.c.Example == cases.DefaultExample {
			out = append(out, x)
		}
	}
	return out
}

// queries sends the POSTs of a tag that only read, with a body built from
// the values the GETs read; they belong to the GET step.
func (w *writes) queries(tag string) {
	for _, x := range w.of(tag, kindQuery) {
		if !w.needs[x.c.Op.ID] {
			continue
		}
		w.resolve(x)
		if x.url == "" {
			continue
		}
		body, missing := w.queryBody(x.c.Op)
		if len(missing) > 0 {
			w.res.note(CodeNotExecuted, x.c.Op.ID, "POST %s is not sent: no value read for its required fields %s", x.c.Op.Path, strings.Join(missing, ", "))
			continue
		}
		x.body = body
		w.send(x, http.MethodPost, "read "+x.c.Op.ID+" (a POST that only reads)")
	}
}

// queryBody fills the fields of the body of a query with the values of the
// selected records (planetCode, dockCode); it returns the required
// fields without a value.
func (w *writes) queryBody(op *spec.Operation) (map[string]any, []string) {
	body := map[string]any{}
	ref := requestSchema(op)
	if ref == nil || ref.Value == nil {
		return body, nil
	}
	props, req := dict.Properties(ref.Value)
	for _, k := range sortedKeys(props) {
		if v, ok := w.rd.k.field("", k); ok {
			body[k] = v.v
		}
	}
	var missing []string
	for _, k := range req {
		if _, ok := body[k]; !ok {
			missing = append(missing, k)
		}
	}
	return body, missing
}

// tagWrites sends the writes of one tag: first every PUT and PATCH, then the
// DELETEs that make room for a POST, then the POSTs. A record that a DELETE
// removed is created again with the same data, so the instance holds it
// again (with a new id).
func (w *writes) tagWrites(tag string) {
	var xs []*wop
	for _, x := range w.ops {
		if x.tag == tag && x.c.Example == cases.DefaultExample {
			xs = append(xs, x)
		}
	}
	w.writeAll(xs)
}

// writeAll sends writes in this order: every PUT and PATCH, then each
// DELETE followed by the POST that creates its record again, then the POSTs
// whose record is still there, each after a DELETE that frees its key.
func (w *writes) writeAll(xs []*wop) {
	for _, u := range kindOf(xs, kindUpdate) {
		if w.needs[u.c.Op.ID] {
			w.update(u)
		}
	}
	for _, d := range kindOf(xs, kindDelete) {
		if !w.needs[d.c.Op.ID] || d.sent || d.failed {
			continue
		}
		w.resolve(d)
		if d.rec == nil || d.url == "" {
			if !w.late {
				w.later = append(w.later, d)
				continue
			}
			w.res.note(CodeNotExecuted, d.c.Op.ID, "DELETE %s is not sent: %s", d.c.Op.Path, d.why)
			continue
		}
		cr := w.creator(d.table, d.rec)
		if cr == nil {
			w.res.note(CodeBuilt, d.c.Op.ID, "DELETE %s is not sent: no POST of the spec creates the %s again, its data would be lost; its example is built from the %s of %s",
				d.c.Op.Path, d.table, d.table, d.rec.from)
			continue
		}
		w.recreate(d, d.url, d.vals, cr, d.rec)
	}
	for _, cr := range kindOf(xs, kindCreate) {
		if !w.needs[cr.c.Op.ID] || cr.sent || cr.failed {
			continue
		}
		w.resolve(cr)
		if cr.rec == nil || cr.url == "" {
			if cr.url == "" && !w.late {
				w.later = append(w.later, cr)
				continue
			}
			w.res.note(CodeBuilt, cr.c.Op.ID, "POST %s is not sent: %s; its example is built from the data the GETs read", cr.c.Op.Path, cr.why)
			continue
		}
		d, u, vals := w.remover(cr.table, cr.rec)
		if d == nil {
			w.res.note(CodeBuilt, cr.c.Op.ID, "POST %s is not sent: no DELETE of the spec removes a %s, so the POST would conflict with it; its body is the answer of %s",
				cr.c.Op.Path, cr.table, cr.rec.from)
			continue
		}
		w.recreate(d, u, vals, cr, cr.rec)
	}
	for _, x := range kindOf(xs, kindAction) {
		if w.needs[x.c.Op.ID] {
			w.resolve(x)
			if r := w.rd.recs[w.rd.n.of(requestSchema(x.c.Op))]; r != nil && x.url != "" {
				w.res.note(CodeBuilt, x.c.Op.ID, "POST %s is not sent: it creates no record the run can create again; its body is the %s of %s", x.c.Op.Path, r.table, r.from)
				continue
			}
			w.res.note(CodeNotExecuted, x.c.Op.ID, "POST %s is not sent: it creates no record the run can create again, and no record read fits its body; it gets no example", x.c.Op.Path)
		}
	}
}

// update sends a PUT or PATCH. One whose record a GET of a later tag reads
// (a ship listed below its dock) waits for the late reads.
func (w *writes) update(u *wop) {
	w.resolve(u)
	if u.url == "" && !w.late {
		w.later = append(w.later, u)
		return
	}
	if u.url == "" || (u.rec == nil && w.sameGet(u) == nil) {
		w.res.note(CodeNotExecuted, u.c.Op.ID, "%s %s is not sent: %s", u.c.Op.Method, u.c.Op.Path, u.why)
		return
	}
	u.body = w.bodyFor(u)
	resp, ok := w.send(u, u.c.Op.Method, "update "+u.c.Op.ID+" with the data it has")
	if !ok && resp.Status == http.StatusNotFound && u.rec != nil {
		w.retry[u.rec] = append(w.retry[u.rec], u)
	}
}

// lateWrites sends the writes that waited for the GETs of later tags, in
// the order of the tags: updates first, then DELETEs and POSTs.
func (w *writes) lateWrites() {
	w.late = true
	later := w.later
	w.later = nil
	for _, x := range later {
		x.resolved, x.why = false, ""
	}
	w.writeAll(later)
}

// kindOf are the writes of one kind.
func kindOf(xs []*wop, kind string) []*wop {
	var out []*wop
	for _, x := range xs {
		if x.kind == kind {
			out = append(out, x)
		}
	}
	return out
}

// recreate deletes the record r with the DELETE d at u and creates it
// again with the POST cr and the data of r, so the instance holds it again
// (with a new id). d and cr keep the answers for their examples when they
// address r; for another record of the table they are sent as copies.
func (w *writes) recreate(d *wop, u string, vals map[string]pval, cr *wop, r *rec) {
	del := d
	if d.sent || d.url != u {
		c := *d
		c.url, c.vals = u, vals
		del = &c
	}
	resp, ok := w.send(del, http.MethodDelete, fmt.Sprintf("delete the %s, %s creates it again", r.table, cr.c.Op.ID))
	if !ok && resp.Status != http.StatusNotFound {
		w.res.problem(CodeWriteFailed, d.c.Op.ID, "#%d DELETE %s answers %d, so %s is not sent; their examples are built from the data the GETs read",
			resp.Seq, u, resp.Status, cr.c.Op.ID)
		return
	}
	del.sent, del.failed = true, false
	if !d.sent {
		d.sent, d.seq, d.resp = true, del.seq, del.resp
		if d.url == "" || d.rec == nil {
			d.url, d.vals, d.rec = u, vals, r
		}
	}
	post := cr
	if cr.sent || (cr.rec != nil && cr.rec != r) {
		c := *cr
		post = &c
	}
	if post.rec == nil || post.rec != r {
		post.rec = r
		post.url, post.vals, _ = w.urlFor(cr.c.Op, r)
	}
	post.body = project(r.data, requestSchema(cr.c.Op), spec.ModeRequest)
	if resp, ok := w.send(post, http.MethodPost, fmt.Sprintf("create the %s again that #%d deleted (%s)", r.table, del.seq, cr.c.Op.ID)); !ok {
		w.res.problem(CodeWriteFailed, cr.c.Op.ID, "the %s deleted by #%d is missing in the instance now; create it again with the body of #%d (%s)",
			r.table, del.seq, resp.Seq, clip(text(post.body)))
		return
	}
	w.newID(r, post)
	for _, u := range w.retry[r] {
		u.resolved = false
		w.resolve(u)
		w.send(u, u.c.Op.Method, "update "+u.c.Op.ID+" again, now that the record exists")
	}
	delete(w.retry, r)
}

// send sends one write and keeps the answer.
func (w *writes) send(x *wop, method, why string) (response, bool) {
	resp, err := w.rd.c.do(w.rd.ctx, method, x.url, x.body, x.tag, why)
	x.seq = resp.Seq
	if err != nil {
		x.failed = true
		w.res.problem(CodeWriteFailed, x.c.Op.ID, "#%d %s %s: %v", resp.Seq, method, x.url, err)
		return resp, false
	}
	if !resp.ok() {
		if method != http.MethodDelete || resp.Status != http.StatusNotFound {
			x.failed = true
			w.res.problem(CodeWriteFailed, x.c.Op.ID, "#%d %s %s answers %d%s", resp.Seq, method, x.url, resp.Status, short(resp.Body))
		}
		return resp, false
	}
	x.resp, x.sent, x.failed = &resp, true, false
	return resp, true
}

func short(v any) string {
	if v == nil {
		return ""
	}
	return ": " + clip(text(v))
}

// newID finds the id the instance gave the record it created again: in
// the answer of the POST, else by reading the list it came from.
func (w *writes) newID(r *rec, cr *wop) {
	if o, ok := cr.resp.Body.(map[string]any); ok {
		if _, id := idOf(o); id != nil {
			r.newID = id
		}
	}
	if r.newID == nil && r.id != nil {
		from := r.from[strings.Index(r.from, " ")+1:]
		if resp, err := w.rd.c.do(w.rd.ctx, http.MethodGet, from, nil, cr.tag, "find the new id of the "+r.table+" #"+fmt.Sprint(cr.seq)+" created"); err == nil && resp.ok() {
			elems := []any{resp.Body}
			if l, ok := resp.Body.([]any); ok {
				elems = l
			} else if o, ok := resp.Body.(map[string]any); ok {
				for _, v := range o {
					if l, ok := v.([]any); ok {
						elems = l
					}
				}
			}
			for _, e := range elems {
				if o, ok := e.(map[string]any); ok && r.matchKeys(o) {
					_, r.newID = idOf(o)
				}
			}
		}
	}
	if r.newID != nil && r == w.rd.recs[r.table] {
		idField, _ := idOf(r.data)
		w.rd.k.add(r.table, map[string]any{idField: r.newID})
	}
}

// matchKeys reports whether an object has the keys the paths address the
// record by, apart from the id.
func (r *rec) matchKeys(o map[string]any) bool {
	n := 0
	for k := range r.keys {
		if k == "id" {
			continue
		}
		f, g := fieldName(o, k), fieldName(r.data, k)
		if f == "" || g == "" {
			continue
		}
		if !same(o[f], r.data[g]) {
			return false
		}
		n++
	}
	return n > 0
}

// sameGet is the GET of the path of an update, if the run read it.
func (w *writes) sameGet(u *wop) *fetched {
	for _, f := range w.rd.gets {
		if f.op.Path == u.c.Op.Path {
			return f
		}
	}
	return nil
}

// bodyFor is the body of an update: what the GET of the same path answers,
// else the record, with the fields of the request schema.
func (w *writes) bodyFor(u *wop) any {
	src := u.rec.dataOrNil()
	if f := w.sameGet(u); f != nil {
		src = f.resp.Body
	}
	return project(src, requestSchema(u.c.Op), spec.ModeRequest)
}

func (r *rec) dataOrNil() any {
	if r == nil {
		return nil
	}
	return r.data
}

// offline builds what the writes not sent would answer, from the data the
// GETs read.
func (w *writes) offline() {
	for _, x := range w.ops {
		if x.resp != nil || x.built || x.c.Example != cases.DefaultExample {
			continue
		}
		w.resolve(x)
		r := x.rec
		if r == nil && x.kind == kindCreate {
			r = w.rd.recs[x.table] // a POST that copies a record: the GET answer is its body
		}
		switch x.kind {
		case kindCreate:
			if r == nil {
				continue
			}
			x.body = project(r.data, requestSchema(x.c.Op), spec.ModeRequest)
			x.resp = &response{Status: successStatus(x.c.Op), Body: project(r.data, responseSchema(x.c.Op, 0), spec.ModeResponse)}
		case kindUpdate:
			if r == nil {
				continue
			}
			x.body = w.bodyFor(x)
			body := project(r.data, responseSchema(x.c.Op, 0), spec.ModeResponse)
			if x.part {
				body = x.body
			}
			x.resp = &response{Status: successStatus(x.c.Op), Body: body}
		case kindAction:
			r := w.rd.recs[w.rd.n.of(requestSchema(x.c.Op))]
			if r == nil {
				continue
			}
			x.body = project(r.data, requestSchema(x.c.Op), spec.ModeRequest)
		default:
			continue
		}
		x.built = true
	}
}

// successStatus is the lowest 2xx status of an operation.
func successStatus(op *spec.Operation) int {
	if op.Op.Responses != nil {
		for _, code := range sortedKeys(op.Op.Responses.Map()) {
			var n int
			if _, err := fmt.Sscanf(code, "%d", &n); err == nil && n/100 == 2 {
				return n
			}
		}
	}
	return http.StatusOK
}

// project keeps the fields of src the schema has, at every level: the
// properties of nested objects (also those of allOf parts) and the
// elements of arrays. readOnly fields are left out of a request, writeOnly
// ones out of a response; the names are those of the schema.
func project(src any, ref *openapi3.SchemaRef, mode spec.Mode) any {
	if ref == nil || ref.Value == nil {
		return src
	}
	switch x := src.(type) {
	case map[string]any:
		props, _ := dict.Properties(ref.Value)
		if len(props) == 0 {
			return src
		}
		out := map[string]any{}
		for k, p := range props {
			if p.Value == nil || (mode == spec.ModeRequest && p.Value.ReadOnly) || (mode == spec.ModeResponse && p.Value.WriteOnly) {
				continue
			}
			if f := fieldName(x, k); f != "" {
				out[k] = project(x[f], p, mode)
			}
		}
		return out
	case []any:
		items := ref.Value.Items
		if items == nil {
			return src
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = project(e, items, mode)
		}
		return out
	}
	return src
}

// requestSchema is the JSON schema of the request body, or nil.
func requestSchema(op *spec.Operation) *openapi3.SchemaRef {
	rb := op.Op.RequestBody
	if rb == nil || rb.Value == nil {
		return nil
	}
	for _, mt := range sortedKeys(rb.Value.Content) {
		if c := rb.Value.Content[mt]; spec.IsJSON(mt) && c != nil && c.Schema != nil {
			return c.Schema
		}
	}
	return nil
}

// any reports whether the run sent any write.
func (w *writes) any() bool {
	for _, x := range w.ops {
		if (x.sent || x.failed) && x.kind != kindQuery {
			return true
		}
	}
	return false
}

// verify reads every GET again after the writes and reports what differs
// from before, apart from the new ids and fields that change anyway: data
// the writes lost or changed.
func (w *writes) verify() {
	for _, id := range sortedKeys(w.rd.gets) {
		f := w.rd.gets[id]
		u, _, ok := w.rd.url(f.op, w.rd.k)
		if !ok {
			continue
		}
		resp, err := w.rd.c.do(w.rd.ctx, http.MethodGet, u, nil, "after", "read "+id+" again: is everything as before the writes?")
		if err != nil || !resp.ok() {
			msg := fmt.Sprintf("#%d GET %s answered %d before the writes, #%d GET %s answers %s now", f.resp.Seq, f.url, f.resp.Status, resp.Seq, u, status(resp, err))
			w.res.problem(CodeChanged, id, "%s", msg)
			w.res.suggest(CodeChanged, id, msg, "the writes removed what this GET reads: a DELETE removed it and no POST created it again under the same key; "+
				"restore it in the instance and check the DELETEs and POSTs the log shows for it (\"select\".<DTO>.delete chooses the DELETE)", nil)
			continue
		}
		before, after := w.canon(w.newIDs(f.resp.Body, responseSchema(f.op, f.resp.Status))), w.canon(resp.Body)
		if l1, l2 := size(before), size(after); l1 != l2 {
			msg := fmt.Sprintf("#%d GET %s lists %d elements, #%d before the writes listed %d", resp.Seq, u, l2, f.resp.Seq, l1)
			w.res.problem(CodeChanged, id, "%s", msg)
			w.res.suggest(CodeChanged, id, msg, "the writes removed or added elements: a DELETE also removed records below the deleted one, which no POST creates again, "+
				"or a POST created one more; restore the data in the instance, then let \"select\" take a record without such records below it", nil)
		} else if d := diffFields(before, after, ""); len(d) > 0 {
			msg := fmt.Sprintf("#%d GET %s answers other values than #%d before the writes: %s", resp.Seq, u, f.resp.Seq, strings.Join(d, ", "))
			w.res.problem(CodeChanged, id, "%s", msg)
			hint := "the server sets these fields itself when a record is written (time, user, version): let apitest ignore them"
			if lost := w.unsettable(d); len(lost) > 0 {
				hint = fmt.Sprintf("%s are no fields of the body of the PUT or POST that wrote the record, so the record created again has the server's values: "+
					"if the server sets them (time, user, version) let apitest ignore them; else the run lost data: restore it in the instance", strings.Join(lost, ", "))
			}
			w.res.suggestIgnore(w.in.Config, CodeChanged, id, msg, hint, d)
		}
	}
}

// unsettable are the fields no body the run sent can set: fields of no
// request schema of a PUT, PATCH or POST it sent.
func (w *writes) unsettable(fields []string) []string {
	settable := map[string]bool{}
	var add func(ref *openapi3.SchemaRef, depth int)
	add = func(ref *openapi3.SchemaRef, depth int) {
		if ref == nil || ref.Value == nil || depth > 10 {
			return
		}
		if ref.Value.Items != nil {
			add(ref.Value.Items, depth+1)
		}
		props, _ := dict.Properties(ref.Value)
		for k, p := range props {
			settable[strings.ToLower(k)] = true
			add(p, depth+1)
		}
	}
	for _, x := range w.ops {
		if x.sent && x.kind != kindDelete {
			add(requestSchema(x.c.Op), 0)
		}
	}
	var out []string
	for _, f := range fields {
		if !settable[strings.ToLower(f)] && !contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// canon leaves out the fields that change anyway and sorts the lists, so a
// record created again at the end of a list compares equal.
func (w *writes) canon(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if !w.changing(k) {
				out[k] = w.canon(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = w.canon(e)
		}
		sort.SliceStable(out, func(i, j int) bool { return text(out[i]) < text(out[j]) })
		return out
	}
	return v
}

// changing reports a field the reads found changing or the config ignores.
func (w *writes) changing(field string) bool {
	if ignores(w.in.Config.Run.IgnoreFields, field) {
		return true
	}
	_, ok := w.rd.volatile[field]
	return ok
}

func status(r response, err error) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprint(r.Status)
}

// newIDs replaces the old ids of the records created again by their new
// ones. The schema tells which table an object belongs to: the "id" of
// another DTO that happens to have the same number stays.
func (w *writes) newIDs(v any, ref *openapi3.SchemaRef) any {
	tables := map[string]bool{}
	for t := range w.rd.all {
		tables[t] = true
	}
	return walk(v, ref, "", func(t string, id any) any {
		for _, r := range w.rd.all[t] {
			if r.newID != nil && same(id, r.id) {
				return r.newID
			}
		}
		return id
	}, nil, tables, w.rd.n)
}

func size(v any) int {
	switch x := v.(type) {
	case []any:
		return len(x)
	case map[string]any:
		for _, e := range x {
			if l, ok := e.([]any); ok {
				return len(l)
			}
		}
	}
	return -1
}
