package tree

import (
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
	same := map[string]int{}
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
		if same[digits.ReplaceAllString(made, "#")]++; same[digits.ReplaceAllString(made, "#")] > 3 {
			// the same change again and again under new names (a node
			// added that does not solve it): stop, Check names it
			return t, done, nil
		}
		done = append(done, made)
		t = next
	}
	return t, done, nil
}

// ApplyFixes makes the fixes a build found (they change tree.yaml; the
// records are fetched again after them) and returns the tree read again
// and the changes made.
func ApplyFixes(t *Tree, fixes []Fix) (*Tree, []string, error) {
	var done []string
	for _, f := range fixes {
		next, made, err := applyFix(t, f)
		if err != nil {
			return t, done, err
		}
		done = append(done, made)
		t = next
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
