package spec

import (
	"regexp"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/getkin/kin-openapi/openapi3"
)

// regexTimeout bounds a single match with the ECMAScript engine, which
// backtracks and could otherwise hang on a pathological pattern.
const regexTimeout = time.Second

// compileRegex compiles a "pattern" of the spec. OpenAPI uses ECMA-262
// regular expressions; Go's regexp (RE2) covers most of them and is used
// first. Patterns RE2 cannot compile, e.g. with lookahead "(?=…)" or
// backreferences, fall back to an ECMAScript-compatible engine.
func compileRegex(expr string) (openapi3.RegexMatcher, error) {
	if re, err := regexp.Compile(expr); err == nil {
		return re, nil
	}
	re, err := regexp2.Compile(expr, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = regexTimeout
	return ecmaRegex{re}, nil
}

type ecmaRegex struct{ re *regexp2.Regexp }

// MatchString reports a match; a timeout counts as no match.
func (r ecmaRegex) MatchString(s string) bool {
	ok, err := r.re.MatchString(s)
	return err == nil && ok
}

// CompilePattern compiles a schema pattern the way apitest validates it,
// for tools that must produce values apitest accepts.
func CompilePattern(expr string) (openapi3.RegexMatcher, error) { return compileRegex(expr) }
