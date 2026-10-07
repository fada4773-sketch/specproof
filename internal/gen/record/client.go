package record

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/internal/gen/discover"
)

// Client sends the requests of a run to the instance.
type Client struct {
	Opt discover.Options
}

// answer is what the instance sent back.
type answer struct {
	Status int
	Header http.Header
	Raw    []byte // the body as sent
}

// send sends one request; a status other than 2xx is no error.
func (c *Client) send(ctx context.Context, method, path, mediaType string, body any, hasBody bool) (answer, error) {
	client := c.Opt.Client
	if client == nil {
		client = &http.Client{}
	}
	timeout := c.Opt.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if hasBody {
		b, err := json.Marshal(body)
		if err != nil {
			return answer{}, err
		}
		rd = bytes.NewReader(b)
	}
	target := strings.TrimSuffix(c.Opt.BaseURL, "/") + "/" + strings.TrimPrefix(path, "/")
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return answer{}, err
	}
	req.Header.Set("Accept", "application/json")
	if hasBody {
		if mediaType == "" {
			mediaType = "application/json"
		}
		req.Header.Set("Content-Type", mediaType)
	}
	for k, v := range c.Opt.Headers {
		req.Header.Set(k, v)
	}
	if c.Opt.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Opt.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return answer{}, err // the error names the URL, never the token
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return answer{}, err
	}
	return answer{Status: resp.StatusCode, Header: resp.Header, Raw: raw}, nil
}
