package record

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// explicit writes every binding apitest would only guess (heuristic) as
// x-apitest-bind at its parameter, so apitest runs without the warning and
// a later change of the spec cannot change the guess. The record file
// decides: a value it saves becomes a binding to the entry that saves it,
// whichever operation apitest guessed; a fixed value (written in the entry
// or taken from vars) becomes x-apitest-bind: false, so apitest sends the
// example. Without either, the guess is checked against the stored answer
// of the producer: an explicit binding reads only that one place, the guess
// tries the Location header and the request body too. A parameter several
// operations share gets the binding once if all of them need the same,
// else each operation gets its own copy.
func (w *writer) explicit(s *spec.Spec, f *File) {
	set, err := bind.Resolve(s)
	if err != nil {
		return // the lint after the run reports it
	}
	type want struct {
		op *spec.Operation
		bound
	}
	wants := map[*yaml.Node][]want{}
	var targets []*yaml.Node
	for _, op := range s.Ops {
		for _, b := range set.Of(op) {
			if b.Kind != bind.Heuristic {
				continue
			}
			bd, ok := w.bindSource(f, b)
			if !ok {
				continue
			}
			e := w.paramEntry(op, b.Param.Name, b.Param.In)
			if e == nil {
				continue
			}
			if wants[e.target] == nil {
				targets = append(targets, e.target)
			}
			wants[e.target] = append(wants[e.target], want{op, bd})
		}
	}
	for _, target := range targets {
		ws := wants[target]
		name, in := "", ""
		for _, p := range ws[0].op.Params {
			if e := w.paramEntry(ws[0].op, p.Name, p.In); e != nil && e.target == target {
				name, in = p.Name, p.In
			}
		}
		users := 0
		for _, op := range s.Ops {
			if e := w.paramEntry(op, name, in); e != nil && e.target == target {
				users++
			}
		}
		same := users == len(ws)
		for _, x := range ws[1:] {
			same = same && x.declares(ws[0].bound)
		}
		for i, x := range ws {
			node := target
			if !same {
				e := w.paramEntry(x.op, name, in)
				if e.shared() {
					e = w.ownParam(x.op, e)
				}
				node = e.target
			} else if i > 0 {
				node = nil // written once for all
			}
			if node != nil && w.setBind(node, x.bound) {
				w.res.Binds++
			}
			w.res.note(CodeBind, method(x.op), "%s", x.message(name))
		}
	}
}

// bound is what x-apitest-bind declares for a guessed binding: the
// producer and source, or off (false) for a fixed value.
type bound struct {
	from  string
	src   bind.Source
	off   bool
	fixed string // the fixed value, for the note
	guess *bind.Binding
}

// declares reports whether two bindings write the same x-apitest-bind.
func (bd bound) declares(o bound) bool {
	if bd.off || o.off {
		return bd.off == o.off
	}
	return bd.from == o.from && bd.src == o.src
}

// message is the note on a written binding.
func (bd bound) message(param string) string {
	guess := fmt.Sprintf("%s (%s)", bd.guess.Producer.ID, bd.guess.Source)
	switch {
	case bd.off:
		return fmt.Sprintf("{%s} has the fixed value %s in the record file; written as x-apitest-bind: false, apitest guessed it from %s before", param, bd.fixed, guess)
	case bd.from != bd.guess.Producer.ID || bd.src != bd.guess.Source:
		return fmt.Sprintf("{%s} takes its value from %s (%s), as the record file saves it; written as x-apitest-bind, apitest guessed %s before", param, bd.from, bd.src, guess)
	}
	return fmt.Sprintf("{%s} takes its value from %s (%s); written as x-apitest-bind, apitest guessed it before", param, bd.from, bd.src)
}

// bindSource is what a heuristic binding gets as x-apitest-bind; ok is
// false if the stored answer of the producer shows it would find no value
// there (a note says so).
func (w *writer) bindSource(f *File, b *bind.Binding) (bound, bool) {
	producer, consumer := (*Step)(nil), (*Step)(nil)
	for _, st := range f.Steps {
		if st.Op == b.Producer && producer == nil {
			producer = st
		}
		if st.Op == b.Consumer && consumer == nil {
			consumer = st
		}
	}
	guessed := bound{from: b.Producer.ID, src: b.Source, guess: b}
	if consumer != nil {
		if _, m := mapping(consumer.Path); m[b.Param.Name] != nil {
			v := m[b.Param.Name]
			g := whole.FindStringSubmatch(v.Value)
			var saver *Step
			from := ""
			if g != nil {
				saver, from = savedBy(f, consumer, g[1])
			}
			switch {
			case saver != nil:
				if src, ok := saveSource(from); ok {
					return bound{from: saver.Op.ID, src: src, guess: b}, true
				}
				if saver.Op != b.Producer {
					// the record file's source cannot be declared; its
					// recorded value is the example
					return bound{off: true, fixed: fmt.Sprintf("{{%s}} (%s %s)", g[1], saver.Op.ID, from), guess: b}, true
				}
			case consumer.status() == StatusIgnore:
				// an ignored entry is not sent; its fixed values may be
				// replaced by generated ones
			case g == nil || slices.ContainsFunc(f.Vars, func(x Var) bool { return x.Name == g[1] }):
				filled, _ := fill(v, f.values())
				return bound{off: true, fixed: text(decode(filled)), guess: b}, true
			}
		}
	}
	if producer == nil || producer.Response == nil || b.Source.Header != "" || b.Source.FromRequest {
		return guessed, true
	}
	if _, ok := bind.Pointer(decode(producer.Response.Body), b.Source.Pointer); ok {
		return guessed, true
	}
	if h := producer.Response.Headers["Location"]; h != "" && b.Source.Pointer == "/id" {
		guessed.src = bind.Source{Header: "Location"}
		return guessed, true
	}
	if producer.Body != nil {
		if _, ok := bind.Pointer(decode(producer.Body), b.Source.Pointer); ok {
			guessed.src = bind.Source{Pointer: b.Source.Pointer, FromRequest: true}
			return guessed, true
		}
	}
	w.res.note(CodeBind, method(b.Consumer), "{%s}: apitest guesses it from %s (%s), but the stored answer of %s has no value there; declare x-apitest-bind at {%s} yourself",
		b.Param.Name, b.Producer.ID, b.Source, producer, b.Param.Name)
	return bound{}, false
}

// savedBy finds the entry before st that saves a value under name, and the
// source it saves it from.
func savedBy(f *File, st *Step, name string) (*Step, string) {
	var saver *Step
	from := ""
	for _, s := range f.Steps {
		if s == st {
			break
		}
		for _, sv := range s.Save {
			if sv.Name == name && s.Op != nil {
				saver, from = s, sv.From
			}
		}
	}
	return saver, from
}

// saveSource converts the source of a save into one x-apitest-bind can
// declare: a pointer into the answer or the request, or a header.
func saveSource(from string) (bind.Source, bool) {
	switch {
	case strings.HasPrefix(from, "header "):
		return bind.Source{Header: strings.TrimPrefix(from, "header ")}, true
	case strings.HasPrefix(from, "request /") && from != "request /":
		return bind.Source{Pointer: strings.TrimPrefix(from, "request "), FromRequest: true}, true
	case strings.HasPrefix(from, "/") && from != "/":
		return bind.Source{Pointer: from}, true
	}
	return bind.Source{}, false
}

// setBind writes x-apitest-bind at a parameter; it reports whether the
// parameter changed.
func (w *writer) setBind(param *yaml.Node, bd bound) bool {
	from, src := bd.from, bd.src
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
	m.Content = append(m.Content, scalarNode("from"), scalarNode(from))
	if src.Header != "" {
		m.Content = append(m.Content, scalarNode("header"), scalarNode(src.Header))
	} else {
		m.Content = append(m.Content, scalarNode("pointer"), scalarNode(src.Pointer))
	}
	if src.FromRequest {
		m.Content = append(m.Content, scalarNode("source"), scalarNode("request"))
	}
	if bd.off {
		from = bd.guess.Consumer.ID
		m = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}
	}
	if cur := yamldoc.Get(param, "x-apitest-bind"); cur != nil && compare.Equal(text(decode(cur)), text(decode(m))) {
		return false
	}
	if err := yamldoc.SetNode(param, "x-apitest-bind", m); err != nil {
		w.res.problem(CodeShared, from, "x-apitest-bind cannot be written: %v", err)
		return false
	}
	w.changed = true
	return true
}
