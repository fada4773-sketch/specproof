package record

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/value"
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
	held     bool      // not sent on purpose: no copy of its record can be made
	bodySrc  string    // where the body comes from, for the log
	why      string    // why it has no record, for the notes
}

// writes runs the writing operations against the instance.
type writes struct {
	noted map[string]bool // operations whose undeclared fields were reported

	rd    *reader
	in    Input
	res   *Result
	needs map[string]bool
	ops   []*wop
	byOp  map[string]*wop
	retry map[*rec][]*wop // updates answered 404, sent again after the POST
	later []*wop          // writes without a value for a parameter yet
	// copies are the records the run created as copies ("tables")
	copies []*rec
	// left are the records the run created and could not delete: table →
	// ids; the check after the writes leaves them out
	left map[string]map[string]bool
	late bool // the late reads are done: nothing waits any more
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
		k.use(x.rec)
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
		k.use(r)
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
		// a POST of another table is not resolved here: its tag may not
		// have read its records yet
		if x.kind != kindCreate || x.c.Example != cases.DefaultExample || x.table != t {
			continue
		}
		w.resolve(x)
		switch {
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
		body, from, missing := w.queryBody(x.c.Op)
		if len(missing) > 0 {
			w.res.note(CodeNotExecuted, x.c.Op.ID, "POST %s is not sent: no value read for its required fields %s", x.c.Op.Path, strings.Join(missing, ", "))
			continue
		}
		x.body = body
		x.bodySrc = "fields of the selected records: " + strings.Join(from, "; ")
		if len(from) == 0 {
			x.bodySrc = "no field of a selected record fits it"
		}
		w.send(x, http.MethodPost, "read "+x.c.Op.ID+" (a POST that only reads)")
	}
}

// queryBody fills the fields of the body of a query with the values of the
// selected records (planetCode, dockCode); it returns where each value
// comes from and the required fields without a value.
func (w *writes) queryBody(op *spec.Operation) (map[string]any, []string, []string) {
	body := map[string]any{}
	ref := requestSchema(op)
	if ref == nil || ref.Value == nil {
		return body, nil, nil
	}
	var from []string
	props, req := dict.Properties(ref.Value)
	for _, k := range sortedKeys(props) {
		if v, ok := w.rd.k.field("", k); ok {
			body[k] = v.v
			from = append(from, k+" ← "+v.src)
			continue
		}
		// a list of simple values named in the plural: dockCodes takes
		// the dockCode of the selected record
		if p := props[k].Value; p != nil && value.Type(p) == "array" && p.Items != nil && p.Items.Value != nil && isPrimitive(p.Items.Value) && strings.HasSuffix(k, "s") {
			if v, ok := w.rd.k.field("", strings.TrimSuffix(k, "s")); ok {
				body[k] = []any{v.v}
				from = append(from, k+" ← "+v.src)
			}
		}
	}
	if b, ok := w.withBody(op, body, nil).(map[string]any); ok {
		body = b
	}
	var missing []string
	for _, k := range req {
		if _, ok := body[k]; !ok {
			missing = append(missing, k)
		}
	}
	return body, from, missing
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
		if !w.needs[d.c.Op.ID] || d.sent || d.failed || d.held {
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
		switch {
		case cr == nil && w.copied(d.table):
			w.res.note(CodeBuilt, d.c.Op.ID, "DELETE %s is not sent: no POST of the spec creates a copy of the %s to delete; its example is built from the %s of %s",
				d.c.Op.Path, d.table, d.table, d.rec.from)
		case cr == nil:
			w.res.note(CodeBuilt, d.c.Op.ID, "DELETE %s is not sent: no POST of the spec creates the %s again, its data would be lost; its example is built from the %s of %s",
				d.c.Op.Path, d.table, d.table, d.rec.from)
		case w.copied(d.table):
			w.cycle(d, cr, d.rec)
		case w.cascades(d.table) != "":
			w.res.note(CodeBuilt, d.c.Op.ID, "DELETE %s is not sent: it would also delete the %s rows of the %s (\"tables\": ON DELETE CASCADE), which no POST creates again; "+
				"give the %s an entry in \"tables\" so the run deletes a copy; its example is built from the %s of %s",
				d.c.Op.Path, w.cascades(d.table), d.table, w.rd.dto(d.table), d.table, d.rec.from)
		default:
			w.recreate(d, d.url, d.vals, cr, d.rec)
		}
	}
	for _, cr := range kindOf(xs, kindCreate) {
		if !w.needs[cr.c.Op.ID] || cr.sent || cr.failed || cr.held {
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
		switch {
		case d == nil && w.copied(cr.table):
			w.res.note(CodeBuilt, cr.c.Op.ID, "POST %s is not sent: no DELETE of the spec removes the copy it would create; its body is the answer of %s",
				cr.c.Op.Path, cr.rec.from)
		case d == nil:
			w.res.note(CodeBuilt, cr.c.Op.ID, "POST %s is not sent: no DELETE of the spec removes a %s, so the POST would conflict with it; its body is the answer of %s",
				cr.c.Op.Path, cr.table, cr.rec.from)
		case w.copied(cr.table):
			w.cycle(d, cr, cr.rec)
		case w.cascades(cr.table) != "":
			w.res.note(CodeBuilt, cr.c.Op.ID, "POST %s is not sent: the DELETE before it would also delete the %s rows of the %s (\"tables\": ON DELETE CASCADE); "+
				"give the %s an entry in \"tables\" so the run creates and deletes a copy; its body is the answer of %s",
				cr.c.Op.Path, w.cascades(cr.table), cr.table, w.rd.dto(cr.table), cr.rec.from)
		default:
			w.recreate(d, u, vals, cr, cr.rec)
		}
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
	if u.body == nil && requestSchema(u.c.Op) != nil {
		w.res.note(CodeNotExecuted, u.c.Op.ID, "%s %s is not sent: the GET of its path answers a list, its body is one object, and no %s was selected",
			u.c.Op.Method, u.c.Op.Path, u.table)
		return
	}
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
	post.body = w.requestBody(cr.c.Op, r.data)
	post.bodySrc = r.origin()
	if resp, ok := w.send(post, http.MethodPost, fmt.Sprintf("create the %s again that #%d deleted (%s)", r.table, del.seq, cr.c.Op.ID)); !ok {
		w.res.problem(CodeWriteFailed, cr.c.Op.ID, "the %s deleted by #%d is missing in the instance now; create it again with the body of #%d (%s)",
			r.table, del.seq, resp.Seq, clip(text(post.body)))
		w.conflict(post, resp, r, true)
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
	resp, err := w.rd.c.do(w.rd.ctx, method, x.url, x.body, x.tag, why, w.origin(x)...)
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

// origin tells where the values of a write come from: its parameters, its
// body and the body "bodies" lays over it.
func (w *writes) origin(x *wop) []string {
	out := origin(x.vals)
	if x.body != nil && x.bodySrc != "" {
		out = append(out, "body ← "+x.bodySrc)
	}
	if w.in.Config != nil {
		if _, ok := w.in.Config.Bodies[x.c.Op.ID]; ok && x.body != nil {
			out = append(out, fmt.Sprintf("body: %q.%s laid over it", "bodies", x.c.Op.ID))
		}
	}
	return out
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
	var id any
	if o, ok := cr.resp.Body.(map[string]any); ok {
		_, id = idOf(o)
	}
	if id == nil && r.id != nil {
		id = w.findID(r, r.data, r.keys, cr)
	}
	if id == nil {
		return
	}
	old := r.newID
	if old == nil {
		old = r.id
	} else {
		r.oldIDs = append(r.oldIDs, old)
	}
	r.newID = id
	r.again = fmt.Sprintf("#%d POST %s", cr.seq, cr.url)
	w.moved(r, old)
}

// findID reads the list a record came from and returns the id of the
// element whose fields keys have the values of data, nil if there is none.
func (w *writes) findID(r *rec, data map[string]any, keys map[string]bool, cr *wop) any {
	from := r.from[strings.Index(r.from, " ")+1:]
	resp, err := w.rd.c.do(w.rd.ctx, http.MethodGet, from, nil, cr.tag, "find the new id of the "+r.table+" #"+fmt.Sprint(cr.seq)+" created")
	if err != nil || !resp.ok() {
		return nil
	}
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
	probe := &rec{data: data, keys: keys}
	var id any
	for _, e := range elems {
		if o, ok := e.(map[string]any); ok && probe.matchKeys(o) {
			_, id = idOf(o)
		}
	}
	return id
}

// moved carries the new id of a record created again into everything that
// addresses it: its data, the fields of the other records that refer to it
// (dockId), the values the paths take and the writes not sent yet, so a
// DELETE, PUT, GET or POST after the POST sends the id the instance holds
// now, not the one the GET read.
func (w *writes) moved(r *rec, old any) {
	rd := w.rd
	if f, _ := idOf(r.data); f != "" {
		r.data[f] = r.newID
		if r == rd.recs[r.table] {
			rd.k.replace(r.table, f, old, r.newID)
		}
	}
	if r == rd.recs[r.table] {
		rd.k.from[r.table] = r.origin()
	}
	for _, t := range sortedKeys(rd.all) {
		for _, o := range rd.all[t] {
			for _, f := range sortedKeys(o.data) {
				if refTable(f) != r.table || !same(o.data[f], old) {
					continue
				}
				o.data[f] = r.newID
				if o == rd.recs[t] {
					rd.k.replace(t, f, old, r.newID)
				}
			}
		}
	}
	// answers read before hold the old id
	rd.cache = map[string]response{}
	for _, x := range w.ops {
		if !x.sent && !x.failed && !x.held && !x.built {
			x.resolved, x.why = false, ""
		}
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

// sameGet is the GET of the path of an update, if the run read it for the
// same record: a GET of the same path with other values read another one.
func (w *writes) sameGet(u *wop) *fetched {
	for _, id := range sortedKeys(w.rd.gets) {
		f := w.rd.gets[id]
		if f.op.Path == u.c.Op.Path && (u.url == "" || f.url == u.url) {
			return f
		}
	}
	return nil
}

// bodyFor is the body of an update: what the GET of the same path answers,
// else the record, with the fields of the request schema. A GET that
// answers a list where the body is one object gives no body: the record
// is used.
func (w *writes) bodyFor(u *wop) any {
	src := u.rec.dataOrNil()
	if u.rec != nil {
		u.bodySrc = u.rec.origin()
	}
	if f := w.sameGet(u); f != nil {
		if _, isList := f.resp.Body.([]any); !isList || isArray(requestSchema(u.c.Op)) {
			// the GET wins, the record keeps what it lacks: a detail GET
			// may leave out fields its list has (DockCode next to a Dock object)
			src = fillIn(f.resp.Body, src)
			u.bodySrc = fmt.Sprintf("the answer of #%d GET %s (the same path)", f.resp.Seq, f.url)
			if u.rec != nil {
				u.bodySrc += ", the fields it lacks from " + u.rec.origin()
			}
		}
	}
	return w.requestBody(u.c.Op, src)
}

// fillIn lays base under top: fields top lacks or holds as null take the
// value of base, in nested objects too.
func fillIn(top, base any) any {
	t, ok1 := top.(map[string]any)
	b, ok2 := base.(map[string]any)
	if !ok1 || !ok2 {
		if top == nil {
			return base
		}
		return top
	}
	out := make(map[string]any, len(t)+len(b))
	for k, v := range b {
		out[k] = v
	}
	for k, v := range t {
		if bk := fieldName(b, k); bk != "" && bk != k {
			delete(out, bk) // the same field in another case
			out[k] = fillIn(v, b[bk])
			continue
		}
		out[k] = fillIn(v, b[k])
	}
	return out
}

// requestBody is the body of a write built from data the run read: the
// fields of the request schema, with the body "bodies" sets laid over it.
// Fields of the data the schema does not declare are not sent; they are
// reported once per operation, so a field the server needs but the spec
// lacks shows up (and "bodies" can send it: "{Field}").
func (w *writes) requestBody(op *spec.Operation, src any) any {
	ref := requestSchema(op)
	body := project(src, ref, spec.ModeRequest)
	if lost := undeclared(src, ref, "", 0); len(lost) > 0 && !w.noted[op.ID] {
		if w.noted == nil {
			w.noted = map[string]bool{}
		}
		w.noted[op.ID] = true
		if len(lost) > 12 {
			lost = append(lost[:12], fmt.Sprintf("… %d more", len(lost)-12))
		}
		w.res.note(CodeUndeclared, op.ID, "%s %s does not send these fields of the record, its request schema has none of them: %s; "+
			"if the server needs one, declare it in the schema, or send it with \"bodies\": {\"%s\": {\"<field>\": \"{<field>}\"}}",
			op.Method, op.Path, strings.Join(lost, ", "), op.ID)
	}
	return w.withBody(op, body, src)
}

// undeclared are the filled fields of data a schema has no property for,
// as paths; an id is left out, a request schema rarely has one.
func undeclared(v any, ref *openapi3.SchemaRef, path string, depth int) []string {
	if ref == nil || ref.Value == nil || depth > 10 {
		return nil
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := dict.Properties(ref.Value)
		if len(props) == 0 {
			return nil
		}
		var out []string
		for _, k := range sortedKeys(x) {
			p := propOf(props, k)
			name := k
			if path != "" {
				name = path + "." + k
			}
			switch {
			case p == nil && filled(x[k]) && !strings.EqualFold(k, "id"):
				out = append(out, name)
			case p != nil:
				out = append(out, undeclared(x[k], p, name, depth+1)...)
			}
		}
		return out
	case []any:
		if len(x) > 0 && ref.Value.Items != nil {
			return undeclared(x[0], ref.Value.Items, path+"[]", depth+1)
		}
	}
	return nil
}

// withBody lays the body "bodies" sets for an operation over the one the
// run built. A value "{Field}" takes that field of the data the body was
// built from (src), else of the selected records.
func (w *writes) withBody(op *spec.Operation, built, src any) any {
	if w.in.Config == nil {
		return built
	}
	set, ok := w.in.Config.Bodies[op.ID]
	if !ok {
		return built
	}
	return overlay(built, w.fillBody(spec.Normalize(set), src))
}

// fillBody replaces the values "{Field}" of a configured body.
func (w *writes) fillBody(v, src any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = w.fillBody(e, src)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = w.fillBody(e, src)
		}
		return out
	case string:
		if len(x) < 3 || x[0] != '{' || x[len(x)-1] != '}' || strings.ContainsAny(x[1:len(x)-1], "{}") {
			return x
		}
		name := strings.TrimSpace(x[1 : len(x)-1])
		if found := lookup(src, name); len(found) > 0 {
			return found[0]
		}
		if w.rd != nil && w.rd.k != nil {
			if p, ok := w.rd.k.field("", name); ok {
				return p.v
			}
		}
		w.res.note(CodeParam, "bodies", "%q: no field %s in the data of the record; the value is sent as it is", x, name)
	}
	return v
}

// overlay lays top over base: objects field by field, anything else is
// replaced.
func overlay(base, top any) any {
	t, ok1 := top.(map[string]any)
	b, ok2 := base.(map[string]any)
	if !ok1 || !ok2 {
		return top
	}
	out := make(map[string]any, len(b)+len(t))
	for k, v := range b {
		out[k] = v
	}
	for k, v := range t {
		out[k] = overlay(b[k], v)
	}
	return out
}

func isPrimitive(s *openapi3.Schema) bool {
	_, ok := dict.Primitive(s)
	return ok
}

func isArray(ref *openapi3.SchemaRef) bool {
	return ref != nil && ref.Value != nil && value.Type(ref.Value) == "array"
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
			x.body = w.requestBody(x.c.Op, r.data)
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
			x.body = w.requestBody(x.c.Op, r.data)
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
		ref := responseSchema(f.op, f.resp.Status)
		before, after := w.canon(w.newIDs(f.resp.Body, ref)), w.canon(w.dropLeft(resp.Body, ref))
		if _, l1, _, ok := listOf(ref, before); ok {
			if _, l2, _, ok := listOf(ref, after); ok && len(l1) != len(l2) {
				msg := fmt.Sprintf("#%d GET %s lists %d elements, #%d before the writes listed %d", resp.Seq, u, len(l2), f.resp.Seq, len(l1))
				hint := "the writes removed or added elements: a DELETE also removed records below the deleted one, which no POST creates again, " +
					"or a POST created one more; restore the data in the instance, then let \"select\" take a record without such records below it"
				if of := w.ofCopies(l1, l2); of != "" {
					msg += "; " + of
					hint = "the POST of a copy created these records below it (a required list of records in its body, or the server adds them), " +
						"and the DELETE of the copy left them (soft delete does not delete the rows below); delete them in the instance, " +
						"or let the service delete them with the copy"
				}
				w.res.problem(CodeChanged, id, "%s", msg)
				w.res.suggest(CodeChanged, id, msg, hint, nil)
				continue
			}
		}
		d := changes(before, after, "", "")
		if len(d) == 0 {
			continue
		}
		var text, fields, lists []string
		for i, c := range d {
			if i < 8 {
				text = append(text, c.String())
			}
			if c.list {
				lists = append(lists, c.path)
			} else if !contains(fields, c.name) {
				fields = append(fields, c.name)
			}
		}
		if len(d) > 8 {
			text = append(text, fmt.Sprintf("… %d more", len(d)-8))
		}
		msg := fmt.Sprintf("#%d GET %s answers other values than #%d before the writes: %s", resp.Seq, u, f.resp.Seq, strings.Join(text, "; "))
		w.res.problem(CodeChanged, id, "%s", msg)
		if len(lists) > 0 {
			w.res.suggest(CodeChanged, id, msg, fmt.Sprintf("the writes changed the number of elements of %s: the body of the PUT or POST left them out or the server keeps them elsewhere "+
				"(e.g. it takes ids, not objects); restore the data in the instance and check the body the log shows (-show-bodies)", strings.Join(lists, ", ")), nil)
		}
		if len(fields) == 0 {
			continue
		}
		hint := "the server sets these fields itself when a record is written (time, user, version): let apitest ignore them; " +
			"if the values are data, the body of the PUT or POST did not carry them: check it with -show-bodies"
		if lost := w.unsettable(fields); len(lost) > 0 {
			hint = fmt.Sprintf("%s are no fields of the body of the PUT or POST that wrote the record, so the record created again has the server's values: "+
				"if the server sets them (time, user, version) let apitest ignore them; else the run lost data: restore it in the instance", strings.Join(lost, ", "))
		}
		w.res.suggestIgnore(w.in.Config, CodeChanged, id, msg, hint, fields)
	}
}

// ofCopies names the new elements of a list that refer to a copy the run
// created ("id 102 (dockId 101: the copy #9 created)"), "" if none does.
func (w *writes) ofCopies(before, after []any) string {
	seen := map[string]bool{}
	for _, e := range before {
		if o, ok := e.(map[string]any); ok {
			if _, id := idOf(o); id != nil {
				seen[text(id)] = true
			}
		}
	}
	var found []string
	for _, e := range after {
		o, ok := e.(map[string]any)
		if !ok {
			continue
		}
		_, id := idOf(o)
		if id == nil || seen[text(id)] {
			continue
		}
		for _, k := range sortedKeys(o) {
			for _, c := range w.copies {
				if c.id != nil && refTable(k) == c.table && same(o[k], c.id) {
					found = append(found, fmt.Sprintf("id %s (%s %s: the copy #%d created)", text(id), k, text(o[k]), c.seq))
				}
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	if len(found) > 5 {
		found = append(found[:5], fmt.Sprintf("… %d more", len(found)-5))
	}
	return "new elements of copies: " + strings.Join(found, ", ")
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

// canon leaves out the fields that change anyway and sorts the lists (by
// id, else by text), so a record created again at the end of a list
// compares equal and the elements of a list compare with the same record.
func (w *writes) canon(v any) any {
	var strip func(v any) any
	strip = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, e := range x {
				if !w.changing(k) {
					out[k] = strip(e)
				}
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = strip(e)
			}
			return out
		}
		return v
	}
	return sortLists(strip(v))
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
