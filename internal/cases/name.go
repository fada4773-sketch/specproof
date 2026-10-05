package cases

import (
	"strconv"
	"strings"
)

// NameElem turns one element of a case name (tag, operation ID, example
// name) into the form that "go test" uses (FR-CASE-09). "/" is replaced by
// "_" because "go test -run" treats "/" as a level separator.
//
// The rewrite matches testing.rewrite: spaces become "_" and non-printable
// runes are escaped.
func NameElem(s string) string {
	s = strings.Trim(s, "/")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/':
			b.WriteByte('_')
		case isSpace(r):
			b.WriteByte('_')
		case !strconv.IsPrint(r):
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// Name builds the stable case name "<Tag>/<operationId>/<example>".
func Name(tag, opID, example string) string {
	return NameElem(tag) + "/" + NameElem(opID) + "/" + NameElem(example)
}

// isSpace is a copy of the unexported testing.isSpace, so names match what
// "go test" prints and filters on. It is not the same as unicode.IsSpace.
func isSpace(r rune) bool {
	if r < 0x2000 {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xA0, 0x1680:
			return true
		}
	} else {
		if r <= 0x200a {
			return true
		}
		switch r {
		case 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
			return true
		}
	}
	return false
}
