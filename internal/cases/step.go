package cases

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Step builds the case of one step of a scenario: the operation, the
// status the step expects and the token it sends decide its kind; the
// response the spec documents for that status (exactly, as "4XX" or as
// "default") is its expectation. Body and parameters are set by the
// scenario runner when the step runs. The name is "<tag>/<id>".
func Step(op *spec.Operation, id string, status int, token string) *Case {
	c := &Case{
		Name:    NameElem(group(op)) + "/" + NameElem(id),
		Group:   group(op),
		Example: id,
		Op:      op,
		Kind:    stepKind(status, token),
		Compare: extString(op.Op.Extensions, "x-apitest-compare"),
		Ignore:  extStrings(op.Op.Extensions, "x-apitest-ignore"),
	}
	code, r := ResponseFor(op, status)
	c.Expect = Expectation{Code: strconv.Itoa(status), Status: status, Response: r}
	if code == "" {
		c.Expect.Code = strconv.Itoa(status)
	}
	if r != nil {
		c.addResponseExtensions(r, nil)
	}
	if rb := op.Op.RequestBody; rb != nil && rb.Value != nil {
		c.MediaType, _ = pickMedia(rb.Value.Content)
	}
	return c
}

func stepKind(status int, token string) Kind {
	switch token {
	case "none":
		return Unauthorized
	case "invalid":
		return InvalidToken
	case "forbidden":
		return Forbidden
	}
	switch {
	case status >= 500:
		return ServerError
	case status == http.StatusNotFound:
		return NotFound
	case status >= 400:
		return Negative
	}
	return Positive
}

// ResponseFor returns the response the operation documents for a status:
// the exact code, else the class ("4XX"), else "default"; code is the key
// it was found under, "" if none.
func ResponseFor(op *spec.Operation, status int) (code string, r *openapi3.Response) {
	rs := op.Op.Responses
	if rs == nil {
		return "", nil
	}
	exact := strconv.Itoa(status)
	for _, k := range []string{exact, exact[:1] + "XX", exact[:1] + "xx"} {
		if ref := rs.Value(k); ref != nil && ref.Value != nil {
			return k, ref.Value
		}
	}
	if ref := rs.Default(); ref != nil && ref.Value != nil {
		return "default", ref.Value
	}
	return "", nil
}

// RequestMedia returns the media type and its definition the operation
// takes as request body, nil without body.
func RequestMedia(op *spec.Operation) (string, *openapi3.MediaType) {
	rb := op.Op.RequestBody
	if rb == nil || rb.Value == nil {
		return "", nil
	}
	return pickMedia(rb.Value.Content)
}

// IsJSONMedia reports whether a media type is JSON.
func IsJSONMedia(mt string) bool {
	return spec.IsJSON(strings.TrimSpace(strings.Split(mt, ";")[0]))
}

// NotFoundValue is a key no record has, for a path parameter: its
// x-apitest-not-found, else a value derived from its schema; why says why
// there is none.
func NotFoundValue(p *openapi3.Parameter) (v any, why string) { return notFoundValue(p) }
