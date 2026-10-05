package apitest

import (
	"context"

	tokenpkg "github.com/fada4773-sketch/specproof/internal/token"
)

// TokenSource provides the token that apitest sends with each request. It is
// asked before every request (FR-AUTH-02); caching and refreshing are up to
// the implementation.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken returns a TokenSource that always returns token.
func StaticToken(token string) TokenSource {
	return staticToken(token)
}

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// TokenFunc adapts a function to a TokenSource, e.g. to log in against the
// API or to refresh an expiring token.
func TokenFunc(fn func(ctx context.Context) (string, error)) TokenSource {
	return tokenFunc(fn)
}

type tokenFunc func(ctx context.Context) (string, error)

func (f tokenFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// TruncateToken is the default of Config.TamperToken: the first 100
// characters of the real token, or its first half if it is shorter. Neither
// a verifier nor an API that only decodes the token can accept it.
func TruncateToken(token string) string { return tokenpkg.Truncate(token) }

// TamperSignature keeps the token's form: a JWT keeps its header and claims,
// gets the claim "apitest": "invalid-token" and an inverted signature; other
// tokens get every second character changed. Use it as Config.TamperToken
// to test that the API (or its gateway) verifies signatures.
func TamperSignature(token string) string { return tokenpkg.Tamper(token) }
