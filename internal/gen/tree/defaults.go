package tree

import (
	"strings"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// defaultWritePath is the value of a path parameter of a write of n when
// the file leaves it out: the value n reads it with (code=.code of the
// planet above becomes planets.code), else the key of the record
// (dockId=.id for a dock). ok is false when neither fits: the file must
// give it.
func defaultWritePath(n *Node, param string) (any, bool) {
	if n == nil {
		return nil, false
	}
	if v, ok := n.Path[param]; ok {
		s, isText := v.(string)
		switch {
		case !isText:
			return v, true
		case strings.Contains(s, "{{"):
			return nil, false
		case strings.HasPrefix(s, "."):
			// read under the record above: in a write "." is the record
			// itself, so name the node above
			if n.Parent == nil || s == "." {
				return nil, false
			}
			return n.Parent.Name + s, true
		}
		return v, true
	}
	for _, k := range n.Key {
		f := PointerField(k)
		if f == "" || strings.Contains(f, ".") {
			continue
		}
		if strings.EqualFold(param, f) || strings.EqualFold(param, singular(n.Name)+f) || strings.EqualFold(param, singular(n.Name)+"_"+f) {
			return "." + f, true
		}
	}
	return nil, false
}

// Complete gives the writes of the tree the path values the file leaves
// out (defaultWritePath). It changes the tree in memory only; the file
// keeps its short form.
func (t *Tree) Complete(s *spec.Spec) {
	t.Walk(func(n *Node) {
		if n.IsRequire {
			return
		}
		for _, w := range n.Write {
			if w.None || w.Call == "" {
				continue
			}
			var params []string
			if _, path, ok := strings.Cut(w.Call, " "); ok {
				for _, m := range placeholderRe.FindAllString(path, -1) {
					params = append(params, m[1:len(m)-1])
				}
			} else if op := lookupOp(s, w.Call); op != nil {
				params = pathParams(op)
			}
			for _, param := range params {
				if _, set := w.Path[param]; set {
					continue
				}
				if v, ok := defaultWritePath(n, param); ok {
					if w.Path == nil {
						w.Path = map[string]any{}
					}
					w.Path[param] = v
				}
			}
		}
	})
}
