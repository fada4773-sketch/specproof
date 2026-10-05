package scenario

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// place is a media type object of the document that holds an example.
type place struct {
	node    *yaml.Node // the media type mapping, which gets "example"
	shared  bool       // reached through a $ref, so other operations use it too
	schema  *openapi3.SchemaRef
	code    string // response code, "" for a request body
	success bool
	named   bool // has named examples, which are curated and never changed
	where   string
}

// example returns the current example, or nil.
func (p *place) example() any {
	if ex := yamldoc.Get(p.node, "example"); ex != nil {
		if v, err := yamldoc.Decode(ex); err == nil {
			return v
		}
	}
	return nil
}

// operation returns the mapping of an operation in the document.
func operation(doc *yamldoc.Doc, op *spec.Operation) *yaml.Node {
	return yamldoc.Get(yamldoc.Path(doc.Root, "paths", op.Path), strings.ToLower(op.Method))
}

// jsonMedia returns the first JSON media type of content, in sorted order.
func jsonMedia(content openapi3.Content) (string, *openapi3.MediaType) {
	for _, mt := range sortedKeys(content) {
		if m := content[mt]; spec.IsJSON(mt) && m != nil && m.Schema != nil && m.Schema.Value != nil {
			return mt, m
		}
	}
	return "", nil
}

// mediaPlace builds the place of a media type below holder (a request body
// or a response, possibly a $ref).
func mediaPlace(doc *yamldoc.Doc, holder *yaml.Node, content openapi3.Content, where string) *place {
	if holder == nil {
		return nil
	}
	target, err := doc.Resolve(holder)
	if err != nil {
		return nil // external: apply has reported it
	}
	mt, media := jsonMedia(content)
	if media == nil {
		return nil
	}
	node := yamldoc.Get(yamldoc.Get(target, "content"), mt)
	if node == nil {
		return nil
	}
	exs := yamldoc.Get(node, "examples")
	return &place{
		node:   node,
		shared: yamldoc.Ref(holder) != "",
		schema: media.Schema,
		named:  exs != nil && len(exs.Content) > 0,
		where:  fmt.Sprintf("%s.content[%s]", where, mt),
	}
}

// requestPlace returns the place of the request body of op, or nil.
func requestPlace(doc *yamldoc.Doc, op *spec.Operation) *place {
	rb := op.Op.RequestBody
	if rb == nil || rb.Value == nil {
		return nil
	}
	return mediaPlace(doc, yamldoc.Get(operation(doc, op), "requestBody"), rb.Value.Content, op.Where+".requestBody")
}

// responsePlaces returns the places of the responses of op, by code.
func responsePlaces(doc *yamldoc.Doc, op *spec.Operation) []*place {
	if op.Op.Responses == nil {
		return nil
	}
	respY := yamldoc.Get(operation(doc, op), "responses")
	var out []*place
	for _, code := range sortedKeys(op.Op.Responses.Map()) {
		ref := op.Op.Responses.Map()[code]
		if ref == nil || ref.Value == nil {
			continue
		}
		p := mediaPlace(doc, yamldoc.Get(respY, code), ref.Value.Content, op.Where+".responses."+code)
		if p == nil {
			continue
		}
		p.code = code
		p.success = len(code) == 3 && code[0] == '2'
		out = append(out, p)
	}
	return out
}

// paramEntry is a path parameter in a parameters list of the document.
type paramEntry struct {
	list   *yaml.Node // the sequence that holds it
	index  int
	target *yaml.Node // the parameter object, $ref resolved
	where  string
}

// pathParam finds the entry of a path parameter of op: in the parameters of
// the operation, else in those of the path.
func pathParam(doc *yamldoc.Doc, op *spec.Operation, name string) *paramEntry {
	itemY := yamldoc.Path(doc.Root, "paths", op.Path)
	for _, holder := range []*yaml.Node{operation(doc, op), itemY} {
		list := yamldoc.Get(holder, "parameters")
		if list == nil || list.Kind != yaml.SequenceNode {
			continue
		}
		for i, entry := range list.Content {
			target, err := doc.Resolve(entry)
			if err != nil {
				continue
			}
			n, in := yamldoc.Get(target, "name"), yamldoc.Get(target, "in")
			if n != nil && in != nil && n.Value == name && in.Value == "path" {
				return &paramEntry{list: list, index: i, target: target,
					where: fmt.Sprintf("%s.parameters[%s]", op.Where, name)}
			}
		}
	}
	return nil
}

// inline replaces a $ref entry by a copy of the parameter object, so the
// path gets its own example.
func (e *paramEntry) inline() bool {
	if yamldoc.Ref(e.list.Content[e.index]) == "" {
		return false
	}
	c := clone(e.target)
	e.list.Content[e.index] = c
	e.target = c
	return true
}

// clone copies a node deeply.
func clone(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = clone(child)
	}
	return &c
}
