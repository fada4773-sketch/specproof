package scenario

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// checks is the compiled "validation" of a "$snapshot" entry.
type checks struct {
	ref       *openapi3.SchemaRef // schema of one element of the source
	mandatory []string
	paths     [][]string
	equal     []equalCheck
	details   []detail
	seeds     []seedField // "seed": must have a value like mandatoryFields
}

// seedField is a field of "seed": its name as written and its path.
type seedField struct {
	name string
	segs []string
}

type equalCheck struct {
	name string
	segs []string
	want any
}

// detail is a request of "followingDetails" and the GET of the spec it
// fits.
type detail struct {
	url string
	op  *spec.Operation
	mo  *model.Op // nil if the operation belongs to no resource
}

func (c *checks) active() bool {
	return len(c.paths)+len(c.equal)+len(c.details)+len(c.seeds) > 0
}

// cheap reports whether item passes the checks that need no request.
func (c *checks) cheap(item any) bool {
	if !hasAll(item, c.ref, c.paths) {
		return false
	}
	for _, s := range c.seeds {
		if valueAt(item, c.ref, s.segs) == nil {
			return false
		}
	}
	for _, e := range c.equal {
		if !equals(item, c.ref, e.segs, e.want) {
			return false
		}
	}
	return true
}

// describe lists the checks for messages.
func (c *checks) describe() string {
	var parts []string
	if len(c.mandatory) > 0 {
		parts = append(parts, fmt.Sprintf("mandatoryFields %v", c.mandatory))
	}
	if len(c.equal) > 0 {
		var eq []string
		for _, e := range c.equal {
			eq = append(eq, fmt.Sprintf("%s=%s", e.name, text(e.want)))
		}
		parts = append(parts, "equalFields "+strings.Join(eq, ", "))
	}
	if len(c.seeds) > 0 {
		var names []string
		for _, s := range c.seeds {
			names = append(names, s.name)
		}
		parts = append(parts, fmt.Sprintf("seed %v", names))
	}
	if len(c.details) > 0 {
		var urls []string
		for _, d := range c.details {
			urls = append(urls, d.url)
		}
		parts = append(parts, "followingDetails "+strings.Join(urls, ", "))
	}
	return strings.Join(parts, "; ")
}

// compile checks the "validation" of r against the spec: every field path
// must exist in the response of the source, every request of
// "followingDetails" must fit a GET of the spec.
func (b *builder) compile(r *model.Resource, src source) (*checks, bool) {
	v := src.checks
	c := &checks{ref: itemRef(src.op)}
	where := "$snapshot." + r.Name
	for _, m := range v.MandatoryFields {
		if m = strings.TrimSpace(m); m == "" {
			continue
		}
		segs := strings.Split(m, ".")
		if !resolvable(c.ref, segs, 0) {
			b.res.problem(CodeSnapshotFail, where, "mandatoryFields: %q matches no field of the response of %s; check the names", m, src.op.Op.ID)
			continue
		}
		c.mandatory = append(c.mandatory, m)
		c.paths = append(c.paths, segs)
	}
	names := make([]string, 0, len(v.EqualFields))
	for name := range v.EqualFields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		segs := strings.Split(strings.TrimSpace(name), ".")
		if !resolvable(c.ref, segs, 0) {
			b.res.problem(CodeSnapshotFail, where, "equalFields: %q matches no field of the response of %s; check the names", name, src.op.Op.ID)
			continue
		}
		c.equal = append(c.equal, equalCheck{name: name, segs: segs, want: spec.Normalize(v.EqualFields[name])})
	}
	for _, name := range src.seed {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		segs := strings.Split(name, ".")
		if !resolvable(c.ref, segs, 0) {
			b.res.problem(CodeSnapshotFail, where, "seed: %q matches no field of the response of %s; check the names", name, src.op.Op.ID)
			continue
		}
		c.seeds = append(c.seeds, seedField{name: name, segs: segs})
	}
	for _, raw := range v.FollowingDetails {
		if raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "GET ")); raw == "" {
			continue
		}
		path, _, _ := strings.Cut(raw, "?")
		var op *spec.Operation
		for _, o := range b.in.Spec.Ops {
			if o.Method == "GET" && MatchPath(o.Path, path) {
				op = o
				break
			}
		}
		if op == nil {
			b.res.problem(CodeSnapshotFail, where, "followingDetails: %q is no GET of the spec", raw)
			continue
		}
		c.details = append(c.details, detail{url: raw, op: op, mo: b.in.Model.Op(op)})
	}
	return c, len(b.res.Problems) == 0
}

// choose searches the elements in order until count of them pass every
// check, the requests of "followingDetails" included. It returns them and,
// per chosen element, the answers of those requests by URL. keyed means the
// first element was selected by a key default and must pass.
func (b *builder) choose(ctx context.Context, r *model.Resource, c *checks, items []any, count int, keyed bool) ([]any, []map[string]any, string) {
	var chosen []any
	var answers []map[string]any
	failed := map[string]int{}
	for i, item := range items {
		if len(chosen) == count {
			break
		}
		got, why := b.details(ctx, r, c, item)
		if why == "" {
			chosen = append(chosen, item)
			answers = append(answers, got)
			continue
		}
		if len(b.res.Problems) > 0 {
			return nil, nil, ""
		}
		failed[why]++
		if i == 0 && keyed {
			b.res.problem(CodeSnapshotKey, r.Name, "the %s the defaults select does not pass the validation: %s", r.Name, why)
			return nil, nil, ""
		}
	}
	var reasons []string
	for why, n := range failed {
		reasons = append(reasons, fmt.Sprintf("%d: %s", n, why))
	}
	sort.Strings(reasons)
	return chosen, answers, strings.Join(reasons, "; ")
}

// details sends the requests of "followingDetails" for one element. why is
// empty if every one answered with a 2xx and a body that fits its schema.
func (b *builder) details(ctx context.Context, r *model.Resource, c *checks, item any) (map[string]any, string) {
	got := map[string]any{}
	for _, d := range c.details {
		target, missing := b.fillDetail(r, d, item)
		if missing != "" {
			b.res.problem(CodeSnapshotFail, "$snapshot."+r.Name, "followingDetails: %s in %q is no field of %s; use the name of a field of the element", missing, d.url, r.Name)
			return nil, "unknown placeholder"
		}
		body, err := b.in.Fetch(ctx, target)
		b.res.Stats.Fetched++
		if err != nil {
			return nil, fmt.Sprintf("GET %s failed (%v)", d.url, err)
		}
		if !nonEmpty(body) {
			return nil, fmt.Sprintf("GET %s answered without data", d.url)
		}
		if s := successSchema(d.op); s != nil && s.Value != nil {
			if errs := b.validator().Validate(s.Value, spec.Normalize(body), spec.ModeResponse); len(errs) > 0 {
				why := fmt.Sprintf("GET %s answered against its schema (%s: %s)", d.url, errs[0].Pointer, errs[0].Reason)
				if !b.in.IgnoreLinting {
					return nil, why
				}
				b.res.note(CodeLint, "$snapshot."+r.Name, "%s", why)
			}
		}
		got[d.url] = spec.Normalize(body)
	}
	return got, ""
}

// fillDetail puts the values of the element into the placeholders of a
// request: by the key the model maps the parameter to, else by a field of
// the same name (or <Resource><Name>).
func (b *builder) fillDetail(r *model.Resource, d detail, item any) (string, string) {
	target, missing, _ := b.fillValues(r, d, item)
	return target, missing
}

// fillValues is fillDetail that also returns the value of each placeholder.
func (b *builder) fillValues(r *model.Resource, d detail, item any) (string, string, map[string]any) {
	used := map[string]any{}
	obj, _ := item.(map[string]any)
	value := func(name string) any {
		if d.mo != nil {
			if mp := d.mo.Param(name); mp != nil && mp.Resource == r {
				if v := fieldOf(obj, mp.Field); v != nil {
					return v
				}
			}
		}
		if v := fieldOf(obj, name); v != nil {
			return v
		}
		return fieldOf(obj, strings.TrimPrefix(strings.ToLower(name), strings.ToLower(r.Name)))
	}
	path, query, _ := strings.Cut(d.url, "?")
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		name, ok := placeholder(seg)
		if !ok {
			continue
		}
		v := value(name)
		if v == nil {
			return "", "{" + name + "}", nil
		}
		used[name] = v
		if p := specParam(d.op, name); p != nil {
			if s, err := params.Path(p, v); err == nil {
				segs[i] = s
				continue
			}
		}
		segs[i] = url.PathEscape(params.Scalar(v))
	}
	out := strings.Join(segs, "/")
	if query == "" {
		return out, "", used
	}
	var pairs []string
	for _, pair := range strings.Split(query, "&") {
		k, val, _ := strings.Cut(pair, "=")
		if name, ok := placeholder(val); ok {
			v := value(name)
			if v == nil {
				return "", "{" + name + "}", nil
			}
			used[name] = v
			pair = k + "=" + url.QueryEscape(params.Scalar(v))
		}
		pairs = append(pairs, pair)
	}
	return out + "?" + strings.Join(pairs, "&"), "", used
}

// keepDetails takes the answers of "followingDetails" into the store. An
// answer of the same resource (BookDetails of Book) is laid over the chosen
// records; one of another resource gives its records (one per chosen
// element); one of no resource becomes the example of its operation, with
// the parameter values that were asked.
func (b *builder) keepDetails(r *model.Resource, c *checks, chosen []any, answers []map[string]any) {
	if len(answers) == 0 {
		return
	}
	for _, d := range c.details {
		if d.mo == nil {
			_, _, used := b.fillValues(r, d, chosen[0])
			b.store.examples[d.op.ID] = answers[0][d.url]
			b.store.params[d.op.ID] = used
			b.res.note(CodeSnapshot, d.op.ID, "example from GET %s (followingDetails of %s)", d.url, r.Name)
			continue
		}
		dr := d.mo.Resource
		if dr == r && d.mo.Role != model.RoleList {
			for i, it := range chosen {
				obj, _ := it.(map[string]any)
				merged := map[string]any{}
				for k, v := range obj {
					merged[k] = v
				}
				if a, ok := answers[i][d.url].(map[string]any); ok {
					for k, v := range a {
						if fieldOf(merged, k) == nil {
							merged[k] = v
						}
					}
				}
				chosen[i] = merged
			}
			continue
		}
		var recs []Record
		var origins []string
		from := func(i int) string {
			target, _ := b.fillDetail(r, d, chosen[i])
			return fmt.Sprintf("GET %s (%s, followingDetails of %s #%d)", target, d.op.ID, r.Name, i+1)
		}
		if d.mo.Role == model.RoleList {
			items, _ := listItems(answers[0][d.url], d.mo.Items)
			for _, it := range items {
				recs = append(recs, toRecord(dr, it))
				origins = append(origins, from(0))
			}
			b.store.lists[d.op.ID] = items
		} else {
			for i, a := range answers {
				recs = append(recs, toRecord(dr, a[d.url]))
				origins = append(origins, from(i))
			}
		}
		b.fit(dr, recs, true)
		if b.detailOf == nil {
			b.detailOf = map[string]*model.Op{}
		}
		b.detailOf[strings.ToLower(dr.Name)] = d.mo
		b.setOrigins(dr, origins)
		b.store.set(dr, recs)
		b.store.fetched[strings.ToLower(dr.Name)] = true
		b.res.note(CodeSnapshot, dr.Name, "%d records from GET %s (followingDetails of %s)", len(recs), d.url, r.Name)
	}
}
