// Package discover fills the source entries of defaults.json from a running
// environment: {"from": "GET /Planet", "pick": "/0/PlanetCode"} sends
// that GET and takes the value at "pick". Sources may use other values as
// placeholders ({planetCode}); they are fetched in dependency order.
// Only GET requests are sent.
package discover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
)

// Options configure the requests.
type Options struct {
	BaseURL string
	// Token is sent as "Authorization: Bearer <token>" if set.
	Token   string
	Headers map[string]string
	Client  *http.Client
	Timeout time.Duration
}

// Resolved is one fetched value.
type Resolved struct {
	Key, Request string
	Value        any
}

var placeholder = regexp.MustCompile(`\{([^}]+)\}`)

// Resolve fetches every source of defs and sets its value. It stops at the
// first source that fails: a missing value would otherwise turn into an
// invented one later.
func Resolve(ctx context.Context, defs *defaults.Defaults, opt Options) ([]Resolved, error) {
	sources := defs.Sources()
	if len(sources) == 0 {
		return nil, nil
	}
	if opt.BaseURL == "" {
		return nil, errors.New("sources in the defaults need -base-url")
	}
	if opt.Client == nil {
		opt.Client = &http.Client{}
	}
	if opt.Timeout == 0 {
		opt.Timeout = 10 * time.Second
	}
	order, err := sortSources(sources)
	if err != nil {
		return nil, err
	}
	var out []Resolved
	for _, e := range order {
		path, err := fill(e, defs)
		if err != nil {
			return out, err
		}
		v, err := fetch(ctx, opt, path, e.From.Pick)
		if err != nil {
			return out, fmt.Errorf("%q from %s: %w", e.Key, e.From.From, err)
		}
		defs.Resolve(e, v)
		out = append(out, Resolved{Key: e.Key, Request: "GET " + path, Value: v})
	}
	return out, nil
}

// sortSources orders sources so that placeholders are fetched first.
func sortSources(sources []*defaults.Entry) ([]*defaults.Entry, error) {
	byKey := map[string]*defaults.Entry{}
	for _, e := range sources {
		byKey[strings.ToLower(e.Key)] = e
	}
	var out []*defaults.Entry
	state := map[*defaults.Entry]int{} // 1 visiting, 2 done
	var visit func(e *defaults.Entry, chain []string) error
	visit = func(e *defaults.Entry, chain []string) error {
		switch state[e] {
		case 1:
			return fmt.Errorf("sources depend on each other in a cycle: %s", strings.Join(append(chain, e.Key), " → "))
		case 2:
			return nil
		}
		state[e] = 1
		for _, m := range placeholder.FindAllStringSubmatch(e.From.From, -1) {
			if dep := byKey[strings.ToLower(m[1])]; dep != nil {
				if err := visit(dep, append(chain, e.Key)); err != nil {
					return err
				}
			}
		}
		state[e] = 2
		out = append(out, e)
		return nil
	}
	for _, e := range sources {
		if err := visit(e, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// fill replaces the placeholders of the request path with known values.
func fill(e *defaults.Entry, defs *defaults.Defaults) (string, error) {
	path := strings.TrimSpace(strings.TrimPrefix(e.From.From, "GET "))
	var missing []string
	path = placeholder.ReplaceAllStringFunc(path, func(m string) string {
		name := m[1 : len(m)-1]
		v := defs.Plain(name)
		if v == nil {
			missing = append(missing, name)
			return m
		}
		return url.PathEscape(fmt.Sprint(v.Value))
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("%q: no value for %s in %s; add it to the defaults or as a source", e.Key, strings.Join(missing, ", "), e.From.From)
	}
	return path, nil
}

func fetch(ctx context.Context, opt Options, path, pick string) (any, error) {
	doc, err := Get(ctx, opt, path)
	if err != nil {
		return nil, err
	}
	return Pick(doc, pick)
}

// Get sends GET <BaseURL><path> and returns the decoded JSON body; numbers
// stay json.Number. A status other than 2xx is an error.
func Get(ctx context.Context, opt Options, path string) (any, error) {
	if opt.Client == nil {
		opt.Client = &http.Client{}
	}
	if opt.Timeout == 0 {
		opt.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()
	target := strings.TrimSuffix(opt.BaseURL, "/") + "/" + strings.TrimPrefix(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range opt.Headers {
		req.Header.Set(k, v)
	}
	if opt.Token != "" {
		req.Header.Set("Authorization", "Bearer "+opt.Token)
	}
	resp, err := opt.Client.Do(req)
	if err != nil {
		// the error contains the URL, never the token
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("response is not JSON: %w", err)
	}
	return doc, nil
}

// Pick selects a value with a JSON pointer whose segments may also be
// filters: "/0/Code" takes Code of the first element, "/[Active=true]/Code"
// Code of the first element whose Active is true. Filter values compare
// with the JSON text of the field ("true", "42", "de").
func Pick(doc any, pick string) (any, error) {
	if pick == "" || pick == "/" {
		return doc, nil
	}
	if !strings.HasPrefix(pick, "/") {
		return nil, fmt.Errorf("pick %q must start with /", pick)
	}
	cur := doc
	for _, seg := range strings.Split(pick[1:], "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		switch x := cur.(type) {
		case []any:
			if strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]") {
				field, want, ok := strings.Cut(seg[1:len(seg)-1], "=")
				if !ok {
					return nil, fmt.Errorf("filter %q needs the form [Field=value]", seg)
				}
				i := slices.IndexFunc(x, func(item any) bool {
					m, ok := item.(map[string]any)
					return ok && text(m[field]) == want
				})
				if i < 0 {
					return nil, fmt.Errorf("no element with %s=%s", field, want)
				}
				cur = x[i]
				continue
			}
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(x) {
				return nil, fmt.Errorf("index %q not in a list of %d elements", seg, len(x))
			}
			cur = x[i]
		case map[string]any:
			v, ok := x[seg]
			if !ok {
				return nil, fmt.Errorf("no field %q", seg)
			}
			cur = v
		default:
			return nil, fmt.Errorf("cannot select %q in a %T", seg, cur)
		}
	}
	if cur == nil {
		return nil, errors.New("the selected value is null")
	}
	return cur, nil
}

func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
