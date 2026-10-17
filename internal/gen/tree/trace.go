package tree

import (
	"fmt"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/redact"
)

// Visit is one run of a node under a record, for the review page: the
// values it sent and where they came from, its requests, its conditions,
// and per candidate why it was taken or not, with the visits below it.
type Visit struct {
	Node  string `json:"node"`
	Under string `json:"under,omitempty"`
	Call  string `json:"call"`
	Op    string `json:"op,omitempty"`
	// Skipped says why the node took nothing: off, a $name without value,
	// a value it could not read, an answer out of 2xx.
	Skipped  string         `json:"skipped,omitempty"`
	Values   []TraceValue   `json:"values,omitempty"`
	Requests []TraceRequest `json:"requests,omitempty"`
	// Where are the conditions of the records: the node's where and
	// where any, the where and prefer of the file.
	Where []string `json:"where,omitempty"`
	// Pick says what pick asks for, Order how the candidates were ordered.
	Pick  string `json:"pick,omitempty"`
	Order string `json:"order,omitempty"`
	// Total is the number of candidates the answers held, Picked the
	// records taken, Want what pick asks for.
	Total  int  `json:"total"`
	Picked int  `json:"picked"`
	Want   int  `json:"want,omitempty"`
	Single bool `json:"single,omitempty"`
	// Notes are the notes of the fetch about this visit.
	Notes      []string          `json:"notes,omitempty"`
	Candidates []*TraceCandidate `json:"candidates,omitempty"`
	// Hidden counts the candidates the page does not list.
	Hidden int `json:"hidden,omitempty"`
}

// TraceValue is a value of a request: as the file writes it (From) and as
// it was sent.
type TraceValue struct {
	Name  string `json:"name"`
	In    string `json:"in"`
	From  any    `json:"from,omitempty"`
	Value any    `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

// TraceRequest is a request a visit sent or took from the cache; Log is its
// line in the requests of the fetch (1-based, 0 for a cached answer).
type TraceRequest struct {
	For    string `json:"for,omitempty"`
	Method string `json:"method"`
	Target string `json:"target"`
	Status int    `json:"status,omitempty"`
	Cached bool   `json:"cached,omitempty"`
	Error  string `json:"error,omitempty"`
	Log    int    `json:"log,omitempty"`
}

// The outcomes of a candidate.
const (
	OutcomePicked  = "picked"
	OutcomeWhere   = "where"
	OutcomeRequire = "require"
	OutcomeSource  = "source"
	OutcomeLimit   = "not needed"
	OutcomeChecks  = "not checked"
)

// TraceCandidate is an element of an answer: taken as a record, or why
// not, with the requires checked on it and the visits below the record.
type TraceCandidate struct {
	Key      string          `json:"key,omitempty"`
	Outcome  string          `json:"outcome"`
	Why      string          `json:"why,omitempty"`
	Record   string          `json:"record,omitempty"`
	Value    any             `json:"value,omitempty"`
	Requires []*TraceRequire `json:"requires,omitempty"`
	Each     []*Visit        `json:"each,omitempty"`
}

// TraceRequire is one require checked on a candidate.
type TraceRequire struct {
	Name     string         `json:"name"`
	Call     string         `json:"call"`
	Values   []TraceValue   `json:"values,omitempty"`
	Where    []string       `json:"where,omitempty"`
	Requests []TraceRequest `json:"requests,omitempty"`
	Held     bool           `json:"held"`
	Why      string         `json:"why,omitempty"`
}

// traceLimit is the number of candidates a visit lists; a list answer of
// thousands would make the page slow.
const traceLimit = 200

// newVisit starts the visit of a node under a record and hangs it where
// the fetch is: at the top, or below the candidate being taken.
func (f *fetcher) newVisit(n *Node, parent *Record) *Visit {
	v := &Visit{Node: n.Name, Call: n.Call, Op: n.Comment}
	if parent != nil {
		v.Under = parent.ID
	}
	*f.attach = append(*f.attach, v)
	return v
}

// traced adds a request to the visit or require being traced.
func (f *fetcher) traced(r TraceRequest) {
	if f.reqs != nil {
		*f.reqs = append(*f.reqs, r)
	}
}

// traceValues are the values of a node's request under a record: each
// path, query and header value and the body, as written and as read.
func (f *fetcher) traceValues(n *Node, parent *Record) []TraceValue {
	var out []TraceValue
	add := func(in string, m map[string]any) {
		for _, k := range sortedKeys(m) {
			tv := TraceValue{Name: k, In: in, From: m[k]}
			if val, err := resolveFetch(m[k], parent, f.t, f.scope, f.first); err != nil {
				tv.Error = err.Error()
			} else {
				tv.Value = val
			}
			out = append(out, tv)
		}
	}
	add("path", n.Path)
	add("query", n.Query)
	add("header", n.Header)
	if n.HasBody {
		tv := TraceValue{Name: "body", In: "body", From: n.Body}
		if val, err := resolveFetch(n.Body, parent, f.t, f.scope, f.first); err != nil {
			tv.Error = err.Error()
		} else {
			tv.Value = val
		}
		out = append(out, tv)
	}
	return out
}

// traceWhere lists the conditions the records of a node must hold.
func (f *fetcher) traceWhere(n *Node) []string {
	var out []string
	for _, c := range n.Where {
		out = append(out, clauseText(c))
	}
	if len(n.WhereAny) > 0 {
		var anyOf []string
		for _, c := range n.WhereAny {
			anyOf = append(anyOf, clauseText(c))
		}
		out = append(out, "any of: "+strings.Join(anyOf, " | "))
	}
	for _, c := range f.globals(n) {
		out = append(out, clauseText(c)+" (where of the file)")
	}
	for _, c := range f.prefers(n) {
		out = append(out, clauseText(c)+" (prefer of the file: first, not a must)")
	}
	return out
}

func clauseText(c Clause) string {
	s := c.Field + ": " + testText(c.Test)
	if c.Note != "" {
		s += " # " + c.Note
	}
	return s
}

// whyNot says which condition a candidate fails, "" when it passes: the
// clause, the value it was tested with, and the value of the record.
func (f *fetcher) whyNot(n *Node, rec any, parent *Record) string {
	_, why := f.failedClause(n, rec, parent)
	return why
}

// failedClause is the clause a candidate fails (as where names it) and
// why; "" when it passes.
func (f *fetcher) failedClause(n *Node, rec any, parent *Record) (string, string) {
	fill := f.whereValue(parent)
	failed := func(c Clause, tested any) (string, string) {
		s := "where " + c.Field + ": " + testText(c.Test)
		label := s
		if fmt.Sprint(tested) != fmt.Sprint(c.Test) {
			if _, none := tested.(noValue); none {
				s += " (no value here)"
			} else {
				s += " (here " + testText(tested) + ")"
			}
		}
		if v, ok := Field(rec, c.Field); ok && v != nil {
			return label, s + "; the record has " + encodeJSON(v)
		}
		return label, s + "; the record has no " + c.Field
	}
	for _, c := range n.Where {
		x := c
		x.Test = fill(c.Test)
		if !x.holds(rec) {
			return failed(c, x.Test)
		}
	}
	if len(n.WhereAny) > 0 {
		held := false
		for _, c := range n.WhereAny {
			x := c
			x.Test = fill(c.Test)
			if x.holds(rec) {
				held = true
				break
			}
		}
		if !held {
			var anyOf []string
			for _, c := range n.WhereAny {
				anyOf = append(anyOf, clauseText(c))
			}
			s := "where any: holds none of " + strings.Join(anyOf, " | ")
			return s, s
		}
	}
	for _, c := range f.globals(n) {
		if !c.holds(rec) {
			label, why := failed(c, c.Test)
			return label + " (where of the file)", why + " (where of the file)"
		}
	}
	return "", ""
}

// whereCounts tells per clause how many candidates it left out, with the
// value of the first: capacity: >=5 ×2 (e.g. 103 has 4).
func whereCounts(out []*TraceCandidate, labels []string) string {
	var order []string
	count, first := map[string]int{}, map[string]*TraceCandidate{}
	for i, c := range out {
		l := labels[i]
		if count[l] == 0 {
			order = append(order, l)
			first[l] = c
		}
		count[l]++
	}
	var parts []string
	for _, l := range order {
		c := first[l]
		has := c.Why
		if _, after, ok := strings.Cut(c.Why, "; the record "); ok {
			has = strings.TrimSuffix(after, " (where of the file)")
		}
		parts = append(parts, fmt.Sprintf("%s ×%d (e.g. %s %s)", strings.TrimPrefix(l, "where "), count[l], c.Key, has))
	}
	return strings.Join(parts, "; ")
}

// pickText says what pick asks for.
func pickText(n *Node) string {
	p := n.Pick
	s := ""
	switch {
	case len(p.Keys) > 0:
		var ks []string
		for _, k := range p.Keys {
			ks = append(ks, text(k))
		}
		s = "the keys [" + strings.Join(ks, ", ") + "]"
	case p.All:
		s = "all"
	case p.First:
		s = fmt.Sprintf("the first %d", max(p.N, 1))
	default:
		s = fmt.Sprintf("%d at random (seed)", max(p.N, 1))
	}
	if n.Candidates > 0 && len(n.Require) > 0 {
		s += fmt.Sprintf("; require is checked on the first %d candidates", n.Candidates)
	}
	return s
}

// FetchTrace is the fetch as a tree for the page, with secrets and the
// token taken out.
func FetchTrace(d *Data, red *redact.Redactor) []*Visit {
	if red == nil {
		red = redact.New(nil)
	}
	reqs := func(list []TraceRequest) {
		for i := range list {
			list[i].Target = red.String(list[i].Target)
			list[i].Error = red.String(list[i].Error)
		}
	}
	vals := func(list []TraceValue) {
		for i := range list {
			list[i].Value = red.Value(list[i].Value)
			list[i].Error = red.String(list[i].Error)
		}
	}
	var walk func([]*Visit)
	walk = func(list []*Visit) {
		for _, v := range list {
			v.Skipped = red.String(v.Skipped)
			vals(v.Values)
			reqs(v.Requests)
			for i, n := range v.Notes {
				v.Notes[i] = red.String(n)
			}
			for _, c := range v.Candidates {
				c.Value = red.Value(c.Value)
				c.Why = red.String(c.Why)
				for _, r := range c.Requires {
					vals(r.Values)
					reqs(r.Requests)
					r.Why = red.String(r.Why)
				}
				walk(c.Each)
			}
		}
	}
	walk(d.Trace)
	if d.Trace == nil {
		return []*Visit{}
	}
	return d.Trace
}
