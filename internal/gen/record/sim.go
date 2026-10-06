package record

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
)

// sim follows apitest's run in the empty environment: it holds only the
// seed, every table counts its ids from 1, a POST takes the next one, a
// DELETE gives none back.
type sim struct {
	rd  *reader
	cfg *Config
	res *Result
	w   *writes
	// next is the last id each table assigned
	next map[string]int
	// ids map the local ids to those of the environment: table → id → id
	ids map[string]map[string]int
	// live reports whether a record exists at this point
	live map[*rec]bool
	// written are the paths a PUT or POST reached: path template and values
	written map[string]bool
	// created is the id each POST of the run creates
	created map[string]int
	made    map[*rec]bool // records a POST of the run created
	tables  map[string]bool
	noted   map[string]bool
	stepped map[string]bool // operations whose case ran already
}

// example is what one case sends and gets.
type example struct {
	src     string // where the data came from: "#12 GET /Dock/D2"
	params  map[string]any
	body    any
	hasBody bool
	status  int
	resp    any
	hasResp bool
}

func newSim(rd *reader, cfg *Config, res *Result, w *writes) *sim {
	s := &sim{rd: rd, cfg: cfg, res: res, w: w, next: map[string]int{}, ids: map[string]map[string]int{}, live: map[*rec]bool{},
		written: map[string]bool{}, created: map[string]int{}, made: map[*rec]bool{}, tables: map[string]bool{}, noted: map[string]bool{}, stepped: map[string]bool{}}
	for t := range rd.recs {
		s.tables[t] = true
	}
	for _, name := range cfg.Seed {
		s.tables[rd.n.table(name)] = true
	}
	for _, x := range w.ops {
		if x.kind == kindCreate {
			s.tables[x.table] = true
		}
	}
	return s
}

// seed puts the seed records in: id 1 each, with "count" ids 1 to n in
// the order of the list.
func (s *sim) seed() {
	for _, name := range s.cfg.Seed {
		t := s.rd.n.table(name)
		recs := s.rd.seedRecs(t)
		if len(recs) == 0 {
			s.next[t] = 1
			s.res.problem(CodeSeedMissing, name, "the seed lists %s, but no GET of the run read one; check \"params\" and \"select\"", name)
			continue
		}
		for i, r := range recs {
			s.live[r] = true
			s.mapID(t, r, i+1)
		}
		s.next[t] = len(recs)
	}
}

func (s *sim) mapID(t string, r *rec, id int) {
	if s.ids[t] == nil {
		s.ids[t] = map[string]int{}
	}
	for _, local := range r.ids() {
		s.ids[t][text(local)] = id
	}
}

// seedRecords are the seed records as the environment must hold them:
// complete, with the ids it assigns; a DTO with "count" has a list.
func (s *sim) seedRecords() map[string]any {
	out := map[string]any{}
	for _, name := range s.cfg.Seed {
		t := s.rd.n.table(name)
		recs := s.rd.seedRecs(t)
		if len(recs) == 0 {
			continue
		}
		var list []any
		for _, r := range recs {
			list = append(list, s.convAll(r.data, nil, r.table, "seed "+name)) // complete: every element of its lists
		}
		out[name] = list[0]
		if s.cfg.selection(t, s.rd.n).Count.many() {
			out[name] = list
		}
	}
	return out
}

// seedOrder is the order to create the seed records in: each after the
// seed records it refers to ("tables" refs, else fields like planetId),
// otherwise in the order of "seed".
func (s *sim) seedOrder() []string {
	deps := map[string][]string{}
	for _, name := range s.cfg.Seed {
		r := s.rd.recs[s.rd.n.table(name)]
		if r == nil {
			continue
		}
		tb := s.cfg.table(r.table, s.rd.n)
		for _, k := range sortedKeys(r.data) {
			target := refTable(k)
			if tb != nil {
				for f, ref := range tb.Refs {
					if norm(f) == norm(k) {
						target = s.rd.n.table(ref.To)
					}
				}
			}
			for _, other := range s.cfg.Seed {
				if other != name && s.rd.n.table(other) == target && !contains(deps[name], other) {
					deps[name] = append(deps[name], other)
				}
			}
		}
	}
	var out []string
	for len(out) < len(s.cfg.Seed) {
		next := ""
		for _, name := range s.cfg.Seed {
			if contains(out, name) {
				continue
			}
			ready := true
			for _, d := range deps[name] {
				ready = ready && contains(out, d)
			}
			if ready {
				next = name
				break
			}
		}
		if next == "" { // a cycle: the rest in the order of "seed"
			for _, name := range s.cfg.Seed {
				if !contains(out, name) {
					out = append(out, name)
				}
			}
			break
		}
		out = append(out, next)
	}
	return out
}

// step follows one case and returns its example; nil if there is none to
// write.
func (s *sim) step(c *cases.Case) *example {
	defer func() { s.stepped[c.Op.ID] = true }()
	if c.Op.Method == http.MethodGet {
		return s.read(c)
	}
	x := s.w.byOp[c.Op.ID]
	if x == nil {
		return nil
	}
	def := c.Example == cases.DefaultExample
	newID := 0
	switch x.kind {
	case kindCreate:
		id := s.next[x.table] + 1
		s.next[x.table] = id
		newID = id
		if def && s.created[c.Op.ID] == 0 {
			s.created[c.Op.ID] = id
			if r := x.rec; r != nil {
				if s.live[r] || s.made[r] {
					s.res.note(CodeDuplicate, c.Op.ID, "%s creates the %s of %s again; with a unique key the environment rejects it. Give it a list of its own at its path (GET %s)",
						c.Op.ID, x.table, r.from, c.Op.Path)
				} else {
					s.mapID(x.table, r, id)
				}
				s.live[r] = true
				s.made[r] = true
			}
		}
	case kindUpdate:
		if x.url != "" {
			s.written[s.key(c.Op.Path, x.vals)] = true
		}
	case kindDelete:
		if def && x.rec != nil {
			s.live[x.rec] = false
		}
	}
	if !def {
		return nil
	}
	if x.url == "" {
		if s.w.needs[c.Op.ID] {
			msg := fmt.Sprintf("a parameter of %s has no value; it gets no example", c.Op.Path)
			s.res.note(CodeNoData, c.Op.ID, "%s", msg)
			s.rd.suggestParams(CodeNoData, c.Op, msg)
		}
		return nil
	}
	e := &example{params: s.params(x.vals), src: x.source()}
	if x.body != nil {
		e.body, e.hasBody = s.convAll(x.body, requestSchema(c.Op), "", c.Op.ID), true
	}
	if x.resp != nil {
		e.status = x.resp.Status
		body := x.resp.Body
		var idField string
		if o, ok := body.(map[string]any); ok && x.kind == kindCreate && x.rec == nil {
			// built from another record of the table: the id is the one
			// the environment assigns to this POST, not that of the record
			if idField, _ = idOf(o); idField != "" {
				c := map[string]any{}
				for k, v := range o {
					if k != idField {
						c[k] = v
					}
				}
				body = c
			}
		}
		if body != nil {
			conv := s.convAll
			if x.kind == kindQuery {
				conv = s.conv // a POST that only reads answers what the environment holds
			}
			e.resp, e.hasResp = conv(body, responseSchema(c.Op, x.resp.Status), x.table, c.Op.ID), true
			if m, ok := e.resp.(map[string]any); ok && idField != "" {
				m[idField] = json.Number(strconv.Itoa(newID))
			}
		}
	}
	return e
}

// key names a path with its values, as the environment sees it.
func (s *sim) key(path string, vals map[string]pval) string {
	var b strings.Builder
	b.WriteString(path)
	for _, k := range sortedKeys(vals) {
		b.WriteString(" " + k + "=" + text(s.param(vals[k])))
	}
	return b.String()
}

// read is the example of a GET at this point of the run.
func (s *sim) read(c *cases.Case) *example {
	f := s.rd.gets[c.Op.ID]
	if f == nil {
		if c.Example == cases.DefaultExample && s.w.needs[c.Op.ID] {
			msg := fmt.Sprintf("not read (%s); it gets no example", s.rd.failed[c.Op.ID])
			s.res.note(CodeNoData, c.Op.ID, "%s", msg)
			s.rd.suggestRead(c.Op, msg)
		}
		return nil
	}
	if c.Example != cases.DefaultExample {
		return nil
	}
	ref := responseSchema(c.Op, f.resp.Status)
	s.check(c, f, ref)
	e := &example{params: s.params(f.vals), status: f.resp.Status, src: fmt.Sprintf("#%d GET %s (%s)", f.resp.Seq, f.url, c.Op.ID)}
	if f.resp.Body != nil {
		e.resp, e.hasResp = s.conv(f.resp.Body, ref, "", c.Op.ID), true
	}
	return e
}

// check reports a GET that reads what the environment does not hold at
// this point.
func (s *sim) check(c *cases.Case, f *fetched, ref *openapi3.SchemaRef) {
	if _, _, list := listShape(ref); list {
		return
	}
	o, ok := f.resp.Body.(map[string]any)
	if !ok {
		return
	}
	// a path that ends with a key reads a record of the seed or of a POST;
	// anything else is a part of the records in its path
	// (/Dock/{code}/Config), which a PUT of the run must write first
	if t := s.rd.n.of(ref); t != "" && s.rd.recs[t] != nil && strings.HasSuffix(c.Op.Path, "}") && (s.cfg.seeded(t, s.rd.n) || s.creates(t)) {
		r := s.rd.taken(t, o)
		switch {
		case r == nil:
			msg := fmt.Sprintf("GET %s reads a %s the environment never holds: it holds only the ones of the seed and of the POSTs of the run", f.url, t)
			s.res.note(CodeContainer, c.Op.ID, "%s", msg)
			_, id := idOf(o)
			s.suggestSeed(c.Op.ID, msg, t, id)
		case !s.live[r]:
			msg := fmt.Sprintf("GET %s runs while the environment does not hold this %s: it is %s", f.url, t, s.why(r))
			s.res.note(CodeContainer, c.Op.ID, "%s", msg)
			if s.made[r] {
				s.suggestDeleteLast(c.Op.ID, msg)
			} else {
				s.suggestSeed(c.Op.ID, msg, t, r.id)
			}
		}
		return
	}
	if s.written[s.key(c.Op.Path, f.vals)] {
		return
	}
	for _, op := range s.rd.s.Ops {
		if op.Path == c.Op.Path && op.Method != http.MethodGet && op.Method != http.MethodDelete {
			msg := fmt.Sprintf("GET %s runs before %s writes it, so the environment answers without these data; let %s run first (\"$apitest\".MethodOrder or x-apitest-order)",
				f.url, op.ID, op.ID)
			s.res.note(CodeContainer, c.Op.ID, "%s", msg)
			s.suggestOrder(c.Op.ID, msg, c.Op, op)
			return
		}
	}
}

// creates reports whether a POST of the run creates records of a table.
func (s *sim) creates(t string) bool {
	for _, x := range s.w.ops {
		if x.kind == kindCreate && x.table == t {
			return true
		}
	}
	return false
}

func (s *sim) why(r *rec) string {
	if s.made[r] {
		return "deleted by then"
	}
	return "not created by then"
}

// params are the values of the parameters as the environment needs them.
func (s *sim) params(vals map[string]pval) map[string]any {
	out := map[string]any{}
	for k, v := range vals {
		out[k] = s.param(v)
	}
	return out
}

func (s *sim) param(v pval) any {
	if v.format != "" {
		i := 0
		out, _ := fill(v.format, func(string) (string, bool) {
			p := v.parts[i]
			i++
			return text(s.param(p)), true
		})
		return out
	}
	if v.table != "" && strings.EqualFold(v.field, "id") {
		return s.id(v.table, v.v, "")
	}
	if t := refTable(v.field); t != "" && s.tables[t] {
		return s.id(t, v.v, "")
	}
	return v.v
}

// id is the id of the environment for a local one; one it does not hold
// stays and is reported.
func (s *sim) id(t string, v any, where string) any {
	if n, ok := s.ids[t][text(v)]; ok {
		return json.Number(strconv.Itoa(n))
	}
	if s.tables[t] && where != "" && !s.noted[t+" "+text(v)+" "+where] {
		s.noted[t+" "+text(v)+" "+where] = true
		msg := fmt.Sprintf("refers to %s %s, which the environment does not hold", t, text(v))
		s.res.note(CodeContainer, where, "%s", msg)
		s.suggestSeed(where, msg, t, v)
	}
	return v
}

// conv turns a local answer into the one of the environment: the ids it
// assigns, and in lists only the records it holds at this point.
func (s *sim) conv(v any, ref *openapi3.SchemaRef, hint, where string) any {
	return walk(v, ref, hint, func(t string, id any) any { return s.id(t, id, where) }, s.present, s.tables, s.rd.n)
}

// convAll turns the body or answer of a write into the one of the
// environment: the ids it assigns, every element of its lists kept. The
// body is what apitest sends; an element that refers to a record the
// environment does not hold is reported by its id, not left out.
func (s *sim) convAll(v any, ref *openapi3.SchemaRef, hint, where string) any {
	return walk(v, ref, hint, func(t string, id any) any { return s.id(t, id, where) }, nil, s.tables, s.rd.n)
}

// present reports whether an element of a list exists in the environment.
func (s *sim) present(t string, o map[string]any) bool {
	if len(s.rd.all[t]) == 0 {
		return true
	}
	r := s.rd.taken(t, o)
	return r != nil && s.live[r]
}

// walk copies a value; ids pass through id, elements of lists through keep
// (nil keeps all).
func walk(v any, ref *openapi3.SchemaRef, hint string, id func(t string, v any) any, keep func(t string, o map[string]any) bool, tables map[string]bool, n namer) any {
	var s *openapi3.Schema
	if ref != nil {
		s = ref.Value
	}
	switch x := v.(type) {
	case map[string]any:
		t := n.of(ref)
		if t == "" {
			t = hint
		}
		var props openapi3.Schemas
		if s != nil {
			props, _ = dict.Properties(s)
		}
		out := map[string]any{}
		for k, e := range x {
			num, isNum := e.(json.Number)
			switch rt := refTable(k); {
			case isNum && t != "" && strings.EqualFold(k, "id"):
				out[k] = id(t, num)
			case isNum && rt != "" && tables[rt]:
				out[k] = id(rt, num)
			default:
				out[k] = walk(e, propOf(props, k), childHint(k, tables), id, keep, tables, n)
			}
		}
		return out
	case []any:
		var items *openapi3.SchemaRef
		if s != nil {
			items = s.Items
		}
		t := n.of(items)
		if t == "" {
			t = hint
		}
		out := []any{}
		var kept []any
		for _, e := range x {
			if o, ok := e.(map[string]any); ok && keep != nil && t != "" && !keep(t, o) {
				continue
			}
			kept = append(kept, e)
			out = append(out, walk(e, items, t, id, keep, tables, n))
		}
		if keep != nil && byID(kept) {
			// the instance lists by id, so the environment lists in the
			// order it created the records
			sort.SliceStable(out, func(i, j int) bool { return idNum(out[i]) < idNum(out[j]) })
		}
		return out
	}
	return v
}

// propOf is the schema of a field, its name ignoring case.
func propOf(props openapi3.Schemas, k string) *openapi3.SchemaRef {
	if p, ok := props[k]; ok {
		return p
	}
	for name, p := range props {
		if strings.EqualFold(name, k) {
			return p
		}
	}
	return nil
}

// byID reports whether every element has a numeric id and they ascend.
func byID(elems []any) bool {
	prev := -1.0
	for _, e := range elems {
		n := idNum(e)
		if n < 0 || n < prev {
			return false
		}
		prev = n
	}
	return len(elems) > 1
}

// idNum is the id of an element, -1 without one.
func idNum(e any) float64 {
	o, ok := e.(map[string]any)
	if !ok {
		return -1
	}
	_, id := idOf(o)
	n, ok := id.(json.Number)
	if !ok {
		return -1
	}
	f, err := n.Float64()
	if err != nil {
		return -1
	}
	return f
}

// childHint is the table a field of an inline schema names: "dock" or
// "docks".
func childHint(field string, tables map[string]bool) string {
	for _, t := range []string{tableName(field), strings.TrimSuffix(strings.ToLower(field), "s")} {
		if tables[t] {
			return t
		}
	}
	return ""
}

// source tells where the example of a write comes from.
func (x *wop) source() string {
	switch {
	case x.built && x.rec != nil:
		return fmt.Sprintf("built from the %s of %s (not sent)", x.table, x.rec.from)
	case x.resp != nil:
		return fmt.Sprintf("#%d %s %s (%s)", x.resp.Seq, x.c.Op.Method, x.url, x.c.Op.ID)
	}
	return x.c.Op.ID + " (not sent)"
}
