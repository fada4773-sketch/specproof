package tree

import (
	"fmt"
	"net/http"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// changingVerbs are first words of an operationId that say a request
// changes what it names, whatever its method: GET /close/gardens/{id}
// (closeGarden) closes the garden.
var changingVerbs = []string{"cancel", "delete", "remove", "reset", "trigger", "run", "start", "stop", "restart", "execute",
	"approve", "reject", "archive", "close", "lock", "unlock", "enable", "disable", "activate", "deactivate",
	"publish", "unpublish", "retry", "abort", "purge", "revoke"}

// changingGET reports whether a GET changes what it names (its operationId
// starts with a verb of changingVerbs): the fetch never sends it, it is an
// action of the record it names.
func changingGET(op *spec.Operation) bool {
	return op != nil && op.Method == http.MethodGet && op.HasOperationID && slices.Contains(changingVerbs, parseOpName(op.ID).verb)
}

// checkChangingReads finds a node whose GET changes the record it names
// (GET /close/gardens/{id}): a fetch would close the gardens of the stage,
// and the test reads nothing with it. The fix makes it an action of the
// node above (close: GET /close/gardens/{id} id=.id), which
// the test runs on a record it created.
func checkChangingReads(s *spec.Spec, t *Tree) ([]string, []Fix) {
	var out []string
	var fixes []Fix
	t.Walk(func(n *Node) {
		if n.IsRequire || n.Gone || n.Off {
			return
		}
		op := lookupOp(s, n.Call)
		if !changingGET(op) {
			return
		}
		what := fmt.Sprintf("line %d: %s: %s (%s) changes the record it names: a fetch would %s records of the stage, the test reads nothing with it",
			n.Line, n.Name, n.Call, op.ID, parseOpName(op.ID).verb)
		kind := parseOpName(op.ID).verb
		if n.Parent == nil || len(n.Each) > 0 || n.Parent.Write[kind] != nil {
			why := op.ID + " changes what it names; an action, no read"
			f := Fix{Line: n.Line, Change: n.Name + ": add off: true # " + why, apply: setNodeOff(n.Name, why)}
			out = append(out, what+f.change())
			fixes = append(fixes, f)
			return
		}
		short, ok := shortText(n.Call, n.Path)
		if !ok || len(n.Query) > 0 {
			short = n.Call + " " + valuesText(n.Path, n.Query)
		}
		f := Fix{Line: n.Line, Change: fmt.Sprintf("remove %s, and add to the write block of %s: %s: %s", n.Name, n.Parent.Name, kind, short),
			apply: func(t *Tree) bool {
				return addAction(n.Parent.Name, kind, short, op.ID)(t) && removeNode(n.Name)(t)
			}}
		out = append(out, what+f.change())
		fixes = append(fixes, f)
	})
	return out, fixes
}

// addAction adds a write in the short form to the write block of a node
// (the block is added when the node has none); false when the node has a
// write of that name.
func addAction(name, kind, short, opID string) func(*Tree) bool {
	return func(t *Tree) bool {
		n := t.Node(name)
		if n == nil || n.value == nil || n.Write[kind] != nil {
			return false
		}
		v := nodeMapping(n)
		if v == nil || v.Kind != yaml.MappingNode {
			return false
		}
		val := scalar(short)
		val.LineComment = comment(opID)
		write := valueOf(v, "write")
		if write == nil {
			write = &yaml.Node{Kind: yaml.MappingNode}
			at := len(v.Content)
			for i := 0; i+1 < len(v.Content); i += 2 {
				if v.Content[i].Value == "each" {
					at = i
				}
			}
			v.Content = slices.Insert(v.Content, at, scalar("write"), write)
		}
		if write.Kind != yaml.MappingNode {
			return false
		}
		write.Style = 0
		write.Content = append(write.Content, scalar(kind), val)
		return true
	}
}
