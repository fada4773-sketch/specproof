// Package contract generates the scenario of a contract test from a spec:
// per resource a life cycle with several records (create, read, list,
// update, read after update, actions), the error steps the spec documents
// (409, 400/422, 412, 404) and the authentication steps (401, 403), and at
// the end the deletes in reverse order. Each decision carries a reason:
// "sure: …" where the spec says so, "guess: …" where the generator chose;
// the review lists the guesses.
package contract

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/exec"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Input is what the generator needs.
type Input struct {
	Spec *spec.Spec
	// Records is how many records each resource gets; 0 means 2.
	Records int
	// Overrides are the corrections of the user.
	Overrides *Overrides
	// Learned are records of a running stage by resource name, for
	// realistic values; LearnedFrom names the stage in the notes.
	Learned     map[string][]map[string]any
	LearnedFrom string
	// Seed makes the generated values reproducible.
	Seed uint64
}

// Note is something the review lists.
type Note struct {
	Step string // step id, "" for a request without step
	What string // the decision, or the request
	Why  string
	Fix  string // how to change it in the overrides
}

// Output is the generated scenario and what the review shows.
type Output struct {
	Scenario *scenario.Scenario
	// Guesses are decisions the generator could not derive from the spec.
	Guesses []Note
	// NotGenerated are documented answers no step provokes (a 500).
	NotGenerated []Note
	// Uncovered are operations without a step.
	Uncovered []Note
}

// rec is a record the scenario creates.
type rec struct {
	name   string
	res    *model.Resource
	idx    int             // from 0
	create *spec.Operation // the create of its resource
	// vars are the names of the values the create saves, by the source of
	// a binding ("body /id", "header Location").
	vars map[string]string
	// keyVar holds the key of the record; keyPtr is the key in an answer.
	keyVar, keyPtr string
	etag           string
	createID       string
	updateID       string
	refs           map[*model.Resource]*rec
	body           any // the body of the create, with placeholders
}

type gen struct {
	in      Input
	s       *spec.Spec
	m       *model.Model
	binds   *bind.Set
	schemes openapi3.SecuritySchemes
	out     *Output
	steps   []*scenario.Step
	recs    map[*model.Resource][]*rec
	order   []*model.Resource
	used    map[*spec.Operation]bool
	ids     map[string]int
}

// Generate builds the scenario of a spec.
func Generate(in Input) (*Output, error) {
	if in.Records <= 0 {
		in.Records = 2
	}
	if in.Overrides == nil {
		in.Overrides = &Overrides{}
	}
	binds, err := bind.Resolve(in.Spec)
	if err != nil {
		return nil, err
	}
	g := &gen{
		in: in, s: in.Spec, m: model.Detect(in.Spec, nil), binds: binds,
		out:  &Output{},
		recs: map[*model.Resource][]*rec{}, used: map[*spec.Operation]bool{}, ids: map[string]int{},
	}
	if in.Spec.Doc.Components != nil {
		g.schemes = in.Spec.Doc.Components.SecuritySchemes
	}
	g.order = g.resourceOrder()
	sc := &scenario.Scenario{}
	for _, r := range g.order {
		sc.Order = append(sc.Order, r.Name)
		g.section(r)
		g.others()
	}
	g.cleanup()
	g.others()
	for _, op := range g.s.Ops {
		if !g.used[op] {
			g.out.Uncovered = append(g.out.Uncovered, Note{What: op.Method + " " + op.Path, Why: g.whyUncovered(op),
				Fix: "add a step with \"add:\" in contract-overrides.yaml"})
		}
	}
	for _, r := range g.order {
		for _, rc := range g.recs[r] {
			refs := map[string]string{}
			for p, pr := range rc.refs {
				refs[p.Name] = pr.name
			}
			sc.Records = append(sc.Records, scenario.Record{Name: rc.name, Resource: r.Name, Refs: refs})
		}
	}
	sc.Steps = g.steps
	if err := sc.Check(); err != nil {
		return nil, fmt.Errorf("generated scenario: %w", err)
	}
	if err := in.Overrides.apply(sc); err != nil {
		return nil, err
	}
	g.out.Scenario = sc
	return g.out, nil
}

// resourceOrder orders the resources that can be created: a resource
// comes after those its create takes path values from, its parent and
// those its body fields refer to.
func (g *gen) resourceOrder() []*model.Resource {
	var creatable []*model.Resource
	for _, r := range g.m.Resources {
		if len(r.OpsWith(model.RoleCreate)) > 0 {
			creatable = append(creatable, r)
		}
	}
	deps := map[*model.Resource][]*model.Resource{}
	for _, r := range creatable {
		add := func(p *model.Resource) {
			if p != nil && p != r && slices.Contains(creatable, p) && !slices.Contains(deps[r], p) {
				deps[r] = append(deps[r], p)
			}
		}
		create := r.OpsWith(model.RoleCreate)[0].Op
		for _, b := range g.binds.Of(create) {
			add(g.resourceOfCreate(b.Producer))
		}
		add(r.Parent)
		for _, f := range r.Relations {
			if g.in.Overrides.link(r.Name, f) == "none" {
				continue
			}
			p, _ := r.Ref(f)
			add(p)
		}
		for f, target := range g.in.Overrides.Links {
			if res, field, ok := strings.Cut(f, "."); ok && strings.EqualFold(res, r.Name) && target != "none" && field != "" {
				add(g.m.Resource(target))
			}
		}
	}
	var out []*model.Resource
	done := map[*model.Resource]bool{}
	for len(out) < len(creatable) {
		progress := false
		for _, r := range creatable {
			if done[r] {
				continue
			}
			ready := true
			for _, d := range deps[r] {
				if !done[d] {
					ready = false
				}
			}
			if ready {
				out = append(out, r)
				done[r] = true
				progress = true
			}
		}
		if !progress {
			// a cycle: take the rest in model order
			for _, r := range creatable {
				if !done[r] {
					out = append(out, r)
					done[r] = true
					g.out.Guesses = append(g.out.Guesses, Note{What: "order of " + r.Name, Why: "its references form a cycle; it runs in the order of the spec"})
				}
			}
		}
	}
	return out
}

func (g *gen) resourceOfCreate(op *spec.Operation) *model.Resource {
	if mo := g.m.Op(op); mo != nil && mo.Role == model.RoleCreate {
		return mo.Resource
	}
	return nil
}

// id returns a unique step id.
func (g *gen) id(base string) string {
	g.ids[base]++
	if n := g.ids[base]; n > 1 {
		return base + "." + strconv.Itoa(n)
	}
	return base
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// section generates the steps of one resource: create every record, then
// read, list, update, actions, errors and authentication on the first.
func (g *gen) section(r *model.Resource) {
	create := r.OpsWith(model.RoleCreate)[0].Op
	for i := range g.in.Records {
		rc := &rec{name: lowerFirst(r.Name) + strconv.Itoa(i+1), res: r, idx: i, create: create, vars: map[string]string{}, refs: map[*model.Resource]*rec{}}
		for _, p := range g.dependsOn(r) {
			if list := g.recs[p]; len(list) > 0 {
				rc.refs[p] = list[min(i, len(list)-1)]
			}
		}
		if !g.createStep(rc) {
			break
		}
		g.recs[r] = append(g.recs[r], rc)
	}
	list := g.recs[r]
	if len(list) == 0 {
		return
	}
	first := list[0]
	for _, mo := range r.OpsWith(model.RoleRead) {
		g.readStep(first, mo.Op, first.name+".read", first.createID)
	}
	for _, mo := range r.OpsWith(model.RoleList) {
		g.listStep(r, mo.Op)
	}
	for _, mo := range r.OpsWith(model.RoleUpdate) {
		g.updateStep(first, mo.Op)
	}
	g.errorSteps(first)
}

// dependsOn returns the resources a resource refers to: its parent, the
// producers of its create and the targets of its relations.
func (g *gen) dependsOn(r *model.Resource) []*model.Resource {
	var out []*model.Resource
	add := func(p *model.Resource) {
		if p != nil && p != r && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	add(r.Parent)
	for _, a := range r.Ancestors() {
		add(a)
	}
	create := r.OpsWith(model.RoleCreate)[0].Op
	for _, b := range g.binds.Of(create) {
		add(g.resourceOfCreate(b.Producer))
	}
	for _, f := range r.Relations {
		if g.in.Overrides.link(r.Name, f) != "none" {
			p, _ := r.Ref(f)
			add(p)
		}
	}
	for f, target := range g.in.Overrides.Links {
		if res, _, ok := strings.Cut(f, "."); ok && strings.EqualFold(res, r.Name) && target != "none" {
			add(g.m.Resource(target))
		}
	}
	return out
}

// lowestSuccess is the lowest documented 2xx status of an operation.
func lowestSuccess(op *spec.Operation) int {
	best := 0
	if op.Op.Responses == nil {
		return 0
	}
	for code := range op.Op.Responses.Map() {
		n, err := strconv.Atoi(code)
		if err == nil && n/100 == 2 && (best == 0 || n < best) {
			best = n
		}
	}
	if best == 0 {
		for code := range op.Op.Responses.Map() {
			if strings.EqualFold(code, "2XX") {
				return 200
			}
		}
	}
	return best
}

// documents reports whether an operation documents a status exactly.
func documents(op *spec.Operation, status int) bool {
	return op.Op.Responses != nil && op.Op.Responses.Value(strconv.Itoa(status)) != nil
}

// needsAuth reports whether an operation needs a token.
func (g *gen) needsAuth(op *spec.Operation) bool {
	return exec.ResolveAuth(op.Security, g.schemes).Needed()
}

// createStep adds the create of a record; false if it cannot be built.
func (g *gen) createStep(rc *rec) bool {
	op := rc.create
	expect := lowestSuccess(op)
	if expect == 0 {
		g.out.Uncovered = append(g.out.Uncovered, Note{What: op.Method + " " + op.Path, Why: "no 2xx response documented"})
		return false
	}
	st := &scenario.Step{ID: g.id(rc.name + ".create"), Call: op.Method + " " + op.Path, Expect: expect, Notes: map[string]string{},
		Why: fmt.Sprintf("sure: create of %s", rc.res.Name)}
	if !g.params(st, op, rc) {
		return false
	}
	if _, media := cases.RequestMedia(op); media != nil {
		body, unique := g.body(st, op, media.Schema, rc, nil)
		st.Body, st.HasBody, st.Unique = body, true, unique
		rc.body = body
	}
	g.saves(st, rc)
	rc.createID = st.ID
	g.used[op] = true
	g.steps = append(g.steps, st)
	return true
}

// saves adds the values a create gives: one per source apitest binds
// parameters to (the id in the answer, the Location header), the key of
// the record and its ETag.
func (g *gen) saves(st *scenario.Step, rc *rec) {
	op := rc.create
	st.Save = map[string]string{}
	bs := g.binds.By(op)
	// the parameter name of the read operation names the value best
	read := map[string]bool{}
	for _, mo := range rc.res.OpsWith(model.RoleRead) {
		for _, p := range mo.Op.Params {
			read[p.Name] = true
		}
	}
	slices.SortStableFunc(bs, func(a, b *bind.Binding) int {
		ra, rb := read[a.Param.Name], read[b.Param.Name]
		switch {
		case ra && !rb:
			return -1
		case rb && !ra:
			return 1
		}
		return strings.Compare(a.Param.Name, b.Param.Name)
	})
	for _, b := range bs {
		if b.Source.HasConst {
			continue
		}
		key := b.Source.String()
		if rc.vars[key] != "" {
			continue
		}
		name := rc.name + "." + b.Param.Name
		rc.vars[key] = name
		st.Save[name] = saveSource(b.Source)
	}
	resp := responseSchema(op, st.Expect)
	key := ""
	if len(rc.res.Keys) > 0 {
		key = rc.res.Keys[0]
	}
	// the key of the record: from a saved value whose source is the key
	for src, name := range rc.vars {
		if src == "body /"+key || (strings.HasPrefix(src, "header Location") && rc.keyVar == "") {
			rc.keyVar = name
		}
	}
	if rc.keyVar == "" && key != "" {
		if resp != nil && hasProperty(resp, key) {
			name := rc.name + "." + key
			st.Save[name] = "/" + key
			rc.keyVar = name
		} else if hasRequestProperty(op, key) {
			name := rc.name + "." + key
			st.Save[name] = "request /" + key
			rc.keyVar = name
			rc.vars["request body /"+key] = name
		}
	}
	if key != "" && g.readHas(rc.res, key) {
		rc.keyPtr = "/" + key
	}
	if g.usesIfMatch(rc.res) && documentsHeader(op, st.Expect, "ETag") {
		rc.etag = rc.name + ".etag"
		st.Save[rc.etag] = "header ETag"
	}
	if len(st.Save) == 0 {
		st.Save = nil
	}
}

func saveSource(src bind.Source) string {
	switch {
	case src.Header != "":
		return "header " + src.Header
	case src.FromRequest && src.Pointer == "":
		return "request /"
	case src.FromRequest:
		return "request " + src.Pointer
	case src.Pointer == "":
		return "/"
	}
	return src.Pointer
}

// readHas reports whether the answers of a resource's reads have a field.
func (g *gen) readHas(r *model.Resource, field string) bool {
	for _, mo := range r.OpsWith(model.RoleRead) {
		if s := responseSchema(mo.Op, lowestSuccess(mo.Op)); s != nil && hasProperty(s, field) {
			return true
		}
	}
	return false
}

// usesIfMatch reports whether an operation of the resource takes If-Match.
func (g *gen) usesIfMatch(r *model.Resource) bool {
	for _, mo := range r.Ops {
		if ifMatch(mo.Op) != nil {
			return true
		}
	}
	return false
}

func ifMatch(op *spec.Operation) *openapi3.Parameter {
	for _, p := range op.Params {
		if p.In == openapi3.ParameterInHeader && strings.EqualFold(p.Name, "If-Match") {
			return p
		}
	}
	return nil
}

func documentsHeader(op *spec.Operation, status int, name string) bool {
	_, r := cases.ResponseFor(op, status)
	if r == nil {
		return false
	}
	for h := range r.Headers {
		if strings.EqualFold(h, name) {
			return true
		}
	}
	return false
}

// params fills the parameters of a step: bound path parameters from the
// records, required other ones from overrides, examples or the schema.
// False if a required one has no value.
func (g *gen) params(st *scenario.Step, op *spec.Operation, rc *rec) bool {
	for _, p := range op.Params {
		if p.In == openapi3.ParameterInCookie {
			continue
		}
		if p.In == openapi3.ParameterInHeader && strings.EqualFold(p.Name, "If-Match") {
			if rc != nil && rc.etag != "" {
				setParam(st, p, "{{"+rc.etag+"}}")
				continue
			}
		}
		if v, ok := g.boundValue(op, p, rc); ok {
			setParam(st, p, v)
			continue
		}
		if v, ok := g.in.Overrides.value(idxOf(rc), op.ID+"."+p.Name, p.Name); ok {
			setParam(st, p, v)
			st.Notes["/"+p.In+"/"+p.Name] = "override"
			continue
		}
		if !p.Required && p.In != openapi3.ParameterInPath {
			continue
		}
		v, ok := paramExample(p)
		if !ok {
			g.out.Uncovered = append(g.out.Uncovered, Note{What: op.Method + " " + op.Path,
				Why: fmt.Sprintf("no value for the required %s parameter %q", p.In, p.Name),
				Fix: fmt.Sprintf("values: {%s.%s: …} in contract-overrides.yaml", op.ID, p.Name)})
			return false
		}
		setParam(st, p, v)
		g.out.Guesses = append(g.out.Guesses, Note{Step: st.ID, What: fmt.Sprintf("%s parameter %s = %s", p.In, p.Name, scenario.Text(v)),
			Why: "from the example or schema of the parameter", Fix: fmt.Sprintf("values: {%s.%s: …}", op.ID, p.Name)})
	}
	return true
}

func idxOf(rc *rec) int {
	if rc == nil {
		return 0
	}
	return rc.idx
}

func setParam(st *scenario.Step, p *openapi3.Parameter, v any) {
	var m *map[string]any
	switch p.In {
	case openapi3.ParameterInPath:
		m = &st.Path
	case openapi3.ParameterInQuery:
		m = &st.Query
	default:
		m = &st.Header
	}
	if *m == nil {
		*m = map[string]any{}
	}
	(*m)[p.Name] = v
}

// boundValue is the placeholder of a bound path parameter: the value the
// create of the record (or of the record it refers to) saved.
func (g *gen) boundValue(op *spec.Operation, p *openapi3.Parameter, rc *rec) (string, bool) {
	b := g.binds.For(op, p)
	if b == nil || b.Source.HasConst {
		return "", false
	}
	src := g.resourceOfCreate(b.Producer)
	if src == nil {
		return "", false
	}
	var target *rec
	switch {
	case rc != nil && rc.res == src:
		target = rc
	case rc != nil && rc.refs[src] != nil:
		target = rc.refs[src]
	case len(g.recs[src]) > 0:
		target = g.recs[src][0]
	}
	if target == nil {
		return "", false
	}
	name := target.vars[b.Source.String()]
	if name == "" {
		return "", false
	}
	return "{{" + name + "}}", true
}

// paramExample is a value for a parameter from the spec: example, schema
// example, default, first enum value, else generated.
func paramExample(p *openapi3.Parameter) (any, bool) {
	if p.Example != nil {
		return spec.Normalize(p.Example), true
	}
	for _, k := range sortedKeys(p.Examples) {
		if ex := p.Examples[k]; ex != nil && ex.Value != nil && ex.Value.Value != nil {
			return spec.Normalize(ex.Value.Value), true
		}
	}
	if p.Schema == nil || p.Schema.Value == nil {
		return nil, false
	}
	s := p.Schema.Value
	switch {
	case s.Example != nil:
		return spec.Normalize(s.Example), true
	case s.Default != nil:
		return spec.Normalize(s.Default), true
	case len(s.Enum) > 0:
		return spec.Normalize(s.Enum[0]), true
	}
	return generated(s, "param."+p.Name, p.Name, "", 0)
}

// readStep reads a record and checks that it holds what step equals sent.
func (g *gen) readStep(rc *rec, op *spec.Operation, id, equals string) string {
	st := &scenario.Step{ID: g.id(id), Call: op.Method + " " + op.Path, Expect: lowestSuccess(op), Notes: map[string]string{}}
	if st.Expect == 0 || !g.params(st, op, rc) {
		return ""
	}
	st.Why = fmt.Sprintf("sure: read %s", rc.name)
	if equals != "" && g.sendsBody(equals) && responseSchema(op, st.Expect) != nil {
		st.Equals = equals
		st.Why += ", it holds what " + equals + " sent"
	}
	g.used[op] = true
	g.steps = append(g.steps, st)
	return st.ID
}

func (g *gen) sendsBody(id string) bool {
	for _, st := range g.steps {
		if st.ID == id {
			return st.HasBody || st.BodyFrom != ""
		}
	}
	return false
}

// listStep lists the records of a resource and checks they are there.
func (g *gen) listStep(r *model.Resource, op *spec.Operation) {
	list := g.recs[r]
	st := &scenario.Step{ID: g.id(lowerFirst(r.Name) + ".list"), Call: op.Method + " " + op.Path, Expect: lowestSuccess(op), Notes: map[string]string{}}
	if st.Expect == 0 || !g.params(st, op, list[0]) {
		return
	}
	st.Why = "sure: list of " + r.Name
	if list[0].keyVar != "" && list[0].keyPtr != "" {
		var values []any
		for _, rc := range list {
			if rc.keyVar != "" {
				values = append(values, "{{"+rc.keyVar+"}}")
			}
		}
		st.Contains = &scenario.Contains{Key: list[0].keyPtr, Values: values}
		st.Why += fmt.Sprintf(", it holds the %d records created", len(values))
	}
	g.used[op] = true
	g.steps = append(g.steps, st)
}

// updateStep changes the first record and reads it again.
func (g *gen) updateStep(rc *rec, op *spec.Operation) {
	expect := lowestSuccess(op)
	if expect == 0 {
		return
	}
	verb := "update"
	if op.Method == http.MethodPatch {
		verb = "patch"
	}
	st := &scenario.Step{ID: g.id(rc.name + "." + verb), Call: op.Method + " " + op.Path, Expect: expect, Notes: map[string]string{}}
	if !g.params(st, op, rc) {
		return
	}
	st.Why = fmt.Sprintf("sure: %s of %s", verb, rc.name)
	if _, media := cases.RequestMedia(op); media != nil {
		body, unique := g.body(st, op, media.Schema, rc, rc.body)
		body, changed := change(body, media.Schema.Value, rc.res, op.Method == http.MethodPatch)
		if changed != "" {
			st.Notes["/body/"+changed] = "sure: changed by the update"
		}
		st.Body, st.HasBody, st.Unique = body, true, unique
	}
	if rc.etag != "" && documentsHeader(op, expect, "ETag") {
		st.Save = map[string]string{rc.etag: "header ETag"}
	}
	g.used[op] = true
	g.steps = append(g.steps, st)
	rc.updateID = st.ID
	for _, mo := range rc.res.OpsWith(model.RoleRead) {
		g.readStep(rc, mo.Op, rc.name+".read.after"+strings.ToUpper(verb[:1])+verb[1:], st.ID)
	}
}

// errorSteps adds the error and authentication steps the spec documents
// for the operations of the first record.
func (g *gen) errorSteps(rc *rec) {
	r := rc.res
	create := rc.create
	createStep := g.step(rc.createID)
	if documents(create, http.StatusConflict) && createStep != nil && createStep.HasBody {
		st := g.copyStep(createStep, rc.name+".create.duplicate", http.StatusConflict)
		st.Body, st.HasBody, st.BodyFrom, st.Save, st.Unique = nil, false, rc.createID, nil, nil
		st.Why = "sure: 409 documented at " + create.ID + "; the same body again"
		g.steps = append(g.steps, st)
	}
	if code := invalidStatus(create); code != 0 && createStep != nil && createStep.HasBody {
		if body, field := withoutRequired(createStep.Body, requestSchema(create)); field != "" {
			st := g.copyStep(createStep, lowerFirst(r.Name)+".create.invalid", code)
			st.Body, st.Save, st.Unique = body, nil, nil
			st.Why = fmt.Sprintf("sure: %d documented at %s; the required field %s is missing", code, create.ID, field)
			g.steps = append(g.steps, st)
		}
	}
	for _, mo := range r.OpsWith(model.RoleUpdate) {
		op := mo.Op
		if p := ifMatch(op); p != nil && documents(op, http.StatusPreconditionFailed) && rc.updateID != "" {
			up := g.step(rc.updateID)
			st := g.copyStep(up, rc.name+".update.stale", http.StatusPreconditionFailed)
			st.Header[p.Name] = `"0"`
			st.Body, st.HasBody, st.BodyFrom, st.Save, st.Unique = nil, false, rc.updateID, nil, nil
			st.Why = "sure: 412 documented; an If-Match that is out of date"
			g.steps = append(g.steps, st)
		}
	}
	for _, mo := range r.OpsWith(model.RoleRead) {
		op := mo.Op
		if !documents(op, http.StatusNotFound) {
			continue
		}
		st := &scenario.Step{ID: g.id(lowerFirst(r.Name) + ".read.unknown"), Call: op.Method + " " + op.Path, Expect: http.StatusNotFound, Notes: map[string]string{}}
		if !g.params(st, op, rc) {
			continue
		}
		key := lastPathParam(op)
		if key == nil {
			continue
		}
		v, why := cases.NotFoundValue(key)
		if why != "" {
			g.out.Guesses = append(g.out.Guesses, Note{Step: st.ID, What: "no unknown key", Why: why})
			continue
		}
		st.Path[key.Name] = v
		st.Why = "sure: 404 documented; a key no record has"
		g.steps = append(g.steps, st)
	}
	// authentication: every operation of the resource that documents 401
	// or 403, on the first record
	for _, mo := range r.Ops {
		op := mo.Op
		if !g.needsAuth(op) {
			continue
		}
		base := g.baseFor(rc, mo)
		if base == nil {
			continue
		}
		verb := string(mo.Role)
		if verb == "" {
			verb = lowerFirst(op.ID)
		}
		if documents(op, http.StatusUnauthorized) {
			st := g.copyStep(base, rc.name+"."+verb+".unauthorized", http.StatusUnauthorized)
			st.Token = scenario.TokenNone
			st.Why = "sure: 401 documented; no token"
			g.authBody(st, base)
			g.steps = append(g.steps, st)
			if mo.Role == model.RoleRead {
				st := g.copyStep(base, rc.name+"."+verb+".invalidToken", http.StatusUnauthorized)
				st.Token = scenario.TokenInvalid
				st.Why = "sure: 401 documented; a manipulated token"
				g.authBody(st, base)
				g.steps = append(g.steps, st)
			}
		}
		forbidden, _ := op.Op.Extensions["x-apitest-forbidden"].(bool)
		if documents(op, http.StatusForbidden) || forbidden {
			st := g.copyStep(base, rc.name+"."+verb+".forbidden", http.StatusForbidden)
			st.Token = scenario.TokenForbidden
			st.Why = "sure: 403 documented; a token with too few rights"
			g.authBody(st, base)
			g.steps = append(g.steps, st)
		}
	}
	for _, mo := range r.Ops {
		for _, code := range []int{500, 502, 503} {
			if documents(mo.Op, code) {
				g.out.NotGenerated = append(g.out.NotGenerated, Note{What: mo.Op.Method + " " + mo.Op.Path, Why: fmt.Sprintf("%d: a server error cannot be provoked from the spec", code),
					Fix: "add: in contract-overrides.yaml, with the request that triggers it"})
			}
		}
	}
}

// baseFor is the regular step of an operation of the record that an
// authentication step repeats without or with another token.
func (g *gen) baseFor(rc *rec, mo *model.Op) *scenario.Step {
	call := mo.Op.Method + " " + mo.Op.Path
	for _, st := range g.steps {
		if st.Call == call && st.Expect/100 == 2 && st.Token == "" && (strings.HasPrefix(st.ID, rc.name+".") || strings.HasPrefix(st.ID, lowerFirst(rc.res.Name)+".")) {
			return st
		}
	}
	if mo.Role == model.RoleDelete {
		st := &scenario.Step{ID: rc.name + ".delete", Call: call, Expect: lowestSuccess(mo.Op), Notes: map[string]string{}}
		if st.Expect != 0 && g.params(st, mo.Op, rc) {
			return st
		}
	}
	return nil
}

// authBody gives an authentication step the body of its regular step.
func (g *gen) authBody(st, base *scenario.Step) {
	st.Save, st.Unique, st.Equals, st.Contains = nil, nil, "", nil
	if base.HasBody && slices.ContainsFunc(g.steps, func(s *scenario.Step) bool { return s.ID == base.ID }) {
		st.Body, st.HasBody, st.BodyFrom = nil, false, base.ID
	}
}

func (g *gen) step(id string) *scenario.Step {
	for _, st := range g.steps {
		if st.ID == id {
			return st
		}
	}
	return nil
}

// copyStep copies a step under a new id and status.
func (g *gen) copyStep(st *scenario.Step, id string, expect int) *scenario.Step {
	c := *st
	c.ID, c.Expect, c.Notes = g.id(id), expect, map[string]string{}
	c.Path, _ = scenario.Clone(anyOrNil(st.Path)).(map[string]any)
	c.Query, _ = scenario.Clone(anyOrNil(st.Query)).(map[string]any)
	c.Header, _ = scenario.Clone(anyOrNil(st.Header)).(map[string]any)
	if c.Header == nil {
		c.Header = map[string]any{}
	}
	c.Body = scenario.Clone(st.Body)
	c.Equals, c.Contains = "", nil
	return &c
}

func anyOrNil(m map[string]any) any {
	if m == nil {
		return nil
	}
	return m
}

// invalidStatus is the status a body without a required field gets: 422
// if documented, else 400; 0 if neither is.
func invalidStatus(op *spec.Operation) int {
	for _, code := range []int{http.StatusUnprocessableEntity, http.StatusBadRequest} {
		if documents(op, code) {
			return code
		}
	}
	return 0
}

func lastPathParam(op *spec.Operation) *openapi3.Parameter {
	segs := strings.Split(strings.Trim(op.Path, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		name, ok := strings.CutPrefix(segs[i], "{")
		if !ok {
			continue
		}
		name = strings.TrimSuffix(name, "}")
		for _, p := range op.Params {
			if p.In == openapi3.ParameterInPath && p.Name == name {
				return p
			}
		}
	}
	return nil
}

// others adds the operations not used yet whose required parameters the
// records give values now: reads of sub-resources, lookups, actions.
func (g *gen) others() {
	for _, op := range g.s.Ops {
		if g.used[op] || op.Method == http.MethodDelete {
			continue
		}
		if mo := g.m.Op(op); mo != nil && mo.Role == model.RoleCreate {
			continue
		}
		expect := lowestSuccess(op)
		if expect == 0 {
			continue
		}
		rc := g.recordFor(op)
		name := lowerFirst(op.ID)
		if !op.HasOperationID {
			name = strings.ToLower(op.Method) + strings.ReplaceAll(strings.ReplaceAll(op.Path, "/", "."), "{", "")
			name = strings.ReplaceAll(name, "}", "")
		}
		id := name
		if rc != nil {
			id = rc.name + "." + name
		}
		st := &scenario.Step{ID: id, Call: op.Method + " " + op.Path, Expect: expect, Notes: map[string]string{}}
		if !g.fillable(op, rc) {
			continue
		}
		st.ID = g.id(id)
		if !g.params(st, op, rc) {
			continue
		}
		if op.Method == http.MethodGet {
			st.Why = "sure: a read of " + op.Path
		} else {
			st.Why = fmt.Sprintf("guess: an action (%s %s); its body and the order are chosen by the generator", op.Method, op.Path)
			g.out.Guesses = append(g.out.Guesses, Note{Step: st.ID, What: st.Call, Why: "an operation that is no create, read, update or delete; it runs once with a generated body", Fix: "steps: {" + st.ID + ": {…}} or skip"})
			if _, media := cases.RequestMedia(op); media != nil {
				body, unique := g.body(st, op, media.Schema, rc, nil)
				st.Body, st.HasBody, st.Unique = body, true, unique
			}
		}
		g.used[op] = true
		g.steps = append(g.steps, st)
	}
}

// recordFor is the record whose values an operation takes: the record of
// the producer of its last bound path parameter.
func (g *gen) recordFor(op *spec.Operation) *rec {
	var out *rec
	for _, p := range op.Params {
		if p.In != openapi3.ParameterInPath {
			continue
		}
		b := g.binds.For(op, p)
		if b == nil {
			continue
		}
		if r := g.resourceOfCreate(b.Producer); r != nil && len(g.recs[r]) > 0 {
			out = g.recs[r][0]
		}
	}
	return out
}

// fillable reports whether every path parameter of an operation has a
// value now: bound to a record that exists, or given by an override or
// the spec.
func (g *gen) fillable(op *spec.Operation, rc *rec) bool {
	for _, p := range op.Params {
		if p.In != openapi3.ParameterInPath {
			continue
		}
		if b := g.binds.For(op, p); b != nil {
			if _, ok := g.boundValue(op, p, rc); !ok {
				return false
			}
			continue
		}
		if _, ok := g.in.Overrides.value(0, op.ID+"."+p.Name, p.Name); ok {
			continue
		}
		if p.Example == nil && len(p.Examples) == 0 {
			return false
		}
	}
	return true
}

// cleanup deletes the records, the resources in reverse order, and reads
// a deleted record once more (404).
func (g *gen) cleanup() {
	for i := len(g.order) - 1; i >= 0; i-- {
		r := g.order[i]
		dels := r.OpsWith(model.RoleDelete)
		if len(dels) == 0 {
			continue
		}
		op := dels[0].Op
		list := g.recs[r]
		for j := len(list) - 1; j >= 0; j-- {
			rc := list[j]
			st := &scenario.Step{ID: g.id(rc.name + ".delete"), Call: op.Method + " " + op.Path, Expect: lowestSuccess(op), Notes: map[string]string{}}
			if st.Expect == 0 || !g.params(st, op, rc) {
				continue
			}
			st.Why = fmt.Sprintf("sure: delete %s", rc.name)
			g.used[op] = true
			g.steps = append(g.steps, st)
			if j == 0 {
				for _, mo := range r.OpsWith(model.RoleRead) {
					if !documents(mo.Op, http.StatusNotFound) {
						continue
					}
					rd := &scenario.Step{ID: g.id(rc.name + ".read.afterDelete"), Call: mo.Op.Method + " " + mo.Op.Path, Expect: http.StatusNotFound, Notes: map[string]string{}}
					if g.params(rd, mo.Op, rc) {
						rd.Why = "sure: read after delete"
						g.steps = append(g.steps, rd)
					}
				}
			}
		}
	}
}

func (g *gen) whyUncovered(op *spec.Operation) string {
	if lowestSuccess(op) == 0 {
		return "no 2xx response documented"
	}
	for _, p := range op.Params {
		if p.In == openapi3.ParameterInPath && g.binds.For(op, p) == nil {
			return fmt.Sprintf("no value for the path parameter {%s}: no create gives it and the spec has no example", p.Name)
		}
	}
	if mo := g.m.Op(op); mo == nil {
		return "the operation belongs to no resource the scenario creates"
	}
	return "the records it needs could not be created"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
