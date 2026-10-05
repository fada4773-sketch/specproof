package apply

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// extensions sets "<operationId>.x-<name>" entries on the operation.
// apitest ignores extensions of the wrong type silently, so the types of
// its own extensions are checked here.
func (a *applier) extensions(op *spec.Operation, opY *yaml.Node, where string) {
	if !op.HasOperationID {
		return
	}
	for _, ext := range a.defs.Extensions(op.ID) {
		if msg := checkExtension(ext.Name, ext.Entry.Value); msg != "" {
			a.fatal(fmt.Sprintf("EXT_INVALID %q: %s", ext.Entry.Key, msg))
			continue
		}
		a.defs.Use(ext.Entry)
		if cur := yamldoc.Get(opY, ext.Name); cur != nil {
			if v, err := yamldoc.Decode(cur); err == nil && reflect.DeepEqual(spec.Normalize(v), spec.Normalize(ext.Entry.Value)) {
				continue
			}
		}
		if err := yamldoc.Set(opY, ext.Name, ext.Entry.Value); err != nil {
			a.note(CodeExtension, where, err.Error())
			continue
		}
		a.res.Changed = true
		a.res.Stats.Extensions++
		a.note(CodeExtension, where, fmt.Sprintf("%s: %s", ext.Name, compact(ext.Entry.Value)))
	}
}

// checkExtension returns why a value is wrong for an apitest extension.
func checkExtension(name string, v any) string {
	isBool := func() bool { _, ok := v.(bool); return ok }
	switch name {
	case "x-apitest-verify":
		if isBool() {
			return ""
		}
		if m, ok := v.(map[string]any); ok {
			if p, has := m["poll"]; has {
				if _, ok := p.(bool); !ok {
					return `"poll" must be true or false`
				}
			}
			if t, has := m["timeout"]; has {
				if _, ok := t.(string); !ok {
					return `"timeout" must be a duration like "30s"`
				}
			}
			return ""
		}
		return `must be true, false or an object like {"poll": true, "timeout": "30s"}`
	case "x-apitest-skip":
		if _, ok := v.(string); ok || isBool() {
			return ""
		}
		return "must be a reason (string)"
	case "x-apitest-forbidden":
		if !isBool() {
			return "must be true or false"
		}
	case "x-apitest-order":
		if n, ok := v.(json.Number); !ok || strings.ContainsAny(n.String(), ".eE") {
			return "must be a whole number"
		}
	case "x-apitest-compare":
		if s, _ := v.(string); !slices.Contains([]string{"schema", "subset", "exact"}, s) {
			return `must be "schema", "subset" or "exact"`
		}
	case "x-apitest-ignore":
		list, ok := v.([]any)
		if !ok {
			return "must be a list of field names or JSON pointers"
		}
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return "must be a list of strings"
			}
		}
	case "x-apitest-bind":
		return `belongs to a parameter; use "<operationId>.<parameter>": {"bind": …}`
	case "x-apitest-compare-unordered":
		return "belongs to a response, not to the operation"
	}
	return ""
}

// bindParam writes a binding from defaults.json: x-apitest-bind on a
// parameter defined in the operation, otherwise a link on the producer's
// response, since x-apitest-bind on a shared parameter would apply to every
// operation that uses it.
func (a *applier) bindParam(op *spec.Operation, p *openapi3.Parameter, paramY *yaml.Node, shared bool, where string) {
	if !op.HasOperationID || p.In != openapi3.ParameterInPath {
		return
	}
	e := a.defs.Binding(op.ID, p.Name)
	if e == nil {
		return
	}
	b := e.Bind
	producer := a.s.Op(b.From)
	if producer == nil {
		a.fatal(fmt.Sprintf("BIND_INVALID %q: operation %q does not exist", e.Key, b.From))
		return
	}
	a.defs.Use(e)
	if !shared {
		bind := map[string]any{"from": producer.ID}
		if b.Header != "" {
			bind["header"] = b.Header
		} else {
			bind["pointer"] = b.Pointer
		}
		if b.Request {
			bind["source"] = "request"
		}
		a.setIfChanged(paramY, "x-apitest-bind", bind, where, CodeBind)
		return
	}
	expr := "$response.body#" + b.Pointer
	switch {
	case b.Header != "":
		expr = "$response.header." + b.Header
	case b.Request:
		expr = "$request.body#" + b.Pointer
	}
	respY, code := a.successResponse(producer)
	if respY == nil {
		a.note(CodeBindSkipped, where, fmt.Sprintf("%s has no 2xx response to put a link on", producer.ID))
		return
	}
	if yamldoc.Ref(respY) != "" {
		a.note(CodeBindSkipped, where, fmt.Sprintf("the %s response of %s is a shared component; no link written, define the parameter in the operation instead", code, producer.ID))
		return
	}
	linksY := yamldoc.Get(respY, "links")
	if linksY == nil {
		if err := yamldoc.Set(respY, "links", map[string]any{}); err != nil {
			a.note(CodeBindSkipped, where, err.Error())
			return
		}
		linksY = yamldoc.Get(respY, "links")
		linksY.Style = 0 // block style: one link per line, not one long flow mapping
	}
	link := map[string]any{"operationId": op.ID, "parameters": map[string]any{p.Name: expr}}
	a.setIfChanged(linksY, op.ID+"_"+p.Name, link, where, CodeBind)
}

func (a *applier) setIfChanged(n *yaml.Node, key string, v any, where, code string) {
	if cur := yamldoc.Get(n, key); cur != nil {
		if old, err := yamldoc.Decode(cur); err == nil && reflect.DeepEqual(spec.Normalize(old), spec.Normalize(v)) {
			return
		}
	}
	if err := yamldoc.Set(n, key, v); err != nil {
		a.note(CodeBindSkipped, where, err.Error())
		return
	}
	a.res.Changed = true
	a.res.Stats.Bindings++
	a.note(code, where, fmt.Sprintf("%s: %s", key, compact(v)))
}

// successResponse returns the YAML node of the lowest 2xx response.
func (a *applier) successResponse(op *spec.Operation) (*yaml.Node, string) {
	opY := yamldoc.Path(a.doc.Root, "paths", op.Path, strings.ToLower(op.Method), "responses")
	if opY == nil {
		return nil, ""
	}
	var codes []string
	for i := 0; i+1 < len(opY.Content); i += 2 {
		if c := opY.Content[i].Value; len(c) == 3 && c[0] == '2' {
			codes = append(codes, c)
		}
	}
	slices.Sort(codes)
	if len(codes) == 0 {
		return nil, ""
	}
	return yamldoc.Get(opY, codes[0]), codes[0]
}
