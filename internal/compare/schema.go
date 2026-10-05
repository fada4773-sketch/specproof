package compare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Response checks headers and body of a response against the documented
// response (stage 2, FR-CMP-01). For JSON bodies it also returns the decoded
// body, so stage 3 can use it.
func Response(v *spec.Validator, r *openapi3.Response, header http.Header, body []byte) (errs []spec.SchemaError, decoded any, isJSON bool) {
	if r == nil {
		return nil, nil, false
	}
	errs = append(errs, headers(v, r, header)...)

	trimmed := bytes.TrimSpace(body)
	if len(r.Content) == 0 {
		if len(trimmed) > 0 {
			errs = append(errs, spec.SchemaError{Reason: "response has a body, but the spec documents none"})
		}
		return errs, nil, false
	}
	ct := header.Get("Content-Type")
	mt, media := matchMedia(r.Content, ct)
	if media == nil {
		if len(trimmed) == 0 && ct == "" {
			errs = append(errs, spec.SchemaError{Reason: "response has no body, but the spec documents " + strings.Join(sortedMediaTypes(r.Content), ", ")})
		} else {
			errs = append(errs, spec.SchemaError{Reason: fmt.Sprintf("Content-Type %q is not documented (expected: %s)", ct, strings.Join(sortedMediaTypes(r.Content), ", "))})
		}
		return errs, nil, false
	}
	if !spec.IsJSON(mt) && !spec.IsJSON(ct) {
		return errs, nil, false
	}
	if len(trimmed) == 0 {
		errs = append(errs, spec.SchemaError{Reason: "empty body, expected JSON"})
		return errs, nil, true
	}
	if err := spec.DecodeJSON(trimmed, &decoded); err != nil {
		errs = append(errs, spec.SchemaError{Reason: "body is not valid JSON: " + err.Error()})
		return errs, nil, true
	}
	if media.Schema != nil && media.Schema.Value != nil {
		errs = append(errs, v.Validate(media.Schema.Value, decoded, spec.ModeResponse)...)
	}
	return errs, decoded, true
}

// Schema returns the schema of the documented media type matching ct.
func Schema(r *openapi3.Response, ct string) *openapi3.Schema {
	if r == nil {
		return nil
	}
	if _, media := matchMedia(r.Content, ct); media != nil && media.Schema != nil {
		return media.Schema.Value
	}
	return nil
}

// matchMedia finds the documented media type for a Content-Type header,
// supporting wildcards like "application/*" and "*/*".
func matchMedia(content openapi3.Content, ct string) (string, *openapi3.MediaType) {
	actual, _, err := mime.ParseMediaType(ct)
	if err != nil {
		actual = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	}
	if actual == "" {
		return "", nil
	}
	best, bestScore := "", -1
	for _, key := range sortedMediaTypes(content) {
		doc, _, err := mime.ParseMediaType(key)
		if err != nil {
			doc = strings.ToLower(key)
		}
		score := -1
		switch {
		case doc == actual:
			score = 3
		case strings.HasSuffix(doc, "/*") && strings.HasPrefix(actual, strings.TrimSuffix(doc, "*")):
			score = 2
		case doc == "*/*":
			score = 1
		}
		if score > bestScore {
			best, bestScore = key, score
		}
	}
	if bestScore < 0 {
		return "", nil
	}
	return best, content[best]
}

func sortedMediaTypes(content openapi3.Content) []string {
	keys := make([]string, 0, len(content))
	for k := range content {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// headers checks documented response headers.
func headers(v *spec.Validator, r *openapi3.Response, h http.Header) []spec.SchemaError {
	var errs []spec.SchemaError
	names := make([]string, 0, len(r.Headers))
	for n := range r.Headers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := r.Headers[name]
		if ref == nil || ref.Value == nil || strings.EqualFold(name, "Content-Type") {
			continue
		}
		hd := ref.Value
		values, present := h[http.CanonicalHeaderKey(name)]
		ptr := "header:" + name
		if !present {
			if hd.Required {
				errs = append(errs, spec.SchemaError{Pointer: ptr, Reason: "required header missing"})
			}
			continue
		}
		if hd.Schema == nil || hd.Schema.Value == nil || len(values) == 0 {
			continue
		}
		for _, e := range v.Validate(hd.Schema.Value, headerValue(hd.Schema.Value, values[0]), spec.ModePlain) {
			e.Pointer = ptr + e.Pointer
			errs = append(errs, e)
		}
	}
	return errs
}

// headerValue converts a header string to the type its schema expects.
func headerValue(s *openapi3.Schema, raw string) any {
	switch {
	case s.Type.Is("integer"), s.Type.Is("number"):
		if _, err := strconv.ParseFloat(raw, 64); err == nil {
			return json.Number(raw)
		}
	case s.Type.Is("boolean"):
		if b, err := strconv.ParseBool(raw); err == nil {
			return b
		}
	}
	return raw
}
