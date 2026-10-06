package record

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/gen/scenario"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Codes of the notes and problems.
const (
	CodeFetch       = "FETCH_FAILED"       // a GET failed
	CodeParam       = "PARAM_UNKNOWN"      // a parameter no record and no config fills
	CodeSelectNone  = "SELECT_NONE"        // no element passes "select"
	CodeSeedMissing = "SEED_MISSING"       // a seed DTO was not read
	CodeNotExecuted = "NOT_EXECUTED"       // a write the run cannot send nor build
	CodeBuilt       = "BUILT"              // a write not sent, its example built from the data read
	CodeWriteFailed = "WRITE_FAILED"       // a write the instance rejected
	CodeChanged     = "DATA_CHANGED"       // the instance answers differently after the writes
	CodeContainer   = "NOT_IN_CONTAINER"   // apitest reads data the empty environment lacks
	CodeDuplicate   = "DUPLICATE_CREATE"   // a second POST of the same record
	CodeVolatile    = "VOLATILE"           // a field that changes between two reads
	CodeNoData      = "NO_DATA"            // an operation without an answer to show
	CodeInvalid     = "EXAMPLE_INVALID"    // an example that violates its schema
	CodeShared      = "SHARED"             // one example place needs two values
	CodeStatus      = "STATUS"             // the instance answers with another 2xx
	CodeRemapped    = "IDS_SHIFTED"        // ids of unchanged examples moved
	CodeLintIgnored = "LINT_IGNORED"       // a violation reported, not stopping
	CodeUndeclared  = "NOT_SENT"           // fields of a record the request schema does not declare
	CodeConflict    = "UNIQUE_CONFLICT"    // a POST that violates a unique index
	CodeCopyLeft    = "COPY_LEFT"          // a copy the run created and could not delete
	CodeSoftUnique  = "UNIQUE_SOFT_DELETE" // a unique index that counts soft-deleted rows
)

// Severity of a code: what a reader has to do about it.
type Severity int

// Severities, the most serious last.
const (
	Info    Severity = iota // the run did something other than usual; the examples are fine
	Warning                 // an operation lacks an example or apitest will see other data
	Problem                 // the instance or the defaults need a fix
)

// codeInfo explains a code in a few words: what happened and what to do.
type codeInfo struct {
	Severity     Severity
	Meaning, Fix string
}

var codeInfos = map[string]codeInfo{
	CodeFetch:       {Problem, "a GET answered with an error or not at all", "check the URL and the record in the log (the line with its #number)"},
	CodeParam:       {Warning, "a GET is not read: no selected record has a value for a parameter", `set it in "params"`},
	CodeSelectNone:  {Problem, `no record of the list passes "select"`, `loosen "select" or add such a record to the instance`},
	CodeSeedMissing: {Problem, "a DTO of the seed was not read", `check "params" and "select" of that DTO`},
	CodeNotExecuted: {Warning, "a PUT, PATCH, DELETE or POST was neither sent nor built from the data read; it gets no example", "see the message: mostly a parameter without value"},
	CodeBuilt:       {Info, "a write was not sent, to keep the data of the instance; its example is built from what the GETs read", "nothing; the example is fine"},
	CodeWriteFailed: {Problem, "the instance rejected a write", "see the answer in the log"},
	CodeChanged:     {Problem, "after the writes a GET answers differently than before: the run changed data of the instance", `see "$suggestions": fields the server sets go into IgnoreFields; else restore the data`},
	CodeContainer:   {Warning, "apitest will read data the empty test environment does not hold at that point", `see "$suggestions": seed, select, MethodOrder, Tags or DeleteLast`},
	CodeDuplicate:   {Warning, "two POSTs create the same record; with a unique key the second fails", "give the second POST a record of its own"},
	CodeVolatile:    {Warning, "a field changes between two reads of the same GET (time, counter)", `add it to "$apitest".IgnoreFields and to the IgnoreFields of the test`},
	CodeNoData:      {Warning, "an operation gets no example: its GET could not be read or a parameter has no value", `see "$suggestions": params or select`},
	CodeInvalid:     {Problem, "an example violates its schema", "fix the data or the spec; -ignorelinting writes it anyway"},
	CodeShared:      {Problem, "one parameter declared for several operations needs two values", "declare it in each operation"},
	CodeStatus:      {Info, "the instance answers with another 2xx than the spec's first one", "nothing, or document that status"},
	CodeRemapped:    {Info, "ids of unchanged examples moved because records are created in another order", "nothing"},
	CodeLintIgnored: {Info, "an example that violates its schema was written (-ignorelinting)", "fix the data or the spec"},
	CodeUndeclared:  {Info, "a write leaves out fields of its record that its request schema does not declare", `declare them in the spec if the server needs them, or send them with "bodies"`},
	CodeConflict:    {Problem, "a POST violates a unique index of the database", `list the unique indexes in "tables"; with soft delete make the index partial (WHERE deleted_at IS NULL)`},
	CodeCopyLeft:    {Problem, "the run created a copy of a record, or rows the copy created, and could not delete them again", "delete them by hand (the message names them, the log shows the answer of the copy)"},
	CodeSoftUnique:  {Warning, "a unique index counts the rows a soft delete keeps: creating a deleted record again fails", "make the index partial: WHERE deleted_at IS NULL"},
}

// Explain returns the severity of a code and what it means and what to do,
// in a few words; unknown codes are warnings without text.
func Explain(code string) (sev Severity, meaning, fix string) {
	i, ok := codeInfos[code]
	if !ok {
		return Warning, "", ""
	}
	return i.Severity, i.Meaning, i.Fix
}

// Note is one line of the report.
type Note struct{ Code, Where, Message string }

func (n Note) String() string { return fmt.Sprintf("%s %s: %s", n.Code, n.Where, n.Message) }

// Result is what a run did.
type Result struct {
	Notes    []Note
	Problems []Note
	Changed  bool
	// Recorded is what the next run needs: it goes into the defaults file.
	Recorded *Recorded
	// Suggestions are what to change in the defaults file for NO_DATA and
	// NOT_IN_CONTAINER: they go into "$suggestions".
	Suggestions []Suggestion
	Stats       Stats
	// Probes are the verdicts on the requests that checked a candidate for
	// "select", by request number: why it was rejected, or that it passes.
	Probes map[int]string
	// Coverage tells which places of the spec hold an example now and why
	// the others have none.
	Coverage Coverage
}

// verdict keeps what a request that checked a candidate of t decided.
func (r *Result) verdict(seq int, t, reason string) {
	if seq == 0 {
		return
	}
	if r.Probes == nil {
		r.Probes = map[int]string{}
	}
	if reason == "" {
		r.Probes[seq] = fmt.Sprintf("the %s passes \"select\"", t)
		return
	}
	r.Probes[seq] = fmt.Sprintf("the %s is rejected, the next one is checked: %s", t, reason)
}

// Stats count what a run did.
type Stats struct {
	Ops, Written, Unchanged, Examples, Remapped, Done int
	// Reused are the records taken from "$recorded" instead of reading them.
	Reused int
}

func (r *Result) note(code, where, format string, args ...any) {
	r.Notes = append(r.Notes, Note{code, where, fmt.Sprintf(format, args...)})
}

func (r *Result) problem(code, where, format string, args ...any) {
	r.Problems = append(r.Problems, Note{code, where, fmt.Sprintf(format, args...)})
}

// lint reports a violation: a problem, or with IgnoreLinting a note; it
// returns whether the example is still written.
func (r *Result) lint(ignore bool, code, where, format string, args ...any) bool {
	if ignore {
		r.note(CodeLintIgnored, where, code+": "+format, args...)
		return true
	}
	r.problem(code, where, format, args...)
	return false
}

// Input is what a run needs.
type Input struct {
	Spec   *spec.Spec
	Doc    *yamldoc.Doc
	Config *Config
	Client *Client
	// Overwrite writes every example, also of unchanged operations.
	Overwrite bool
	// Writes sends PUT, DELETE and POST to the instance; without it they
	// are built from the data the GETs read.
	Writes        bool
	IgnoreLinting bool
	// Prev is the output of the last run (-out, else the spec itself); nil
	// if there is none. The examples of unchanged operations come from it,
	// never from Doc: every example of Doc is removed first.
	Prev *yamldoc.Doc
	// Token changes the unique values of the copies the run creates
	// ("tables"); a new one per run, so a copy an earlier run left (soft
	// delete) never collides. Empty: "run".
	Token string
}

// Run follows the tags of the run in their order: in each tag it reads the
// GETs, then sends the PUTs and PATCHes, the DELETEs and the POSTs. Then it
// writes the examples of what apitest will see in the empty environment.
func Run(ctx context.Context, in Input) (*Result, error) {
	res := &Result{}
	order, _, err := scenario.Order(in.Spec, in.Config.Run)
	if err != nil {
		return nil, err
	}
	var run []*cases.Case
	var tags []string
	ops := map[string]bool{}
	for _, c := range order {
		if c.Kind == cases.Positive && c.Skip == "" {
			run = append(run, c)
			ops[c.Op.ID] = true
			if !contains(tags, c.Group) {
				tags = append(tags, c.Group)
			}
		}
	}
	res.Stats.Ops = len(ops)
	n := newNamer(in.Spec)
	if err := in.Config.check(in.Spec, n); err != nil {
		return nil, err
	}
	rd := &reader{n: n, ctx: ctx, cfg: in.Config, s: in.Spec, c: in.Client, res: res, ops: ops, cache: map[string]response{},
		recs: map[string]*rec{}, all: map[string][]*rec{}, creates: map[string]string{}, forPath: map[string]*rec{},
		k: newKnown(), gets: map[string]*fetched{}, failed: map[string]string{}, volatile: map[string][]string{}}
	for id := range ops {
		if op := in.Spec.Op(id); op.Method == http.MethodPost {
			if t := createTable(op, rd.n); t != "" {
				rd.creates[op.Path] = t
			}
		}
	}
	old := in.Config.Recorded
	ex := newEmitter(in.Doc, res, in.IgnoreLinting)
	var prev *emitter
	if in.Prev != nil {
		prev = newEmitter(in.Prev, res, in.IgnoreLinting)
	}
	needs := map[string]bool{}
	fps := map[string]string{}
	for id := range ops {
		fps[id] = fingerprint(in.Spec.Op(id))
		needs[id] = in.Overwrite || old == nil || old.Operations[id] != fps[id] || prev == nil || !prev.has(in.Spec.Op(id))
		if !needs[id] {
			res.Stats.Unchanged++
		}
	}
	rd.needs = needs
	if old != nil && !in.Overwrite {
		rd.reuse = true
		var stale map[string]bool
		res.Stats.Reused, stale = rd.load(old)
		// a record selected again may be another one: the examples that
		// show it are written again
		for id := range ops {
			if !needs[id] && rd.touches(in.Spec.Op(id), stale) {
				needs[id] = true
				res.Stats.Unchanged--
			}
		}
	}
	if in.Token == "" {
		in.Token = "run"
	}
	w := &writes{rd: rd, in: in, res: res, needs: needs}
	w.classify(run)
	if in.Writes {
		w.softUnique()
	}

	byTag := rd.byTag(tags)
	rd.seedPhase()
	if rd.down != nil {
		return nil, fmt.Errorf("the instance does not answer: %w", rd.down)
	}
	for _, tag := range tags {
		rd.tag = tag
		rd.readOps(byTag[tag])
		if rd.down != nil {
			return nil, fmt.Errorf("the instance does not answer: %w", rd.down)
		}
		if in.Writes {
			w.queries(tag)
			w.tagWrites(tag)
		}
	}
	// GETs that need a value of a record of a later tag
	rd.tag = "late"
	rd.readOps(rd.getOps())
	if in.Writes {
		w.lateWrites()
	}
	rd.unread(rd.getOps())
	for _, name := range sortedKeys(rd.volatile) {
		res.note(CodeVolatile, name, "changes between two reads (%s); apitest cannot compare it: add %q to \"$apitest\".IgnoreFields and to the IgnoreFields of the test",
			strings.Join(rd.volatile[name], ", "), name)
	}
	if len(rd.volatile) > 0 {
		names := sortedKeys(rd.volatile)
		res.suggestIgnore(in.Config, CodeVolatile, "$apitest", "fields that change between two reads: "+strings.Join(names, ", "),
			"the instance changes them by itself (time, counter); apitest cannot compare them: add them to \"$apitest\".IgnoreFields and to the IgnoreFields of the test", names)
	}
	w.offline()
	if in.Writes && w.any() {
		w.verify()
	}

	sm := newSim(rd, in.Config, res, w)
	sm.seed()
	if prev != nil {
		for _, op := range in.Spec.Ops {
			ex.remember(prev, op)
		}
	}
	ex.stripAll()
	// only the output of an earlier run passes its examples on; without
	// "$recorded" the last output may be -spec itself
	if old != nil {
		for _, op := range in.Spec.Ops {
			if !ops[op.ID] || !needs[op.ID] {
				ex.carry(op)
			}
		}
	}
	done := map[string]bool{}
	for _, c := range run {
		e := sm.step(c)
		if e == nil || c.Example != cases.DefaultExample || !needs[c.Op.ID] {
			continue
		}
		if ex.emit(c, e) && w.complete(c.Op) {
			done[c.Op.ID] = true
		}
	}
	if old != nil && !in.Overwrite {
		ex.remap(run, needs, sm, old)
	}
	res.Recorded = recorded(old, fps, needs, done, sm)
	res.Changed = ex.changed
	if in.Prev != nil {
		a, errA := in.Doc.Bytes()
		b, errB := in.Prev.Bytes()
		res.Changed = errA != nil || errB != nil || !bytes.Equal(a, b)
	}
	res.Stats.Written = len(ops) - res.Stats.Unchanged
	res.Stats.Done = len(done)
	res.Coverage = ex.coverage(in.Spec, res, ops, needs)
	res.Coverage.Generated = len(w.generated)
	return res, nil
}

// byTag assigns the GETs to the tags of the run; a list "select" names in
// "from" that no tag runs is read in the first tag that writes or reads its
// DTO.
func (rd *reader) byTag(tags []string) map[string][]*spec.Operation {
	out := map[string][]*spec.Operation{}
	touches := map[string][]string{} // tag → tables
	for _, op := range rd.s.Ops {
		if !rd.ops[op.ID] {
			continue
		}
		g := op.Group()
		if t := createTable(op, rd.n); t != "" {
			touches[g] = append(touches[g], t)
		}
		ref := responseSchema(op, 0)
		if items, _, ok := listShape(ref); ok {
			ref = items
		}
		if t := rd.n.of(ref); t != "" {
			touches[g] = append(touches[g], t)
		}
	}
	for _, op := range rd.getOps() {
		if rd.ops[op.ID] {
			out[op.Group()] = append(out[op.Group()], op)
			continue
		}
		items, _, _ := listShape(responseSchema(op, 0))
		tag := tags[0]
		for _, g := range tags {
			if contains(touches[g], rd.n.of(items)) {
				tag = g
				break
			}
		}
		out[tag] = append(out[tag], op)
	}
	return out
}

// complete reports whether an operation got all it needs: a GET was read,
// a write was sent or built from the data read. Only those go into
// "$recorded"; the others are tried again in the next run.
func (w *writes) complete(op *spec.Operation) bool {
	if op.Method == http.MethodGet {
		return w.rd.gets[op.ID] != nil
	}
	x := w.byOp[op.ID]
	if x == nil || x.failed {
		return false
	}
	return x.resp != nil || (x.kind == kindDelete && x.sent)
}

// recorded is what the next run needs: the fingerprints of the operations
// whose examples are complete (of earlier runs too), the ids the POSTs
// create and the seed.
func recorded(old *Recorded, fps map[string]string, needs, done map[string]bool, sm *sim) *Recorded {
	r := &Recorded{Operations: map[string]string{}, Created: sm.created, Seed: sm.seedRecords(), SeedOrder: sm.seedOrder(),
		Params: paramsHash(sm.cfg), Records: sm.rd.storedRecords()}
	if old != nil {
		for id, fp := range old.Operations {
			if _, inRun := fps[id]; !inRun || !needs[id] {
				r.Operations[id] = fp
			}
		}
	}
	for id := range done {
		r.Operations[id] = fps[id]
	}
	return r
}

// sortLists orders every list by id, or by its JSON text without ids, so
// a list the instance returns in another order compares equal.
func sortLists(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = sortLists(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		ids := true
		for i, e := range x {
			out[i] = sortLists(e)
			ids = ids && idNum(e) >= 0
		}
		sort.SliceStable(out, func(i, j int) bool {
			if ids {
				return idNum(out[i]) < idNum(out[j])
			}
			return text(out[i]) < text(out[j])
		})
		return out
	}
	return v
}

// diffFields returns the names of the fields whose values differ.
func diffFields(a, b any, name string) []string {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return []string{name}
		}
		var out []string
		for _, k := range sortedKeys(x) {
			out = append(out, diffFields(x[k], y[k], k)...)
		}
		for _, k := range sortedKeys(y) { // a field only the second answer has
			if _, ok := x[k]; !ok && y[k] != nil {
				out = append(out, k)
			}
		}
		return out
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return nil // the list changed, not a field
		}
		var out []string
		for i := range x {
			out = append(out, diffFields(x[i], y[i], name)...)
		}
		return out
	}
	if name != "" && !same(a, b) {
		return []string{name}
	}
	return nil
}

// change is a value that differs between two answers.
type change struct {
	path, name    string // "dock.crew[2].name", "name"
	before, after any
	list          bool // a list with another number of elements
}

func (c change) String() string {
	if c.list {
		return fmt.Sprintf("%s: %d → %d elements", c.path, len(c.before.([]any)), len(c.after.([]any)))
	}
	show := func(v any) string {
		if v == nil {
			return "missing"
		}
		s := text(v)
		if str, ok := v.(string); ok {
			s = fmt.Sprintf("%q", str)
		}
		if len(s) > 40 {
			s = s[:37] + "..."
		}
		return s
	}
	return fmt.Sprintf("%s: %s → %s", c.path, show(c.before), show(c.after))
}

// changes returns what differs between two values, with the path of each
// difference and the name of its field; a list with another number of
// elements is one change.
func changes(a, b any, path, name string) []change {
	join := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return []change{{path: path, name: name, before: a, after: b}}
		}
		var out []change
		for _, k := range sortedKeys(x) {
			out = append(out, changes(x[k], y[k], join(k), k)...)
		}
		for _, k := range sortedKeys(y) {
			if _, ok := x[k]; !ok && y[k] != nil {
				out = append(out, change{path: join(k), name: k, after: y[k]})
			}
		}
		return out
	case []any:
		y, ok := b.([]any)
		switch {
		case !ok:
			return []change{{path: path, name: name, before: a, after: b}}
		case len(x) != len(y):
			return []change{{path: path, name: name, before: a, after: b, list: true}}
		}
		var out []change
		for i := range x {
			out = append(out, changes(x[i], y[i], fmt.Sprintf("%s[%d]", path, i), name)...)
		}
		return out
	}
	if !same(a, b) && (a != nil || b != nil) {
		return []change{{path: path, name: name, before: a, after: b}}
	}
	return nil
}
