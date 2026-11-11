package tree

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Fix is a change of tree.yaml that solves a problem: Change says it in
// short ("order: move docks above ships"), apitest-gen tree -fix makes it.
type Fix struct {
	Line   int
	Change string
	apply  func(t *Tree) bool
	// adds is the node the fix adds below the node under; fetch the node
	// the records must be fetched again from ("" when none).
	adds, under string
	fetch       []string
	// why says in short why -fix cannot make a fix without apply.
	why string
}

// change is the line a problem ends with: what to change where; a fix
// with apply is made by -fix.
func (f Fix) change() string {
	if f.Line == 0 {
		return "\n  next: " + f.Change
	}
	s := fmt.Sprintf("\n  change line %d: %s", f.Line, f.Change)
	if f.apply != nil {
		s += " (-fix does it)"
	}
	return s
}

// NeedsFetch reports whether the records must be fetched again after the
// fix: it adds a node or a condition.
func (f Fix) NeedsFetch() bool { return len(f.fetch) > 0 || strings.Contains(f.Change, "fetch again") }

// FetchNodes are the nodes whose records must be fetched again after the
// fix (-node), none when the fix names none (then the whole tree).
func (f Fix) FetchNodes() []string {
	if len(f.fetch) > 0 {
		return f.fetch
	}
	if _, after, ok := strings.Cut(f.Change, "fetch again with -node "); ok {
		return []string{strings.Fields(after)[0]}
	}
	return nil
}

// Fixes are the changes -fix makes for the problems Check finds.
func Fixes(s *spec.Spec, t *Tree) []Fix {
	_, fixes := check(s, t)
	return fixes
}

// maxFixes bounds the fixes of one Repair: each fix solves a problem, a
// file has not that many.
const maxFixes = 200

// Repair makes the fixes of Check one by one, each on the file as the fixes
// before left it (the lines move), and returns the tree read again and the
// changes made ("line 23: order: move docks above ships").
func Repair(s *spec.Spec, t *Tree) (*Tree, []string, error) {
	var done []string
	// per kind of change (its text without numbers): how often it was
	// made and how many fixes were left when it came first
	type kind struct{ made, left int }
	same := map[string]*kind{}
	// the defaults of -fix (below) may bring new problems (a value of the
	// node above changes what a create takes): both go again until the
	// defaults change nothing
	for range maxRounds {
		for range maxFixes {
			fixes := Fixes(s, t)
			if len(fixes) == 0 {
				break
			}
			next, made, err := applyFix(t, fixes[0])
			if err != nil {
				return t, done, err
			}
			if slices.Contains(done, made) {
				// the change did not solve the problem: stop, Check names it
				return next, done, nil
			}
			k := same[digits.ReplaceAllString(made, "#")]
			if k == nil {
				k = &kind{left: len(fixes)}
				same[digits.ReplaceAllString(made, "#")] = k
			}
			if k.made++; k.made > 3 && len(fixes) >= k.left {
				// the same change again and again under new names, and no
				// fewer problems for it (a node added that does not solve
				// it): stop, Check names it. Changes of many nodes whose
				// names differ by a number only (off for node2 … node20)
				// solve one problem each and go on.
				return t, done, nil
			}
			done = append(done, made)
			t = next
		}
		// the defaults of -fix that solve no problem: the name an update
		// changes (each found again on the file the one before left)
		defaults := 0
		for range maxFixes {
			fixes := slices.Concat(sideRefs(s, t), upsertDefaults(s, t), changeDefaults(s, t), seedDefaults(s, t))
			if len(fixes) == 0 {
				break
			}
			next, made, err := applyFix(t, fixes[0])
			if err != nil {
				return t, done, err
			}
			done = append(done, made)
			t = next
			defaults++
		}
		if defaults == 0 {
			break
		}
	}
	return t, done, nil
}

// maxRounds bounds the rounds of Repair: fixes of the problems, then the
// defaults; a default rarely brings a problem the round before did not.
const maxRounds = 5

// ApplyFixes makes the fixes a build found (they change tree.yaml; the
// records are fetched again after them) and returns the tree read again
// and the changes made. A fix that does not fit the file as the fixes
// before left it is left out; the error names those, the others are made.
func ApplyFixes(t *Tree, fixes []Fix) (*Tree, []string, error) {
	var done, failed []string
	// the fixes name the lines of order as the file had them: written and
	// read again, a line may move (the head of the file, a blank line);
	// each line keeps its place in order
	origin := orderLines(t)
	for _, f := range fixes {
		next, made, err := applyFix(t, f)
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		done = append(done, made)
		now := orderLines(next)
		next.moved = map[int]int{}
		for i, line := range origin {
			if i < len(now) {
				next.moved[line] = now[i]
			}
		}
		t = next
	}
	if len(failed) > 0 {
		return t, done, errors.New(strings.Join(failed, "\n"))
	}
	return t, done, nil
}

func applyFix(t *Tree, f Fix) (*Tree, string, error) {
	made := fmt.Sprintf("line %d: %s", f.Line, f.Change)
	if t.doc == nil || f.apply == nil || !f.apply(t) {
		return t, "", fmt.Errorf("%s: could not change tree.yaml there; change it by hand", made)
	}
	b, err := t.doc.Bytes()
	if err != nil {
		return t, "", err
	}
	next, err := Parse(b)
	if next == nil {
		return t, "", err
	}
	// a file with problems left is read all the same: Check names them
	return next, made, nil
}

// digits are the numbers of a change: its lines, the number of a new name.
var digits = regexp.MustCompile(`\d+`)

// lineRe reads the line number at the start of a problem.
var lineRe = regexp.MustCompile(`^line (\d+):`)

// sortProblems orders problems by their line in the file, the others after.
func sortProblems(list []string) {
	num := func(p string) int {
		if m := lineRe.FindStringSubmatch(p); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
		return 1 << 30
	}
	slices.SortStableFunc(list, func(a, b string) int {
		if c := num(a) - num(b); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
}

// orderItem is the line of order with that line number, and its index in
// the section.
func orderItem(t *Tree, line int) (*yaml.Node, int) {
	if t.doc == nil {
		return nil, -1
	}
	if m, ok := t.moved[line]; ok {
		line = m
	}
	section := valueOf(t.doc.Root, "order")
	if section == nil || section.Kind != yaml.SequenceNode {
		return nil, -1
	}
	for i, item := range section.Content {
		if item.Line == line {
			return section, i
		}
	}
	return nil, -1
}

// removeOrderLine deletes the line of order at line.
func removeOrderLine(line int) func(*Tree) bool {
	return func(t *Tree) bool {
		section, i := orderItem(t, line)
		if section == nil {
			return false
		}
		section.Content = slices.Delete(section.Content, i, i+1)
		return true
	}
}

// moveOrderLine puts the line of order at from right above the one at to.
func moveOrderLine(from, to int) func(*Tree) bool {
	return func(t *Tree) bool {
		section, i := orderItem(t, from)
		_, j := orderItem(t, to)
		if section == nil || j < 0 {
			return false
		}
		item := section.Content[i]
		section.Content = slices.Delete(section.Content, i, i+1)
		if i < j {
			j--
		}
		section.Content = slices.Insert(section.Content, j, item)
		return true
	}
}

// appendOrderLine adds "- name" to order after its last create (before
// the lines that place steps after them).
func appendOrderLine(name string) func(*Tree) bool {
	return func(t *Tree) bool {
		if t.doc == nil {
			return false
		}
		section := valueOf(t.doc.Root, "order")
		if section == nil || section.Kind != yaml.SequenceNode {
			return false
		}
		at := 0
		for i, item := range section.Content {
			key := item.Value
			if item.Kind == yaml.MappingNode && len(item.Content) > 0 {
				key = item.Content[0].Value
			}
			if key == name {
				return false
			}
			if !strings.Contains(key, ".") {
				at = i + 1
			}
		}
		section.Content = slices.Insert(section.Content, at, scalar(name))
		return true
	}
}

// insertOrderStep adds the line "- step" right below the line of order at
// line (ships.update below - pilots).
func insertOrderStep(line int, step string) func(*Tree) bool {
	return func(t *Tree) bool {
		section, i := orderItem(t, line)
		if section == nil || slices.ContainsFunc(section.Content, func(x *yaml.Node) bool { return x.Value == step }) {
			return false
		}
		section.Content = slices.Insert(section.Content, i+1, scalar(step))
		return true
	}
}

// setWriteLeave adds a field to the leave of a write of a node; a write in
// the short form becomes a mapping.
func setWriteLeave(name, kind, field string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.Write[kind] == nil || n.Write[kind].value == nil {
			return false
		}
		val := n.Write[kind].value
		if val.Kind == yaml.ScalarNode {
			call, params, err := shortForm(val.Value)
			if err != nil {
				return false
			}
			c := scalar(call)
			c.LineComment = val.LineComment
			content := []*yaml.Node{scalar("call"), c}
			if len(params) > 0 {
				content = append(content, scalar("path"), valueYAML(params, true))
			}
			val.Kind, val.Tag, val.Value, val.Style, val.LineComment = yaml.MappingNode, "", "", 0, ""
			val.Content = content
		}
		if val.Kind != yaml.MappingNode {
			return false
		}
		val.Style = 0
		leave := valueOf(val, "leave")
		if leave == nil {
			leave = &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
			val.Content = append(val.Content, scalar("leave"), leave)
		}
		if slices.ContainsFunc(leave.Content, func(x *yaml.Node) bool { return x.Value == field }) {
			return false
		}
		leave.Content = append(leave.Content, scalar(field))
		return true
	}
}

// setOrderSource sets the source of a body field in the line of order at
// line; an empty value removes it (and the braces with the last one).
func setOrderSource(line int, field, value string) func(*Tree) bool {
	return func(t *Tree) bool {
		section, i := orderItem(t, line)
		if section == nil {
			return false
		}
		item := section.Content[i]
		if item.Kind != yaml.MappingNode || len(item.Content) != 2 || item.Content[1].Kind != yaml.MappingNode {
			return false
		}
		vals := item.Content[1]
		for k := 0; k+1 < len(vals.Content); k += 2 {
			if vals.Content[k].Value != field {
				continue
			}
			if value != "" {
				vals.Content[k+1].Value = value
				return true
			}
			vals.Content = slices.Delete(vals.Content, k, k+2)
			if len(vals.Content) == 0 {
				name := item.Content[0]
				name.LineComment = item.LineComment
				section.Content[i] = name
			}
			return true
		}
		return false
	}
}

// replaceRef replaces a reference in the values of a YAML subtree: a value
// that is it, a name=value of a short form, a part {{…}} of a text. The
// child nodes (each) are left alone: their references read other records.
func replaceRef(root *yaml.Node, from, to string) bool {
	if root == nil {
		return false
	}
	changed := false
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		switch n.Kind {
		case yaml.ScalarNode:
			if n.Value == from {
				n.Value, changed = to, true
				return
			}
			v := n.Value
			if strings.Contains(v, "{{") {
				v = strings.ReplaceAll(v, "{{"+from+"}}", "{{"+to+"}}")
			}
			fields, hit := strings.Fields(v), false
			for i, f := range fields {
				if k, val, ok := strings.Cut(f, "="); ok && val == from {
					fields[i], hit = k+"="+to, true
				}
			}
			if hit {
				v = strings.Join(fields, " ")
			}
			if v != n.Value {
				n.Value, changed = v, true
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == "each" {
					continue
				}
				walk(n.Content[i+1])
			}
		default:
			for _, c := range n.Content {
				walk(c)
			}
		}
	}
	walk(root)
	return changed
}

// addWhere adds "field: present" to the where of a node, with the reason
// as comment.
func addWhere(name, field, why string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.value == nil || n.value.Kind != yaml.MappingNode {
			return false
		}
		cond := &yaml.Node{Kind: yaml.ScalarNode, Value: "present", LineComment: comment(why)}
		where := valueOf(n.value, "where")
		if where == nil {
			m := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar(field), cond}}
			// before key, write and each, where the proposal puts it
			at := len(n.value.Content)
			for i := 0; i+1 < len(n.value.Content); i += 2 {
				if k := n.value.Content[i].Value; k == "require" || k == "key" || k == "save" || k == "write" || k == "each" {
					at = i
					break
				}
			}
			n.value.Content = slices.Insert(n.value.Content, at, scalar("where"), m)
			return true
		}
		if where.Kind != yaml.MappingNode || valueOf(where, field) != nil {
			return false
		}
		where.Style = 0
		where.Content = append(where.Content, scalar(field), cond)
		return true
	}
}

// orderLines are the lines of the entries of order, in order.
func orderLines(t *Tree) []int {
	if t.doc == nil {
		return nil
	}
	section := valueOf(t.doc.Root, "order")
	if section == nil || section.Kind != yaml.SequenceNode {
		return nil
	}
	var out []int
	for _, item := range section.Content {
		out = append(out, item.Line)
	}
	return out
}

// setNodeField sets a field of the fields of a node (fields: {size:
// .size}) with the reason as comment; the block is added before write and
// each, an entry there is replaced. A nil value is written as null: the
// body leaves the field out.
func setNodeField(name, field string, value any, why string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.value == nil {
			return false
		}
		v := nodeMapping(n)
		if v == nil || v.Kind != yaml.MappingNode {
			return false
		}
		val := valueYAML(value, true)
		if value == nil {
			val = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
		}
		val.LineComment = comment(why)
		fields := valueOf(v, "fields")
		if fields == nil {
			fields = &yaml.Node{Kind: yaml.MappingNode}
			at := len(v.Content)
			for i := 0; i+1 < len(v.Content); i += 2 {
				if k := v.Content[i].Value; k == "write" || k == "each" || k == "off" {
					at = i
					break
				}
			}
			v.Content = slices.Insert(v.Content, at, scalar("fields"), fields)
		}
		if fields.Kind != yaml.MappingNode {
			return false
		}
		fields.Style = 0
		for i := 0; i+1 < len(fields.Content); i += 2 {
			if strings.EqualFold(fields.Content[i].Value, field) {
				fields.Content[i+1] = val
				return true
			}
		}
		fields.Content = append(fields.Content, scalar(field), val)
		return true
	}
}

// removeNode removes a node and what hangs below it from the file.
func removeNode(name string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.owner == nil {
			return false
		}
		for i := 0; i+1 < len(n.owner.Content); i += 2 {
			if n.owner.Content[i+1] == n.value {
				n.owner.Content = slices.Delete(n.owner.Content, i, i+2)
				return true
			}
		}
		return false
	}
}

// addedByFix reports whether -fix added the node: its comment says so (on
// the call, or after the short form).
func addedByFix(n *Node) bool {
	const mark = "added by -fix"
	if strings.Contains(n.Comment, mark) {
		return true
	}
	for _, y := range []*yaml.Node{n.value, n.key} {
		if y != nil && strings.Contains(y.LineComment, mark) {
			return true
		}
	}
	if v := valueOf(n.value, "call"); v != nil && strings.Contains(v.LineComment, mark) {
		return true
	}
	return false
}
