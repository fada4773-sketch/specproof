package tree

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PlanSection is the plan on top of the review: the order of the creates
// with the sources of their bodies, and the tree with the records of the
// last fetch, the writes of each node and where its body comes from.
func PlanSection(t *Tree, d *Data) string {
	var b strings.Builder
	if len(t.Order) > 0 {
		var parts []string
		for _, e := range t.Order {
			p := e.Key()
			var src []string
			for _, f := range sortedKeys(e.Values) {
				src = append(src, fmt.Sprintf("%s <- %v", f, e.Values[f]))
			}
			if len(src) > 0 {
				p += " (" + strings.Join(src, ", ") + ")"
			}
			if e.Note != "" {
				p += " *" + e.Note + "*"
			}
			parts = append(parts, p)
		}
		fmt.Fprintf(&b, "order: %s\n\n", strings.Join(parts, " → "))
	} else {
		b.WriteString("order: none in tree.yaml: apitest-gen orders the creates and finds the sources itself\n\n")
	}
	count := map[string]int{}
	if d != nil {
		for _, r := range d.Records {
			count[r.Node.Name]++
		}
	}
	b.WriteString("| Node | Records | Writes | Body from |\n|---|---|---|---|\n")
	var walk func(list []*Node, depth int)
	walk = func(list []*Node, depth int) {
		for _, n := range list {
			records := "–"
			switch {
			case n.Off:
				records = "off"
			case d != nil:
				records = fmt.Sprint(count[n.Name])
			}
			var writes []string
			from := ""
			for _, k := range n.Kinds() {
				w := n.Write[k]
				switch {
				case w.None:
					writes = append(writes, "seed")
				case w.Off || w.Gone:
				default:
					writes = append(writes, k)
				}
				if k == WriteCreate && !w.None {
					from = n.Name
					if w.From != "" {
						from = w.From
					}
				}
			}
			if e := t.Entry(n.Name); e != nil {
				for _, f := range sortedKeys(e.Values) {
					from += fmt.Sprintf(", %s <- %v", f, e.Values[f])
				}
			}
			fmt.Fprintf(&b, "| %s%s | %s | %s | %s |\n", strings.Repeat("· ", depth), n.Name, records, strings.Join(writes, ", "), cell(from))
			walk(n.Each, depth+1)
		}
	}
	walk(t.Roots, 0)
	return b.String()
}

// PlanText is the plan for the terminal: the order, and the tree with the
// writes of each node and where its body comes from.
func PlanText(t *Tree) string {
	var b strings.Builder
	if len(t.Order) > 0 {
		var parts []string
		for _, e := range t.Order {
			p := e.Key()
			var src []string
			for _, f := range sortedKeys(e.Values) {
				src = append(src, fmt.Sprintf("%s <- %v", f, e.Values[f]))
			}
			if len(src) > 0 {
				p += " (" + strings.Join(src, ", ") + ")"
			}
			if e.Note != "" {
				p += " [" + e.Note + "]"
			}
			parts = append(parts, p)
		}
		fmt.Fprintf(&b, "order  %s\n", strings.Join(parts, " → "))
	}
	first := true
	var walk func(list []*Node, depth int)
	walk = func(list []*Node, depth int) {
		for _, n := range list {
			label := "       "
			if first {
				label, first = "tree   ", false
			}
			var what []string
			for _, k := range n.Kinds() {
				w := n.Write[k]
				switch {
				case w.None:
					what = append(what, "seed")
				case !w.Off && !w.Gone:
					what = append(what, k)
				}
				if k == WriteCreate && !w.None && w.From != "" && w.From != n.Name {
					what = append(what, "body from "+w.From)
				}
			}
			if n.Off {
				what = append([]string{"off"}, what...)
			}
			name := strings.Repeat("  ", depth) + n.Name
			fmt.Fprintf(&b, "%s%-28s %s\n", label, name, strings.Join(what, " · "))
			walk(n.Each, depth+1)
		}
	}
	walk(t.Roots, 0)
	return b.String()
}

// FetchPlan are the requests a fetch would send, as templates: the call,
// the values it takes and per which record above.
func FetchPlan(t *Tree) []string {
	var out []string
	var walk func(list []*Node)
	walk = func(list []*Node) {
		for _, n := range list {
			if !n.Enabled() {
				continue
			}
			line := n.Call
			var vals []string
			for _, k := range sortedKeys(n.Path) {
				vals = append(vals, fmt.Sprintf("%s=%v", k, n.Path[k]))
			}
			for _, k := range sortedKeys(n.Query) {
				vals = append(vals, fmt.Sprintf("?%s=%v", k, n.Query[k]))
			}
			if n.HasBody {
				b, _ := json.Marshal(n.Body)
				vals = append(vals, "body "+string(b))
			}
			if len(vals) > 0 {
				line += "  " + strings.Join(vals, " ")
			}
			if n.Parent != nil {
				line += "  per " + n.Parent.Name
			}
			if len(n.Require) > 0 {
				line += fmt.Sprintf("  (+%d require per candidate)", len(n.Require))
			}
			out = append(out, line)
			walk(n.Each)
		}
	}
	walk(t.Roots)
	return out
}
