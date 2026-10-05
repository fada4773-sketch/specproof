package scenario

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// source is where the records of a resource are fetched.
type source struct {
	op       *model.Op
	checks   defaults.Validation // "validation" of "$snapshot"
	url      string              // the request written in "$snapshot", "" to build it from op
	count    int
	explicit bool     // from "$snapshot"
	seed     []string // "seed" of "$snapshot"
}

// snapshot fetches the records from the running instance. Only GETs are
// sent: first the source of every resource whose parameters are known, in
// the order of "$snapshot" in the defaults, then the other resources (a
// list without parameters, then lists below records fetched so far); an
// entry whose placeholders are not known yet waits for the ones after it.
// Then the reads of every record, to complete and cross-check them, and the
// other lists, whose elements the list examples show.
func (b *builder) snapshot(ctx context.Context) {
	b.keyDefaults()
	sources := map[*model.Resource]source{}
	for _, r := range b.in.Model.Resources {
		if src, ok := b.source(r); ok {
			sources[r] = src
		}
	}
	pending := b.runOrder()
	for progress := true; progress && len(pending) > 0; {
		progress = false
		var rest []*model.Resource
		for _, r := range pending {
			if b.store.Fetched(r.Name) { // by the followingDetails of another resource
				progress = true
				continue
			}
			src, ok := sources[r]
			if entry, has := b.in.Defaults.SnapshotFor(r.Name); has && strings.TrimSpace(entry.From) == "" {
				b.generated(r)
				b.res.note(CodeGenerated, r.Name, "\"$snapshot\" has no \"from\": the records are generated, nothing is fetched")
				if len(entry.Seed) > 0 {
					b.res.note(CodeSeed, r.Name, "\"seed\" is not used: generated values do not exist in the instance")
				}
				progress = true
				continue
			}
			if parent := b.generatedParent(src); ok && !src.explicit && parent != "" {
				b.generated(r)
				b.res.note(CodeGenerated, r.Name, "its list runs below %s, whose records are generated: the records are generated too", parent)
				progress = true
				continue
			}
			if !ok {
				b.generated(r)
				b.res.note(CodeSnapshotEmpty, r.Name, "no GET lists %s with known parameters; its records are generated (set \"$snapshot\": {%q: {\"from\": \"/path?query\"}}, apitest-gen review proposes it)", r.Name, r.Name)
				progress = true
				continue
			}
			paths, missing := b.requests(r, src)
			if missing != "" {
				rest = append(rest, r)
				continue
			}
			progress = true
			b.fetchSource(ctx, r, src, paths)
		}
		pending = rest
	}
	for _, r := range pending {
		src := sources[r]
		_, missing := b.requests(r, src)
		if r.Parent != nil && b.store.Fetched(r.Parent.Name) && len(b.store.Records(r.Parent.Name)) == 0 {
			b.store.set(r, nil)
			b.store.fetched[strings.ToLower(r.Name)] = true
			b.res.note(CodeSnapshotEmpty, r.Name, "the test starts without %s records, so there are no %s below them either", r.Parent.Name, r.Name)
			continue
		}
		if src.url != "" {
			b.res.problem(CodeSnapshotFail, "$snapshot."+r.Name, "%s in %q has no value; replace it in \"from\" with a value that exists in the instance", missing, src.url)
			continue
		}
		b.res.problem(CodeSnapshotFail, r.Name, "%s needs %s, which no record provides", src.op.Op.ID, missing)
	}
	if len(b.res.Problems) > 0 {
		return
	}
	for _, r := range ordered(b.in.Model) {
		if b.store.Fetched(r.Name) {
			b.complete(ctx, r, sources[r])
		}
	}
}

// runOrder returns the resources with a "$snapshot" entry in the order of
// the defaults, then the others, parents before their children.
func (b *builder) runOrder() []*model.Resource {
	var out []*model.Resource
	for _, name := range b.in.Defaults.SnapshotOrder() {
		for _, r := range b.in.Model.Resources {
			if strings.EqualFold(r.Name, name) && !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	for _, r := range ordered(b.in.Model) {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// source picks the operation the records of r come from: "$snapshot", or
// the list of r with the fewest parameters, all of them keys of a parent.
func (b *builder) source(r *model.Resource) (source, bool) {
	if s, ok := b.in.Defaults.SnapshotFor(r.Name); ok {
		if strings.TrimSpace(s.From) == "" {
			return source{}, false // generated on purpose
		}
		if IsURL(s.From) {
			src, ok := b.urlSource(r, s.From, s.Records())
			src.checks, src.seed = s.Checks(), s.Seed
			return src, ok
		}
		o := b.in.Model.OpByID(s.From)
		switch {
		case o == nil:
			b.res.problem(CodeSnapshotFail, "$snapshot."+r.Name, "operation %q does not exist", s.From)
		case o.Resource != r || o.Op.Method != http.MethodGet:
			b.res.problem(CodeSnapshotFail, "$snapshot."+r.Name, "%s is not a GET of %s", s.From, r.Name)
		default:
			return source{op: o, count: s.Records(), explicit: true, checks: s.Checks(), seed: s.Seed}, true
		}
		return source{}, false
	}
	if best := AutoSource(r); best != nil {
		return source{op: best, count: 1}, true
	}
	return source{}, false
}

// AutoSource is the list the records of r are fetched from without
// "$snapshot": the one with the fewest path parameters, all of them keys of
// a parent. A list with a path parameter of unknown meaning ({tier}) needs
// a value only the user knows, so it is no automatic source.
func AutoSource(r *model.Resource) *model.Op {
	var best *model.Op
	for _, o := range r.OpsWith(model.RoleList) {
		ok := true
		for _, p := range o.Op.Params {
			mp := o.Param(p.Name)
			if p.In == openapi3.ParameterInPath && (mp == nil || mp.Resource == r) {
				ok = false
			}
		}
		if ok && (best == nil || len(o.Params) < len(best.Params)) {
			best = o
		}
	}
	return best
}

// urlSource finds the GET of r a request in "$snapshot" addresses.
func (b *builder) urlSource(r *model.Resource, raw string, count int) (source, bool) {
	path, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "GET ")), "?")
	for _, role := range []model.Role{model.RoleList, model.RoleRead} {
		for _, o := range r.OpsWith(role) {
			if MatchPath(o.Op.Path, path) {
				return source{op: o, url: raw, count: count, explicit: true}, true
			}
		}
	}
	b.res.problem(CodeSnapshotFail, "$snapshot."+r.Name, "%q is no GET of %s in the spec", raw, r.Name)
	return source{}, false
}

// target builds the request path of o. Parameters of other resources take
// the key of their first record, own parameters the key of rec. missing
// names a parameter without value.
func (b *builder) target(o *model.Op, rec Record) (string, string) {
	target, missing := Fill(o, func(p *openapi3.Parameter) any { return b.paramValue(o, p, rec, true) })
	if len(missing) > 0 {
		return "", "{" + missing[0] + "}"
	}
	return target, ""
}

// paramExample is the example of a parameter that is no record key.
func paramExample(doc *yamldoc.Doc, op *spec.Operation, p *openapi3.Parameter) any {
	if p.In == openapi3.ParameterInPath {
		if e := pathParam(doc, op, p.Name); e != nil {
			if ex := yamldoc.Get(e.target, "example"); ex != nil {
				v, _ := yamldoc.Decode(ex)
				return v
			}
		}
	}
	if p.Example != nil {
		return spec.Normalize(p.Example)
	}
	return nil
}

// fetchSource fetches the records of r: the elements of all requests (one
// per seed set) are searched together, each element once.
func (b *builder) fetchSource(ctx context.Context, r *model.Resource, src source, paths []string) {
	var items []any
	var froms []string // the request of each element
	for _, p := range paths {
		body, err := b.in.Fetch(ctx, p)
		b.res.Stats.Fetched++
		if err != nil {
			b.res.problem(CodeSnapshotFail, r.Name, "GET %s (%s): %v", p, src.op.Op.ID, err)
			return
		}
		got := []any{body}
		if src.op.Role == model.RoleList {
			list, ok := listItems(body, src.op.Items)
			if !ok {
				b.res.problem(CodeSnapshotFail, r.Name, "GET %s (%s) returned no list", p, src.op.Op.ID)
				return
			}
			got = list
		}
		for _, item := range got {
			if !slices.ContainsFunc(items, func(seen any) bool { return compare.Equal(seen, item) }) {
				items = append(items, item)
				froms = append(froms, fmt.Sprintf("GET %s (%s)", p, src.op.Op.ID))
			}
		}
	}
	path := strings.Join(paths, ", ")
	all := len(items)
	fetched := items
	c, ok := b.compile(r, src)
	if !ok {
		return
	}
	var passed []any
	for _, item := range items {
		if c.cheap(item) {
			passed = append(passed, item)
		}
	}
	// only elements the validation keeps must fit the schema: the ones it
	// rejects ({} in a list) never become examples
	if !b.checkItems(src.op, passed, path) {
		return
	}
	items = b.selectKeyed(ctx, r, passed)
	if len(b.res.Problems) > 0 {
		return
	}
	keyed := len(b.keys[strings.ToLower(r.Name)]) > 0
	if keyed && len(items) > 0 && !c.cheap(items[0]) {
		b.res.problem(CodeSnapshotKey, r.Name, "the %s the defaults select does not pass %s", r.Name, c.describe())
		return
	}
	chosen, answers, reasons := b.choose(ctx, r, c, items, src.count, keyed)
	if len(b.res.Problems) > 0 {
		return
	}
	if len(chosen) < src.count {
		if c.active() || src.explicit || len(chosen) > 0 {
			b.res.problem(CodeSnapshotShort, r.Name, "%s", short(src, paths, c, all, len(passed), len(chosen), reasons))
			return
		}
		// the database starts without this resource: only what the test
		// creates exists
		b.store.set(r, nil)
		b.store.fetched[strings.ToLower(r.Name)] = true
		b.res.note(CodeSnapshotEmpty, r.Name, "GET %s (%s) returned no elements: the test starts without %s records, the examples show only the ones it creates", path, src.op.Op.ID, r.Name)
		return
	}
	b.keepSeeds(r, c, chosen[:src.count])
	origins := make([]string, src.count)
	for i, item := range chosen[:src.count] {
		// an element the defaults select may come from a read with its key
		origins[i] = fmt.Sprintf("GET %s (%s)", path, src.op.Op.ID)
		if j := slices.IndexFunc(fetched, func(f any) bool { return compare.Equal(f, item) }); j >= 0 {
			origins[i] = froms[j]
		}
	}
	b.setOrigins(r, origins)
	b.keepDetails(r, c, chosen, answers)
	items = chosen
	var recs []Record
	for _, item := range items[:src.count] {
		recs = append(recs, toRecord(r, item))
	}
	b.fit(r, recs, true)
	b.store.set(r, recs)
	b.store.fetched[strings.ToLower(r.Name)] = true
	b.store.lists[src.op.Op.ID] = items
	if c.active() {
		b.res.note(CodeSnapshot, r.Name, "%d of %d elements from GET %s (%s) that pass %s", src.count, all, path, src.op.Op.ID, c.describe())
		return
	}
	b.res.note(CodeSnapshot, r.Name, "%d of %d elements from GET %s (%s)", src.count, all, path, src.op.Op.ID)
}

// selectKeyed moves the element whose keys the defaults set to the front.
func (b *builder) selectKeyed(ctx context.Context, r *model.Resource, items []any) []any {
	keys := b.keys[strings.ToLower(r.Name)]
	if len(keys) == 0 {
		return items
	}
	match := func(item any) bool {
		obj, ok := item.(map[string]any)
		if !ok {
			return false
		}
		for f, k := range keys {
			if !compare.Equal(spec.Normalize(fieldOf(obj, f)), k.value) {
				return false
			}
		}
		return true
	}
	for i, item := range items {
		if match(item) {
			out := append([]any{item}, items[:i]...)
			return append(out, items[i+1:]...)
		}
	}
	// not in the list: read it with its key
	rec := b.keyRecord(r)
	for _, o := range r.OpsWith(model.RoleRead) {
		path, missing := b.target(o, rec)
		if missing != "" {
			continue
		}
		body, err := b.in.Fetch(ctx, path)
		b.res.Stats.Fetched++
		if err == nil && b.checkItems(o, []any{body}, path) && match(body) {
			return append([]any{body}, items...)
		}
	}
	var want []string
	for f, k := range keys {
		want = append(want, fmt.Sprintf("%s=%s (%q)", f, text(k.value), k.key))
	}
	b.res.problem(CodeSnapshotKey, r.Name, "the defaults ask for the %s with %s, but the instance has none; correct the defaults or add the record", r.Name, strings.Join(want, ", "))
	return nil
}

// complete reads every record with each read operation and lays the
// response over the record; a field that differs is a problem. The other
// lists of the resource are fetched for their examples.
func (b *builder) complete(ctx context.Context, r *model.Resource, src source) {
	recs := b.store.Records(r.Name)
	ignore := b.in.Defaults.RunConfig().IgnoreFields
	// where the records come from: the source, or for records from the
	// followingDetails of another resource that operation (src is empty
	// then); it is not read again
	origin := src.op
	if origin == nil {
		origin = b.detailOf[strings.ToLower(r.Name)]
	}
	for i, rec := range recs {
		// the request each field of the record comes from
		fieldFrom := map[string]string{}
		for f := range rec {
			fieldFrom[f] = b.origin(r, i)
		}
		for _, o := range r.OpsWith(model.RoleRead) {
			if o == origin || (i > 0 && !ownKey(o)) {
				continue // a read without own key only addresses the first record
			}
			path, missing := b.target(o, rec)
			if missing != "" {
				continue
			}
			body, err := b.in.Fetch(ctx, path)
			b.res.Stats.Fetched++
			if err != nil {
				b.res.problem(CodeSnapshotFail, r.Name, "GET %s (%s) for %s #%d: %v", path, o.Op.ID, r.Name, i+1, err)
				continue
			}
			if !b.checkItems(o, []any{body}, path) {
				continue
			}
			got := toRecord(r, body)
			read := fmt.Sprintf("GET %s (%s)", path, o.Op.ID)
			var diffs []fieldDiff
			for _, f := range sortedKeys(got) {
				old, has := rec[f]
				switch {
				case !has:
					rec[f] = got[f]
					fieldFrom[f] = read
				case !compare.Equal(old, got[f]) && !containsFold(ignore, f):
					diffs = append(diffs, fieldDiff{f, fieldFrom[f], old, got[f]})
				}
			}
			if len(diffs) > 0 {
				b.res.problem(CodeSnapshotDiff, fmt.Sprintf("%s #%d", r.Name, i+1), "%s", mismatch(r, rec, read, diffs))
			}
		}
	}
	if len(recs) > 0 {
		b.fieldDefaults(r, recs[0], true)
	}
	for _, o := range r.OpsWith(model.RoleList) {
		if o == origin {
			continue
		}
		path, missing := b.target(o, nil)
		if missing != "" {
			continue
		}
		body, err := b.in.Fetch(ctx, path)
		b.res.Stats.Fetched++
		if err != nil {
			b.res.problem(CodeSnapshotFail, r.Name, "GET %s (%s): %v", path, o.Op.ID, err)
			continue
		}
		items, ok := listItems(body, o.Items)
		if !ok {
			continue
		}
		// only the elements the example shows must fit the schema
		if kept := items[:min(len(items), len(recs))]; b.checkItems(o, kept, path) {
			b.store.lists[o.Op.ID] = kept
		}
	}
}

// short describes a source with fewer elements than "count":
//
//	"$snapshot" asks for 5 records, 3 pass:
//	  request   GET /BookPreset/Tier/A1 (GetBooks)
//	  elements  100 returned, 7 pass the fields, 3 pass all of it
//	  checks    mandatoryFields [Book.Author]; followingDetails /book/{code}/price
//	  rejected  4: GET /book/{code}/price failed (status 404)
//	add data to the instance, lower "count" or check the validation
func short(src source, paths []string, c *checks, all, fields, chosen int, reasons string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\"$snapshot\" asks for %d record", src.count)
	if src.count > 1 {
		b.WriteString("s")
	}
	fmt.Fprintf(&b, ", %d pass:", chosen)
	for i, p := range paths {
		label := ""
		if i == 0 {
			label = "request"
			if len(paths) > 1 {
				label = "requests"
			}
		}
		fmt.Fprintf(&b, "\n  %-9s GET %s (%s)", label, p, src.op.Op.ID)
	}
	if !c.active() {
		fmt.Fprintf(&b, "\n  %-9s %d returned", "elements", all)
		b.WriteString("\nadd data to the instance or lower \"count\"")
		return b.String()
	}
	fmt.Fprintf(&b, "\n  %-9s %d returned, %d pass the fields, %d pass all of it", "elements", all, fields, chosen)
	fmt.Fprintf(&b, "\n  %-9s %s", "checks", c.describe())
	for i, why := range strings.Split(reasons, "; ") {
		if why == "" {
			continue
		}
		label := ""
		if i == 0 {
			label = "rejected"
		}
		fmt.Fprintf(&b, "\n  %-9s %s", label, why)
	}
	b.WriteString("\nadd data to the instance, lower \"count\" or check the validation")
	return b.String()
}

// fieldDiff is a field two responses disagree about.
type fieldDiff struct {
	field, from string // from: the request of the value the record has
	had, got    any
}

// mismatch describes the fields a read returns differently, grouped by the
// request the record's value came from:
//
//	two requests for Book (Code="abc") disagree about 2 fields:
//	  A  GET /BookPreset/Tier/A1?bookCode=abc (GetBooks)
//	  B  GET /Book/abc (GetBook)
//	  Status  A "Docked"  B "Launched"
//	  …
func mismatch(r *model.Resource, rec Record, read string, diffs []fieldDiff) string {
	var keys []string
	for _, k := range r.Keys {
		keys = append(keys, fmt.Sprintf("%s=%s", k, text(rec[k])))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "two requests for the same %s (%s) disagree about %d field", r.Name, strings.Join(keys, " "), len(diffs))
	if len(diffs) > 1 {
		b.WriteString("s")
	}
	b.WriteString(", the examples cannot show both:")
	var froms []string
	width, valWidth := 0, 0
	for _, d := range diffs {
		if !slices.Contains(froms, d.from) {
			froms = append(froms, d.from)
		}
		width = max(width, len(d.field))
		valWidth = max(valWidth, len(text(d.had)))
	}
	label := func(from string) string {
		if len(froms) == 1 {
			return "A"
		}
		return fmt.Sprintf("A%d", slices.Index(froms, from)+1)
	}
	for _, f := range froms {
		fmt.Fprintf(&b, "\n  %-2s %s", label(f), f)
	}
	fmt.Fprintf(&b, "\n  %-2s %s", "B", read)
	var names []string
	for _, d := range diffs {
		fmt.Fprintf(&b, "\n  %-*s  %s %-*s  B %s", width, d.field, label(d.from), valWidth, text(d.had), text(d.got))
		names = append(names, fmt.Sprintf("%q", d.field))
	}
	fmt.Fprintf(&b, "\nif the endpoints show these fields differently on purpose, add them to \"$apitest\": {\"IgnoreFields\": [%s]} and to Config.IgnoreFields of the test; otherwise check that both requests address the same %s",
		strings.Join(names, ", "), r.Name)
	return b.String()
}

// checkItems validates fetched elements against the schema of o's response.
func (b *builder) checkItems(o *model.Op, items []any, path string) bool {
	s := itemSchema(o)
	if s == nil {
		return true
	}
	for i, item := range items {
		if errs := b.validator().Validate(s, spec.Normalize(item), spec.ModeResponse); len(errs) > 0 {
			if !b.res.lint(b.in.IgnoreLinting, CodeSnapshotFail, o.Resource.Name, "GET %s (%s): element %d violates the schema (%s: %s); the spec or the instance is wrong",
				path, o.Op.ID, i, errs[0].Pointer, errs[0].Reason) {
				return false
			}
		}
	}
	return true
}

// itemSchema is the schema of one record in o's response.
func itemSchema(o *model.Op) *openapi3.Schema {
	ref := successSchema(o.Op)
	if ref == nil || ref.Value == nil {
		return nil
	}
	if o.Role != model.RoleList {
		return ref.Value
	}
	if o.Items != "" {
		props := ref.Value.Properties
		p := props[strings.TrimPrefix(o.Items, "/")]
		if p == nil || p.Value == nil || p.Value.Items == nil {
			return nil
		}
		return p.Value.Items.Value
	}
	if ref.Value.Items == nil {
		return nil
	}
	return ref.Value.Items.Value
}

// successSchema is the schema of the lowest 2xx JSON response.
func successSchema(op *spec.Operation) *openapi3.SchemaRef {
	if op.Op.Responses == nil {
		return nil
	}
	for _, code := range sortedKeys(op.Op.Responses.Map()) {
		r := op.Op.Responses.Map()[code]
		if len(code) != 3 || code[0] != '2' || r == nil || r.Value == nil {
			continue
		}
		if _, m := jsonMedia(r.Value.Content); m != nil {
			return m.Schema
		}
	}
	return nil
}

// toRecord converts a fetched element: field names as in the resource.
func toRecord(r *model.Resource, item any) Record {
	rec := Record{}
	obj, _ := item.(map[string]any)
	for k, v := range obj {
		f := r.Field(k)
		if f == "" {
			f = k
		}
		rec[f] = spec.Normalize(v)
	}
	return rec
}

// fieldOf returns the field of obj that matches name, ignoring case.
func fieldOf(obj map[string]any, name string) any {
	if v, ok := obj[name]; ok {
		return v
	}
	for k, v := range obj {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return nil
}

// keyRecord holds the key values the defaults set for r.
func (b *builder) keyRecord(r *model.Resource) Record {
	keys := b.keys[strings.ToLower(r.Name)]
	if len(keys) == 0 {
		return nil
	}
	rec := Record{}
	for f, k := range keys {
		rec[f] = k.value
	}
	return rec
}

// ownKey reports whether o addresses a record by a key of its own resource.
func ownKey(o *model.Op) bool {
	for _, p := range o.Params {
		if p.Resource == o.Resource {
			return true
		}
	}
	return false
}

// generatedParent names a resource the source takes a key from whose
// records are generated: such a key does not exist in the instance.
func (b *builder) generatedParent(src source) string {
	if src.op == nil {
		return ""
	}
	for _, mp := range src.op.Params {
		name := mp.Resource.Name
		if mp.Resource != src.op.Resource && b.store.has(name) && !b.store.Fetched(name) {
			return name
		}
	}
	return ""
}
