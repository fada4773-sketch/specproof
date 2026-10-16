package tree

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/redact"
)

// PlanSection is the plan on top of the review: the order of the creates
// with the sources of their bodies, and the tree with the records of the
// last fetch, the writes of each node and where its body comes from.
func PlanSection(t *Tree, d *Data) string {
	var b strings.Builder
	if len(t.Order) > 0 {
		var parts []string
		for _, e := range t.Order {
			p := e.Node
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

// RequestsBlock lists requests with their bodies, folded: what each was
// for, method, target, status, duration, the body sent and the answer,
// secrets masked.
func RequestsBlock(title string, log []Exchange, red *redact.Redactor) string {
	if len(log) == 0 {
		return ""
	}
	if red == nil {
		red = redact.New(nil)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n<details><summary>%s (%d)</summary>\n\n", title, len(log))
	for i, ex := range log {
		status := fmt.Sprint(ex.Status)
		if ex.Error != "" {
			status = "no answer: " + red.String(ex.Error)
		}
		fmt.Fprintf(&b, "#### %d. %s %s → %s (%d ms)", i+1, ex.Method, red.String(ex.Target), status, ex.Ms)
		if ex.For != "" {
			fmt.Fprintf(&b, " · %s", ex.For)
		}
		b.WriteString("\n\n")
		if ex.HasRequest {
			b.WriteString("request\n\n" + jsonBlock(red.Value(ex.Request)))
		}
		if ex.Answer != nil {
			b.WriteString("answer\n\n" + jsonBlock(red.Value(ex.Answer)))
		}
	}
	b.WriteString("</details>\n")
	return b.String()
}

func jsonBlock(v any) string {
	if s, ok := v.(string); ok {
		return "```\n" + strings.TrimSpace(s) + "\n```\n\n"
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "```\n" + fmt.Sprint(v) + "\n```\n\n"
	}
	return "```json\n" + string(out) + "\n```\n\n"
}

// PlanText is the plan for the terminal: the order, and the tree with the
// writes of each node and where its body comes from.
func PlanText(t *Tree) string {
	var b strings.Builder
	if len(t.Order) > 0 {
		var parts []string
		for _, e := range t.Order {
			p := e.Node
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
