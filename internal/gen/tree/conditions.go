package tree

import (
	"fmt"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// maxRequires limits the requires the proposal gives a node: each costs a
// request per candidate.
const maxRequires = 3

// requireCandidates is how many candidates a proposed require checks.
const requireCandidates = 20

// conditions gives the list nodes of the tree the where and require their
// children and operationIds suggest, each with the reason as comment:
//
//   - a read placed below the node for a filter its operationId names
//     (GetGardensByPersonName below the persons): require it with min: 1,
//     the field it reads present;
//   - a read about the record that answers 404 without it (details):
//     require it;
//   - the first list of records that belong to the record (beds of a
//     garden): require it with min: 1;
//   - a field a child reads to name another record (personId): present;
//   - state words of the node's own operationId (GetOpenGardens: status
//     OPEN), With (GetGardensWithBeds: beds present, or require the list);
//   - an action of its write block (open) whose create sends the state:
//     the states the action starts from (status: [DRAFT, CLOSED]).
func (p *proposer) conditions() {
	for _, n := range p.order() {
		op := p.lookup(n.Call)
		if n.Off || op == nil || !isList(op) && !n.Read {
			continue
		}
		c := &cond{p: p, n: n}
		c.children()
		c.own(op)
		c.actions()
		if len(c.reqs) > 0 {
			n.Require = c.reqs
			if n.Candidates == 0 {
				n.Candidates = requireCandidates
			}
		}
	}
}

type cond struct {
	p    *proposer
	n    *Node
	reqs []*Node
}

func (c *cond) require(from *Node, least int, note string) {
	if len(c.reqs) >= maxRequires || slices.ContainsFunc(c.reqs, func(x *Node) bool { return x.Call == from.Call }) {
		return
	}
	// a value of params may not be set yet: such a require would drop
	// every candidate
	unset := false
	walkStrings(anyMap(from.Path), func(s string) { _, isParam := ParamRef(s); unset = unset || isParam })
	walkStrings(anyMap(from.Query), func(s string) { _, isParam := ParamRef(s); unset = unset || isParam })
	if unset {
		return
	}
	c.reqs = append(c.reqs, &Node{Name: from.Name, Call: from.Call, Path: copyMap(from.Path), Query: copyMap(from.Query),
		Comment: from.Comment, Note: note, Min: least, IsRequire: true, Parent: c.n})
}

func (c *cond) where(field string, test any, note string) {
	if slices.ContainsFunc(c.n.Where, func(x Clause) bool { return strings.EqualFold(x.Field, field) }) {
		return
	}
	c.n.Where = append(c.n.Where, Clause{Field: field, Test: test, Note: note})
}

// ownFields are the fields of the record above a node reads (".name").
func ownFields(n *Node) []string {
	var out []string
	for _, m := range []map[string]any{n.Path, n.Query} {
		for _, k := range sortedKeys(m) {
			if s, ok := m[k].(string); ok && strings.HasPrefix(s, ".") && len(s) > 1 && !slices.Contains(out, s[1:]) {
				out = append(out, s[1:])
			}
		}
	}
	return out
}

func (c *cond) children() {
	p, n, r := c.p, c.n, c.p.res[c.n]
	listed := false
	for _, ch := range n.Each {
		op := p.lookup(ch.Call)
		if op == nil || ch.Off {
			continue
		}
		// a filter that is a key of the node itself (listBedsByGardenCode
		// below the gardens) is the list below it by another key: one
		// require for the lists below
		if h, ok := p.placedBy[ch]; ok && r != nil && h.res.Name == r.Name && isKeyOf(n, h.field) && p.res[ch] != nil && p.res[ch].Parent == r {
			if isList(op) && !listed {
				listed = true
				c.require(ch, 1, fmt.Sprintf("only %s with %s", n.Name, ch.Name))
			}
			continue
		}
		if h, ok := p.placedBy[ch]; ok {
			c.require(ch, 1, fmt.Sprintf("only %s with an answer of %s", n.Name, op.ID))
			for _, f := range ownFields(ch) {
				if !isKeyOf(n, f) {
					c.where(f, "present", fmt.Sprintf("%s filters by it (By %s)", op.ID, h.phrase))
				}
			}
			continue
		}
		about := p.aboutRecord(ch, n)
		switch {
		case about && !isList(op) && !p.view(ch, r) && documents(op, 404):
			c.require(ch, 0, op.ID+" answers 404 without it")
		case about && isList(op) && !listed && r != nil && p.res[ch] != nil && p.res[ch].Parent == r:
			listed = true
			c.require(ch, 1, fmt.Sprintf("only %s with %s", n.Name, ch.Name))
		case !about:
			// a field that names another record (pilotId)
			for _, f := range ownFields(ch) {
				c.where(f, "present", ch.Name+" reads it")
			}
		}
	}
}

func isKeyOf(n *Node, field string) bool {
	return slices.ContainsFunc(keyOf(n), func(k string) bool { return strings.EqualFold(PointerField(k), field) })
}

// view reports whether a read of a node answers with the record of r
// itself (GET /gardens/{gardenId}), not with a DTO of its own about it
// (details), even if the model counts that DTO to the resource.
func (p *proposer) view(n *Node, r *model.Resource) bool {
	op := p.lookup(n.Call)
	if op == nil || r == nil || p.res[n] != r {
		return false
	}
	ref := responseRef(op, lowestSuccess(op))
	return ref == nil || len(r.Schemas) == 0 || dict.DTORef(ref) == r.Schemas[0]
}

// own reads the node's own operationId: state words and With.
func (c *cond) own(op *spec.Operation) {
	p, n, r := c.p, c.n, c.p.res[c.n]
	if r == nil || !op.HasOperationID {
		return
	}
	name := parseOpName(op.ID)
	if f, vals := states(r, name.subject); len(vals) > 0 {
		c.where(f, oneOrList(vals), fmt.Sprintf("%s: %s", op.ID, strings.Join(vals, ", ")))
	}
	for _, ws := range name.with {
		if f := r.Field(lowerFirst(pascal(ws))); f != "" {
			c.where(f, "present", op.ID+": With "+pascal(ws))
			continue
		}
		// a list of records below: require one
		for _, child := range p.m.Resources {
			if child.Parent != r || !sameName(child.Name, ws) {
				continue
			}
			for _, lo := range child.OpsWith(model.RoleList) {
				vals, missing := p.paramValues(lo.Op, &chain{res: r, node: n})
				if len(missing) > 0 {
					continue
				}
				q := &Node{Name: lowerFirst(pascal(ws)), Call: callOf(lo.Op), Comment: lo.Op.ID}
				for _, nd := range p.needs(lo.Op) {
					q.set(nd, vals[nd.name])
				}
				c.require(q, 1, op.ID+": With "+pascal(ws))
				break
			}
		}
	}
}

// actions: an action of the write block needs records in the states it
// starts from, when the create sends the state (else a created record is
// in the state the API gives it).
func (c *cond) actions() {
	p, n, r := c.p, c.n, c.p.res[c.n]
	if r == nil {
		return
	}
	f := stateField(r)
	create := n.Write[WriteCreate]
	if f == "" || create == nil || create.None {
		return
	}
	cop := p.lookup(create.Call)
	if cop == nil || requestRef(cop) == nil {
		return
	}
	if _, ok := propertyFoldIn(mustProps(cop), f); !ok {
		return
	}
	values := enumValues(r, f)
	allowed := values
	var why []string
	for _, kind := range n.Kinds() {
		if !IsAction(kind) {
			continue
		}
		verb := words(kind)[0]
		from := actionStates(values, verb)
		if from == nil {
			continue
		}
		allowed = slices.DeleteFunc(slices.Clone(allowed), func(v string) bool { return !slices.Contains(from, v) })
		why = append(why, kind)
	}
	if len(why) == 0 || len(allowed) == len(values) {
		return
	}
	if len(allowed) == 0 {
		c.n.Note = strings.TrimSpace(c.n.Note + " " + fmt.Sprintf("no %s fits every action (%s)", f, strings.Join(why, ", ")))
		return
	}
	c.where(f, oneOrList(allowed), fmt.Sprintf("%s starts from it, and the create sends %s", strings.Join(why, ", "), f))
}

func mustProps(op *spec.Operation) openapi3.Schemas {
	ref := requestRef(op)
	if ref == nil || ref.Value == nil {
		return nil
	}
	props, _ := properties(ref.Value)
	return props
}

func oneOrList(vals []string) any {
	if len(vals) == 1 {
		return vals[0]
	}
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
	}
	return out
}
