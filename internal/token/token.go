// Package token manipulates and inspects tokens for the authentication
// cases (FR-CASE-10) and the expiry check (FR-AUTH-06). It never verifies
// signatures and needs no key.
package token

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// IsJWT reports whether tok looks like a JWS in compact form
// (header.payload.signature) with a decodable signature.
func IsJWT(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(parts[2])
	return err == nil
}

// TruncateLen is how many characters of the real token Truncate keeps.
const TruncateLen = 100

// Truncate returns the first TruncateLen characters of tok, or the first
// half if tok is not longer. A JWT loses its signature and the end of its
// payload, so neither a verifier nor code that only decodes the token can
// accept it. It never returns tok itself.
func Truncate(tok string) string {
	switch {
	case len(tok) > TruncateLen:
		return tok[:TruncateLen]
	case len(tok) >= 2:
		return tok[:len(tok)/2]
	}
	return "invalid"
}

// Tamper returns a copy of tok that must be rejected by any server that
// validates tokens. It differs from tok as much as possible while keeping
// its form, so a cache or proxy keyed on parts of the token cannot mistake
// it for the real one.
//
// For a JWT the header stays, so the server still picks the real key. The
// payload keeps all claims and gets the claim "apitest": "invalid-token",
// and every byte of the decoded signature is inverted. Other tokens get
// every second character replaced by a different character of the same
// kind.
func Tamper(tok string) string {
	if IsJWT(tok) {
		parts := strings.Split(tok, ".")
		parts[1] = tamperPayload(parts[1])
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		for i := range sig {
			sig[i] ^= 0xff
		}
		parts[2] = base64.RawURLEncoding.EncodeToString(sig)
		return strings.Join(parts, ".")
	}
	if tok == "" {
		return "invalid"
	}
	return replaceEverySecond(tok)
}

// tamperPayload adds a claim to a JSON object payload. Other payloads get
// characters replaced.
func tamperPayload(p string) string {
	raw, err := base64.RawURLEncoding.DecodeString(p)
	var claims map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &claims) != nil || claims == nil {
		return replaceEverySecond(p)
	}
	claims["apitest"] = json.RawMessage(`"invalid-token"`)
	out, err := json.Marshal(claims)
	if err != nil {
		return replaceEverySecond(p)
	}
	return base64.RawURLEncoding.EncodeToString(out)
}

func replaceEverySecond(s string) string {
	b := []byte(s)
	for i := 0; i < len(b); i += 2 {
		b[i] = replacement(b[i])
	}
	return string(b)
}

func replacement(c byte) byte {
	switch {
	case c >= '0' && c <= '8', c >= 'a' && c <= 'y', c >= 'A' && c <= 'Y':
		return c + 1
	case c == '9':
		return '0'
	case c == 'z':
		return 'a'
	case c == 'Z':
		return 'A'
	case c == 'x':
		return 'y'
	default:
		return 'x'
	}
}

// Expiry returns the "exp" claim of a JWT. ok is false for other tokens or
// tokens without "exp". The token is decoded, not verified.
func Expiry(tok string) (exp time.Time, ok bool) {
	if !IsJWT(tok) {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp *json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == nil {
		return time.Time{}, false
	}
	sec, err := claims.Exp.Float64()
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(int64(sec), 0).UTC(), true
}
