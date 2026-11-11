package tree

import (
	"fmt"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// checkReadKeys finds a node that reads the record a field of the record
// above names, through a path parameter that takes another key of that
// resource than the field holds: GET /moons/{moonNumber} with moonNumber:
// .MoonId sends an id where the API wants a number, the read answers 404
// (or another record), and the record above loses its source. The fix
// reads the record by the key the field holds, else the list of the
// resource with a where on that key; the node keeps its name, its pick,
// its write block and what hangs below it.
func checkReadKeys(s *spec.Spec, t *Tree) ([]string, []Fix) {
	m := modelOf(s)
	p := &proposer{m: m}
	var out []string
	var fixes []Fix
	t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone {
			return
		}
		wo, wf := writePathKeys(s, n)
		out, fixes = append(out, wo...), append(fixes, wf...)
		if !n.Enabled() || n.Parent == nil {
			return
		}
		op := lookupOp(s, n.Call)
		if op == nil || op.Method != http.MethodGet {
			return
		}
		mo := m.Op(op)
		params := pathParams(op)
		if mo == nil || mo.Resource == nil || len(params) != 1 {
			return
		}
		v, ok := n.Path[params[0]].(string)
		if !ok || !strings.HasPrefix(v, ".") || len(v) < 2 {
			return
		}
		field := v[1:]
		above := nodeResource(s, m, n.Parent)
		if above == nil {
			return
		}
		to, key := above.Ref(field)
		if to == nil || key == "" || p.creatable(to, key) != mo.Resource && to != mo.Resource {
			return
		}
		if exact, fits := readsByKey(mo.Resource, mo, key); exact || fits {
			return
		}
		if k := mo.Resource.Field(key); k != "" {
			key = k
		}
		need := refNeed{field: field, to: mo.Resource, key: key, many: n.Pick.All}
		what := fmt.Sprintf("%s: %s takes the %s of %s by {%s}, but %s holds its %s",
			n.Name, callOf(op), byField(mo), article(lowerFirst(mo.Resource.Name)), params[0], v, key)
		c, desc := sourceNode(s, t, n.Parent, need, nil)
		if c == nil {
			// no read of the record by that key the nodes above can send:
			// the node reads nothing that fits; it goes off
			why := fmt.Sprintf("reads the %s by its %s, but %s holds its %s; no read by %s in reach", lowerFirst(mo.Resource.Name), byField(mo), v, key, key)
			f := Fix{Line: n.Line, Change: n.Name + ": add off: true # " + why, apply: setNodeOff(n.Name, why)}
			out = append(out, fmt.Sprintf("line %d: %s", n.Line, what)+f.change())
			fixes = append(fixes, f)
			return
		}
		f := Fix{Line: n.Line, fetch: []string{n.Parent.Name},
			Change: fmt.Sprintf("%s: %s %s=%s → %s; then fetch again with -node %s", n.Name, callOf(op), params[0], v, desc, n.Parent.Name),
			apply:  replaceRead(n.Name, c)}
		out = append(out, fmt.Sprintf("line %d: %s", n.Line, what)+f.change())
		fixes = append(fixes, f)
	})
	return out, fixes
}

// writePathKeys finds a write of n whose path takes a key of n's records by
// another field of them than the API wants: PUT /moons/{moonNumber}
// with moonNumber=.Id, where the parameter holds the Number. The fix
// names that field (moonNumber=.Number).
func writePathKeys(s *spec.Spec, n *Node) ([]string, []Fix) {
	m := modelOf(s)
	own := nodeResource(s, m, n)
	if own == nil {
		return nil, nil
	}
	var out []string
	var fixes []Fix
	for _, kind := range n.Kinds() {
		w := n.Write[kind]
		if kind == WriteCreate || w == nil || w.None || w.Off || w.Gone || w.value == nil {
			continue
		}
		mo := m.Op(lookupOp(s, w.Call))
		if mo == nil {
			continue
		}
		for _, p := range sortedKeys(w.Path) {
			v, ok := w.Path[p].(string)
			mp := mo.Param(p)
			if !ok || !strings.HasPrefix(v, ".") || mp == nil || mp.Resource != own || mp.Field == "" ||
				strings.EqualFold(mp.Field, v[1:]) || own.Field(mp.Field) == "" || strings.EqualFold(v[1:], p) {
				// a field named like the parameter is the user's choice
				continue
			}
			to := "." + own.Field(mp.Field)
			target := w.value
			f := Fix{Line: w.Line, Change: fmt.Sprintf("%s.write.%s: %s=%s → %s=%s", n.Name, kind, p, v, p, to),
				apply: func(*Tree) bool { return replaceRef(target, v, to) }}
			out = append(out, fmt.Sprintf("line %d: %s.write.%s: {%s} takes the %s of %s, but %s is its %s",
				w.Line, n.Name, kind, p, mp.Field, article(lowerFirst(own.Name)), v, v[1:])+f.change())
			fixes = append(fixes, f)
		}
	}
	return out, fixes
}

// replaceRead gives a node the request of c (call, path, query, where,
// key); its name, pick, write block and children stay.
func replaceRead(name string, c *Node) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.value == nil {
			return false
		}
		v := nodeMapping(n)
		if v == nil || v.Kind != yaml.MappingNode {
			return false
		}
		set := func(key string, val *yaml.Node) {
			for i := 0; i+1 < len(v.Content); i += 2 {
				if v.Content[i].Value == key {
					if val == nil {
						v.Content = append(v.Content[:i], v.Content[i+2:]...)
					} else {
						v.Content[i+1] = val
					}
					return
				}
			}
			if val == nil {
				return
			}
			// after call and its values, before what the user keeps below
			at := len(v.Content)
			for i := 0; i+1 < len(v.Content); i += 2 {
				switch v.Content[i].Value {
				case "pick", "candidates", "require", "key", "save", "fields", "write", "each", "off", "items", "page":
					if i < at {
						at = i
					}
				}
			}
			v.Content = append(v.Content[:at], append([]*yaml.Node{scalar(key), val}, v.Content[at:]...)...)
		}
		call := scalar(c.Call)
		call.LineComment = comment(c.Comment)
		set("call", call)
		yamlOrNil := func(m map[string]any) *yaml.Node {
			if len(m) == 0 {
				return nil
			}
			return valueYAML(m, true)
		}
		set("path", yamlOrNil(c.Path))
		set("query", yamlOrNil(c.Query))
		var where *yaml.Node
		if len(c.Where) > 0 {
			where = &yaml.Node{Kind: yaml.MappingNode}
			for _, cl := range c.Where {
				x := valueYAML(cl.Test, true)
				x.LineComment = comment(cl.Note)
				where.Content = append(where.Content, scalar(cl.Field), x)
			}
		}
		set("where", where)
		if len(c.Key) > 0 {
			k := stringsYAML(c.Key)
			setFlow(k)
			set("key", k)
		}
		return true
	}
}
