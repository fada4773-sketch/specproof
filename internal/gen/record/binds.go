package record

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// explicit writes every binding apitest would only guess (heuristic) as
// x-apitest-bind at its parameter, so apitest runs without the warning and
// a later change of the spec cannot change the guess. The source is the
// one the record file saves the value from where the entry of the same
// producer has one, else the guess, checked against the stored answer of
// the producer: an explicit binding reads only that one place, the guess
// tries the Location header and the request body too. A parameter several
// operations share gets the binding once if all of them need the same,
// else each operation gets its own copy.
func (w *writer) explicit(s *spec.Spec, f *File) {
	set, err := bind.Resolve(s)
	if err != nil {
		return // the lint after the run reports it
	}
	type want struct {
		op   *spec.Operation
		src  bind.Source
		from string
	}
	wants := map[*yaml.Node][]want{}
	var targets []*yaml.Node
	for _, op := range s.Ops {
		for _, b := range set.Of(op) {
			if b.Kind != bind.Heuristic {
				continue
			}
			src, ok := w.bindSource(f, b)
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
			wants[e.target] = append(wants[e.target], want{op, src, b.Producer.ID})
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
			same = same && x.from == ws[0].from && x.src == ws[0].src
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
			if node != nil && w.setBind(node, x.from, x.src) {
				w.res.Binds++
			}
			w.res.note(CodeBind, method(x.op), "{%s} takes its value from %s (%s); written as x-apitest-bind, apitest guessed it before",
				name, x.from, x.src)
		}
	}
}

// bindSource is the source a heuristic binding gets as x-apitest-bind; ok
// is false if the stored answer of the producer shows it would find no
// value there (a note says so).
func (w *writer) bindSource(f *File, b *bind.Binding) (bind.Source, bool) {
	producer, consumer := (*Step)(nil), (*Step)(nil)
	for _, st := range f.Steps {
		if st.Op == b.Producer && producer == nil {
			producer = st
		}
		if st.Op == b.Consumer && consumer == nil {
			consumer = st
		}
	}
	if consumer != nil {
		if _, m := mapping(consumer.Path); m[b.Param.Name] != nil {
			if g := whole.FindStringSubmatch(m[b.Param.Name].Value); g != nil {
				if saver, from := savedBy(f, consumer, g[1]); saver != nil {
					if saver.Op == b.Producer {
						if src, ok := saveSource(from); ok {
							return src, true
						}
					} else {
						w.res.note(CodeBind, consumer.where(), "{%s}: apitest takes it from %s (%s), the record file from %s (%s); x-apitest-bind keeps apitest's source. If the record file is right, declare {from: %s, …} at {%s}",
							b.Param.Name, b.Producer.ID, b.Source, saver.Op.ID, from, saver.Op.ID, b.Param.Name)
					}
				}
			}
		}
	}
	if producer == nil || producer.Response == nil || b.Source.Header != "" || b.Source.FromRequest {
		return b.Source, true
	}
	if _, ok := bind.Pointer(decode(producer.Response.Body), b.Source.Pointer); ok {
		return b.Source, true
	}
	if h := producer.Response.Headers["Location"]; h != "" && b.Source.Pointer == "/id" {
		return bind.Source{Header: "Location"}, true
	}
	if producer.Body != nil {
		if _, ok := bind.Pointer(decode(producer.Body), b.Source.Pointer); ok {
			return bind.Source{Pointer: b.Source.Pointer, FromRequest: true}, true
		}
	}
	w.res.note(CodeBind, method(b.Consumer), "{%s}: apitest guesses it from %s (%s), but the stored answer of %s has no value there; declare x-apitest-bind at {%s} yourself",
		b.Param.Name, b.Producer.ID, b.Source, producer, b.Param.Name)
	return bind.Source{}, false
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
func (w *writer) setBind(param *yaml.Node, from string, src bind.Source) bool {
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
