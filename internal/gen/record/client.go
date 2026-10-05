package record

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/internal/gen/discover"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Client sends the requests to the instance.
type Client struct {
	Opt discover.Options
	// Count are the requests sent, by method.
	Count map[string]int
	// Log receives every request when it is answered, in the order they
	// are sent.
	Log func(Entry)
	n   int
}

// Entry is one request of the run.
type Entry struct {
	N      int    // its position in the run, from 1
	Tag    string // the tag whose step sent it
	Why    string // the operation and the reason: "read GetDocks", "details of dock D2"
	Method string
	URL    string
	Body   any // what was sent
	Status int // 0 if there was no answer
	Resp   any
	Err    error
}

// String is the line of the request in the log; a failed one shows what was
// sent and what came back.
func (e Entry) String() string {
	status := fmt.Sprint(e.Status)
	if e.Err != nil {
		status = "ERR"
	}
	line := fmt.Sprintf("#%03d %-14s %-6s %s → %s  %s", e.N, "["+e.Tag+"]", e.Method, e.URL, status, e.Why)
	if e.Err != nil {
		return line + "\n       error:  " + e.Err.Error()
	}
	if e.Status/100 != 2 {
		if e.Body != nil {
			line += "\n       sent:   " + clip(text(e.Body))
		}
		if e.Resp != nil {
			line += "\n       answer: " + clip(text(e.Resp))
		}
	}
	return line
}

// JSON is a value as JSON text.
func JSON(v any) string { return text(v) }

// Clip is a value as JSON text, at most 1500 bytes, for the log.
func Clip(v any) string { return clip(text(v)) }

func clip(s string) string {
	if len(s) > 1500 {
		return s[:1500] + fmt.Sprintf("… (%d bytes)", len(s))
	}
	return s
}

// response is an answer of the instance.
type response struct {
	Status int
	Body   any // decoded JSON, numbers as json.Number; nil without body
	Seq    int // the number of the request in the log
}

func (r response) ok() bool { return r.Status/100 == 2 }

// do sends one request and logs it; a status other than 2xx is no error.
func (c *Client) do(ctx context.Context, method, path string, body any, tag, why string) (resp response, err error) {
	c.n++
	e := Entry{N: c.n, Tag: tag, Why: why, Method: method, URL: path, Body: body}
	defer func() {
		resp.Seq = e.N
		e.Status, e.Resp, e.Err = resp.Status, resp.Body, err
		if c.Log != nil {
			c.Log(e)
		}
	}()
	return c.send(ctx, method, path, body)
}

func (c *Client) send(ctx context.Context, method, path string, body any) (response, error) {
	if c.Opt.Client == nil {
		c.Opt.Client = &http.Client{}
	}
	if c.Opt.Timeout == 0 {
		c.Opt.Timeout = 30 * time.Second
	}
	if c.Count == nil {
		c.Count = map[string]int{}
	}
	c.Count[method]++
	ctx, cancel := context.WithTimeout(ctx, c.Opt.Timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return response{}, err
		}
		rd = bytes.NewReader(b)
	}
	target := strings.TrimSuffix(c.Opt.BaseURL, "/") + "/" + strings.TrimPrefix(path, "/")
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.Opt.Headers {
		req.Header.Set(k, v)
	}
	if c.Opt.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Opt.Token)
	}
	resp, err := c.Opt.Client.Do(req)
	if err != nil {
		return response{}, err // the error names the URL, never the token
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return response{}, err
	}
	out := response{Status: resp.StatusCode}
	if len(bytes.TrimSpace(raw)) == 0 {
		return out, nil
	}
	if err := spec.DecodeJSON(raw, &out.Body); err != nil {
		if out.ok() {
			return out, fmt.Errorf("%s %s: the answer is not JSON: %w", method, path, err)
		}
		out.Body = nil
	}
	return out, nil
}
