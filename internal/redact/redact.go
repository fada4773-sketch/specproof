// Package redact removes secrets from everything apitest outputs: report,
// t.Log/t.Errorf messages and error texts (FR-REP-03, NFR-06).
package redact

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Mask replaces every redacted value.
const Mask = "***"

// DefaultFields are field names that are always redacted in bodies. Matching
// is case-insensitive and by substring, so "accessToken" or "clientSecret"
// are covered as well.
var DefaultFields = []string{"password", "secret", "token", "apikey"}

// defaultHeaders are always redacted.
var defaultHeaders = []string{"Authorization", "Cookie", "Set-Cookie", "Proxy-Authorization"}

// Redactor knows which values, headers, query parameters and fields are
// secret. It is safe for concurrent use.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string // sorted longest first
	fields  []string // lower case
	headers map[string]bool
	params  map[string]bool // query parameter and cookie names carrying secrets
	re      *regexp.Regexp  // matches name=value pairs of params in text
}

// New returns a Redactor for the default fields plus extra field names.
func New(extraFields []string) *Redactor {
	r := &Redactor{headers: map[string]bool{}, params: map[string]bool{}}
	for _, f := range append(append([]string(nil), DefaultFields...), extraFields...) {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			r.fields = append(r.fields, f)
		}
	}
	for _, h := range defaultHeaders {
		r.headers[http.CanonicalHeaderKey(h)] = true
	}
	return r
}

// AddSecret registers a secret value, e.g. a token. For a JWT the
// "header.payload" part is registered as well, because a manipulated copy of
// the token shares it (FR-REP-03 b).
func (r *Redactor) AddSecret(s string) {
	if s == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	add := func(v string) {
		for _, have := range r.secrets {
			if have == v {
				return
			}
		}
		r.secrets = append(r.secrets, v)
	}
	add(s)
	if parts := strings.Split(s, "."); len(parts) == 3 && parts[0] != "" && parts[1] != "" {
		add(parts[0] + "." + parts[1])
	}
	sort.SliceStable(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

// AddHeader registers a header whose value is secret, e.g. an API key header.
func (r *Redactor) AddHeader(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headers[http.CanonicalHeaderKey(name)] = true
}

// AddParam registers a query parameter or cookie name whose value is secret.
func (r *Redactor) AddParam(name string) {
	if name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.params[name] = true
	names := make([]string, 0, len(r.params))
	for n := range r.params {
		names = append(names, regexp.QuoteMeta(n))
	}
	sort.Strings(names)
	// name=value in URLs (?a=b&c=d) and cookie strings (a=b; c=d). Values
	// starting with "$" are shell placeholders like $TOKEN in curl commands.
	r.re = regexp.MustCompile(`((?:^|[?&;\s"'])(?:` + strings.Join(names, "|") + `)=)[^$&;\s"'#][^&;\s"'#]*`)
}

// String redacts secrets and secret parameters in arbitrary text, including
// error messages that contain URLs (FR-REP-03 a).
func (r *Redactor) String(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, sec := range r.secrets {
		s = strings.ReplaceAll(s, sec, Mask)
	}
	if r.re != nil {
		s = r.re.ReplaceAllString(s, "${1}"+Mask)
	}
	return s
}

// SecretHeader reports whether the value of header name is always masked.
func (r *Redactor) SecretHeader(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.headers[http.CanonicalHeaderKey(name)]
}

// headerWords mark header names whose values are masked in addition to the
// field names, e.g. X-Signature or X-Session-Id. "auth" is not one of them:
// WWW-Authenticate tells who rejected a request and is not secret.
var headerWords = []string{"signature", "session", "cookie"}

// SecretHeaderName reports whether the value of header name must be masked:
// a secret header, or a name that contains a secret field name or one of
// headerWords, ignoring case and dashes ("X-Api-Key" matches "apikey").
func (r *Redactor) SecretHeaderName(name string) bool {
	if r.SecretHeader(name) {
		return true
	}
	n := strings.ReplaceAll(name, "-", "")
	return matches(n, r.fields) || matches(n, headerWords)
}

// Header returns a redacted copy of h.
func (r *Redactor) Header(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		r.mu.RLock()
		secret := r.headers[http.CanonicalHeaderKey(k)]
		r.mu.RUnlock()
		cp := make([]string, len(vs))
		for i, v := range vs {
			if secret {
				cp[i] = Mask
			} else {
				cp[i] = r.String(v)
			}
		}
		out[k] = cp
	}
	return out
}

// Value returns a redacted copy of a decoded JSON value. Object fields whose
// name matches a secret field name, or is listed in extra (e.g. writeOnly
// properties), are replaced by Mask at every level.
func (r *Redactor) Value(v any, extra ...string) any {
	fields := r.fields
	for _, e := range extra {
		fields = append(fields[:len(fields):len(fields)], strings.ToLower(e))
	}
	return r.value(v, fields)
}

func (r *Redactor) value(v any, fields []string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			if matches(k, fields) {
				out[k] = Mask
				continue
			}
			out[k] = r.value(child, fields)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, child := range x {
			out[i] = r.value(child, fields)
		}
		return out
	case string:
		return r.String(x)
	default:
		return v
	}
}

func matches(name string, fields []string) bool {
	n := strings.ToLower(name)
	for _, f := range fields {
		if strings.Contains(n, f) {
			return true
		}
	}
	return false
}
