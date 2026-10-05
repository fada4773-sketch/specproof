// Package exec builds HTTP requests from cases and sends them (FR-HTTP).
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/params"
)

// maxBody limits how much of a response body is read.
const maxBody = 16 << 20

// Prepared is a request ready to be sent.
type Prepared struct {
	Method string
	URL    string // absolute URL
	Target string // path and query, relative to the base URL, for display
	Header http.Header
	Body   []byte
	Auth   Auth // where the token went; used to build curl commands
}

// NotBuildableError reports that a case cannot be sent (FR-CASE-06).
type NotBuildableError struct{ Reason string }

func (e *NotBuildableError) Error() string { return e.Reason }

// Input is everything Prepare needs besides the case.
type Input struct {
	Base    string // base URL without trailing slash
	Params  params.Inputs
	Auth    Auth
	Token   string // used if Auth.Needed()
	Headers map[string]string
}

// Prepare builds the request for c. It returns a *NotBuildableError if a
// required parameter has no value.
func Prepare(c *cases.Case, in Input) (*Prepared, error) {
	if c.NotBuildable != "" {
		return nil, &NotBuildableError{c.NotBuildable}
	}
	if in.Auth.Unsupported != "" {
		return nil, &NotBuildableError{in.Auth.Unsupported}
	}
	path := c.Op.Path
	var query []params.Pair
	header := http.Header{}
	var cookies []params.Pair

	for _, p := range c.Op.Params {
		pin := in.Params
		pin.CaseName = c.ParamSource()
		v, ok := params.Resolve(p, pin)
		if !ok {
			if p.Required || p.In == openapi3.ParameterInPath {
				return nil, &NotBuildableError{fmt.Sprintf("no value for required parameter %q (%s)", p.Name, p.In)}
			}
			continue
		}
		switch p.In {
		case openapi3.ParameterInPath:
			s, err := params.Path(p, v.V)
			if err != nil {
				return nil, &NotBuildableError{err.Error()}
			}
			path = strings.ReplaceAll(path, "{"+p.Name+"}", s)
		case openapi3.ParameterInQuery:
			pairs, err := params.Query(p, v.V)
			if err != nil {
				return nil, &NotBuildableError{err.Error()}
			}
			query = append(query, pairs...)
		case openapi3.ParameterInHeader:
			s, err := params.Header(p, v.V)
			if err != nil {
				return nil, &NotBuildableError{err.Error()}
			}
			header.Set(p.Name, s)
		case openapi3.ParameterInCookie:
			pair, err := params.Cookie(p, v.V)
			if err != nil {
				return nil, &NotBuildableError{err.Error()}
			}
			cookies = append(cookies, pair)
		}
	}

	for k, v := range in.Headers {
		header.Set(k, v)
	}
	if in.Auth.Needed() {
		for _, pl := range in.Auth.Placements {
			switch pl.In {
			case "header":
				if pl.Bearer {
					header.Set("Authorization", "Bearer "+in.Token)
				} else {
					header.Set(pl.Name, in.Token)
				}
			case "query":
				query = append(query, params.Pair{Key: pl.Name, Value: in.Token})
			case "cookie":
				cookies = append(cookies, params.Pair{Key: pl.Name, Value: in.Token})
			}
		}
	}
	if len(cookies) > 0 {
		parts := make([]string, 0, len(cookies))
		for _, c := range cookies {
			parts = append(parts, c.Key+"="+url.QueryEscape(c.Value))
		}
		header.Set("Cookie", strings.Join(parts, "; "))
	}

	var body []byte
	if c.HasBody {
		b, ct, err := encodeBody(c.MediaType, c.Body)
		if err != nil {
			return nil, &NotBuildableError{err.Error()}
		}
		body = b
		header.Set("Content-Type", ct)
	}
	if accept := acceptHeader(c); accept != "" && header.Get("Accept") == "" {
		header.Set("Accept", accept)
	}

	target := path
	if len(query) > 0 {
		target += "?" + params.EncodeQuery(query)
	}
	return &Prepared{
		Method: c.Op.Method,
		URL:    in.Base + target,
		Target: target,
		Header: header,
		Body:   body,
		Auth:   in.Auth,
	}, nil
}

func encodeBody(mediaType string, v any) ([]byte, string, error) {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0])) {
	case "application/x-www-form-urlencoded":
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, "", errors.New("form body must be an object")
		}
		var pairs []params.Pair
		for _, k := range sortedKeys(obj) {
			pairs = append(pairs, params.Pair{Key: k, Value: params.Scalar(obj[k])})
		}
		return []byte(params.EncodeQuery(pairs)), mediaType, nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, "", fmt.Errorf("cannot encode body as JSON: %w", err)
		}
		return b, mediaType, nil
	}
}

// acceptHeader takes the media type of the expected response (FR-HTTP-05).
func acceptHeader(c *cases.Case) string {
	r := c.Expect.Response
	if r == nil || len(r.Content) == 0 {
		return ""
	}
	mt, _ := cases.PickMedia(r.Content)
	return mt
}

// Response is what the API answered.
type Response struct {
	Status   int
	Header   http.Header
	Body     []byte
	Duration time.Duration
}

// Request creates the HTTP request for p. ctx should carry the request
// timeout.
func (p *Prepared) Request(ctx context.Context) (*http.Request, error) {
	var body io.Reader
	if p.Body != nil {
		body = bytes.NewReader(p.Body)
	}
	req, err := http.NewRequestWithContext(ctx, p.Method, p.URL, body)
	if err != nil {
		return nil, err
	}
	req.Header = p.Header.Clone()
	return req, nil
}

// Do sends req and reads the response. The timeout must already be part of
// the request context (FR-HTTP-02); it is only used for the error message.
// Requests are never retried (FR-HTTP-03).
func Do(client *http.Client, req *http.Request, timeout time.Duration) (*Response, error) {
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("timeout after %s: %w", timeout, err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("timeout after %s while reading body: %w", timeout, err)
		}
		return nil, fmt.Errorf("read response body: %w", err)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: b, Duration: time.Since(start)}, nil
}

// Send sends p with a timeout that only affects this request.
func Send(ctx context.Context, client *http.Client, p *Prepared, timeout time.Duration) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := p.Request(ctx)
	if err != nil {
		return nil, err
	}
	return Do(client, req, timeout)
}

// WaitReady waits until the API answers (FR-HTTP-04). Without healthPath any
// HTTP response on "/" counts; with healthPath a 2xx is required. Only
// connection errors and non-2xx health responses are retried.
func WaitReady(ctx context.Context, client *http.Client, base, healthPath string, limit time.Duration) error {
	target := base + "/"
	if healthPath != "" {
		target = base + "/" + strings.TrimPrefix(healthPath, "/")
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if healthPath == "" || resp.StatusCode/100 == 2 {
				return nil
			}
			last = fmt.Errorf("%s returned %d", target, resp.StatusCode)
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("API at %s not reachable after %s: %w", target, limit, last)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// IsWrite reports whether a method changes state (NFR-07).
func IsWrite(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	}
	return true
}

// WritesAllowed reports whether writing requests may go to base (NFR-07):
// localhost, loopback addresses and hosts in allowed are permitted.
func WritesAllowed(base string, allowed []string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") || slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, host) }) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
