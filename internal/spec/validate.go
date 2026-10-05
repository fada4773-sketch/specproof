package spec

import (
	"errors"
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Mode selects how readOnly and writeOnly are treated during validation.
type Mode int

// Validation modes.
const (
	ModePlain    Mode = iota // no readOnly/writeOnly rules
	ModeRequest              // readOnly properties must not be sent
	ModeResponse             // writeOnly properties must not be returned
)

// SchemaError is one validation problem at a JSON pointer.
type SchemaError struct {
	Pointer string // e.g. "/price"; "" is the root
	Reason  string
}

func (e SchemaError) String() string {
	p := e.Pointer
	if p == "" {
		p = "/"
	}
	return p + ": " + e.Reason
}

// Validator validates values against schemas. It holds only per-validation
// settings; nothing is registered globally (FR-GO-04).
type Validator struct {
	opts []openapi3.SchemaValidationOption
}

// NewValidator returns a validator with format checks for the formats that
// FR-CMP-01 requires. Formats already known to kin-openapi (date, date-time,
// byte, int32, int64) are validated as well.
func NewValidator() *Validator {
	// kin-openapi evaluates 3.1 keywords (type arrays, const) without extra settings.
	return &Validator{opts: []openapi3.SchemaValidationOption{
		openapi3.MultiErrors(),
		openapi3.EnableFormatValidation(),
		openapi3.SetSchemaRegexCompiler(compileRegex),
		openapi3.WithStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForUUIDOfRFC9562)),
		openapi3.WithStringFormatValidator("email", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForEmail)),
	}}
}

// Validate checks value against schema. value must be in the canonical form
// produced by Normalize or DecodeJSON.
func (v *Validator) Validate(schema *openapi3.Schema, value any, mode Mode) []SchemaError {
	opts := append([]openapi3.SchemaValidationOption(nil), v.opts...)
	switch mode {
	case ModeRequest:
		opts = append(opts, openapi3.VisitAsRequest())
	case ModeResponse:
		opts = append(opts, openapi3.VisitAsResponse())
	default:
		opts = append(opts, openapi3.DisableReadOnlyValidation(), openapi3.DisableWriteOnlyValidation())
	}
	err := schema.VisitJSON(value, opts...)
	if err == nil {
		return nil
	}
	var out []SchemaError
	collect(err, &out)
	return out
}

func collect(err error, out *[]SchemaError) {
	var multi openapi3.MultiError
	if errors.As(err, &multi) {
		for _, e := range multi {
			collect(e, out)
		}
		return
	}
	var se *openapi3.SchemaError
	if errors.As(err, &se) {
		// Errors from oneOf/anyOf carry the underlying errors as Origin.
		if se.Origin != nil {
			var inner openapi3.MultiError
			if errors.As(se.Origin, &inner) && len(inner) > 0 {
				collect(se.Origin, out)
				return
			}
		}
		*out = append(*out, SchemaError{Pointer: pointer(se.JSONPointer()), Reason: se.Reason})
		return
	}
	*out = append(*out, SchemaError{Reason: fmt.Sprint(err)})
}

func pointer(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteByte('/')
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(p, "~", "~0"), "/", "~1"))
	}
	return b.String()
}
