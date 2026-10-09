package apitest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/exec"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/spec"
	tokenpkg "github.com/fada4773-sketch/specproof/internal/token"
)

// prepareRequest builds the request for c. Bound parameters take their value
// from overrides first, then from the values captured so far. It also asks
// the token source (FR-AUTH-02); an error there aborts the run (FR-AUTH-03).
func (r *runner) prepareRequest(ctx context.Context, c *cases.Case, overrides map[string]any) (*exec.Prepared, string, error) {
	auth := exec.ResolveAuth(c.Op.Security, r.schemes)
	token := ""
	if auth.Needed() {
		src := r.cfg.Token
		if c.Kind == cases.Forbidden {
			src = r.cfg.ForbiddenToken
		}
		tok, err := src.Token(ctx)
		if err != nil {
			r.aborted = "token source returned an error: " + r.red.String(err.Error())
			return nil, "", fmt.Errorf("%s", r.aborted)
		}
		tok = r.cleanToken(tok)
		r.red.AddSecret(tok)
		token = tok
		switch c.Kind {
		case cases.Unauthorized:
			// Keep the placements for Unsupported checks, send no token.
			auth.Placements = nil
			token = ""
		case cases.InvalidToken:
			// The manipulated token contains parts of the real one; it is
			// redacted like the real token (FR-REP-03 b).
			token = r.tamper(tok)
			switch token {
			case tok:
				return nil, "", errors.New("Config.TamperToken returned the real token; it must return a different one")
			case "":
				return nil, "", errors.New("Config.TamperToken returned an empty token; the unauthorized case already covers a missing token")
			}
			r.red.AddSecret(token)
		}
	}
	var override map[string]any
	if c.NotFoundParam != "" {
		override = map[string]any{c.NotFoundParam: c.NotFoundValue}
	}
	p, err := exec.Prepare(c, exec.Input{
		Base: r.base,
		Params: params.Inputs{
			Fixed:    r.cfg.Params,
			OpID:     c.Op.ID,
			Override: override,
			// a not-found case needs no real values: it is sent anyway
			Generate: c.Kind == cases.NotFound,
			Binding: func(p *openapi3.Parameter) (any, bool) {
				b := r.binds.For(c.Op, p)
				if b == nil {
					return nil, false
				}
				if v, ok := overrides[b.Key()]; ok {
					return v, true
				}
				v, ok := r.values[b.Key()]
				return v, ok
			},
		},
		Auth:    auth,
		Token:   token,
		Headers: r.cfg.Headers,
	})
	return p, token, err
}

// missingDependency explains why a bound parameter of c has no value, or
// returns "" if all values are there (FR-ORDER-04).
func (r *runner) missingDependency(c *cases.Case) string {
	for _, b := range r.binds.Of(c.Op) {
		if _, ok := r.values[b.Key()]; ok || c.Kind == cases.NotFound {
			continue // a not-found case takes another value (Generate)
		}
		po := r.producers[b.Producer.ID]
		if r.paramFallback(c, b, po) {
			continue
		}
		switch {
		case po == nil:
			return fmt.Sprintf("dependency not available: %s (value for %q) was not executed", b.Producer.ID, b.Param.Name)
		case po.status == StatusSkipped || po.status == StatusNotBuildable:
			return fmt.Sprintf("dependency failed: %s is %s (%s), so %q has no value", po.c.Name, po.status, po.message, b.Param.Name)
		case po.resp == nil || po.resp.Status/100 != 2:
			return fmt.Sprintf("dependency failed: %s is %s, so %q has no value", po.c.Name, po.status, b.Param.Name)
		default:
			return fmt.Sprintf("dependency failed: the response of %s contains no value for %q (%s, %s)", po.c.Name, b.Param.Name, b.Source, b.Kind)
		}
	}
	return ""
}

// paramFallback reports whether a heuristic binding without value is
// replaced by Config.Params: a guessed source must not block a value the
// user set explicitly. The producer must have succeeded, so a failed
// producer still skips its dependents.
func (r *runner) paramFallback(c *cases.Case, b *bind.Binding, po *outcome) bool {
	if b.Kind != bind.Heuristic || po == nil || po.resp == nil || po.resp.Status/100 != 2 {
		return false
	}
	_, scoped := r.cfg.Params[c.Op.ID+"."+b.Param.Name]
	_, plain := r.cfg.Params[b.Param.Name]
	if !scoped && !plain {
		return false
	}
	if r.fallbackWarned == nil {
		r.fallbackWarned = map[string]bool{}
	}
	if key := b.Producer.ID + "." + b.Param.Name; !r.fallbackWarned[key] {
		r.fallbackWarned[key] = true
		r.warn(fmt.Sprintf("the response of %s contains no value for %q (%s, heuristic); Config.Params is used instead", po.c.Name, b.Param.Name, b.Source))
	}
	return true
}

// capture stores the values that o's operation provides to other operations.
// Only the first successful case of a producer sets a value; later changes
// come from PUT/PATCH (FR-PARAM-04).
func (r *runner) capture(o *outcome) {
	for _, b := range r.binds.By(o.c.Op) {
		key := b.Key()
		if _, ok := r.values[key]; ok {
			continue
		}
		if v, ok := bind.Extract(b.Source, b.Kind, o.resp.Header, o.decoded, o.c.Body); ok {
			r.values[key] = v
		}
	}
}

// verifyWrite reads the resource after a successful write (FR-VERIFY).
func (r *runner) verifyWrite(ctx context.Context, o *outcome) {
	op := o.c.Op
	poll, ok := verifySettings(op)
	if !ok {
		return
	}
	switch op.Method {
	case http.MethodPost:
		if get := r.itemGet(op); get != nil {
			r.verifyStored(ctx, o, get, nil, poll)
		}
	case http.MethodPut, http.MethodPatch:
		if get := r.opAt(http.MethodGet, op.Path); get != nil {
			r.verifyStored(ctx, o, get, r.candidates(o.c), poll)
		}
	case http.MethodDelete:
		if get := r.opAt(http.MethodGet, op.Path); get != nil {
			r.verifyGone(ctx, o, get, poll)
		}
	}
}

// defaultPollTimeout applies to x-apitest-verify: {poll: true} without timeout.
const defaultPollTimeout = 10 * time.Second

// pollInterval is the pause between two GETs while polling.
const pollInterval = 200 * time.Millisecond

// verifySettings reads x-apitest-verify: false disables the check
// (FR-VERIFY-05), {poll: true, timeout: 10s} repeats it until it succeeds
// or the timeout ends (FR-VERIFY-06). poll is 0 without polling.
func verifySettings(op *spec.Operation) (poll time.Duration, enabled bool) {
	switch v := op.Op.Extensions["x-apitest-verify"].(type) {
	case bool:
		return 0, v
	case map[string]any:
		if p, _ := v["poll"].(bool); !p {
			return 0, true
		}
		poll = defaultPollTimeout
		if ts, ok := v["timeout"].(string); ok {
			if d, err := time.ParseDuration(ts); err == nil && d > 0 {
				poll = d
			}
		}
		return poll, true
	}
	return 0, true
}

// repeat calls try until it reports done or the poll time is over. Without
// polling try runs once.
func repeat(ctx context.Context, poll time.Duration, try func() bool) {
	deadline := time.Now().Add(poll)
	for !try() && poll > 0 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

// itemGet finds "GET <post path>/{param}" whose parameter is bound to the
// POST, i.e. the GET that reads the created resource.
func (r *runner) itemGet(post *spec.Operation) *spec.Operation {
	for _, op := range r.spec.Ops {
		if op.Method != http.MethodGet || !strings.HasPrefix(op.Path, post.Path+"/") {
			continue
		}
		rest := strings.TrimPrefix(op.Path, post.Path+"/")
		if strings.Contains(rest, "/") || !strings.HasPrefix(rest, "{") || !strings.HasSuffix(rest, "}") {
			continue
		}
		name := rest[1 : len(rest)-1]
		for _, p := range op.Params {
			if p.In == openapi3.ParameterInPath && p.Name == name {
				if b := r.binds.For(op, p); b != nil && b.Producer == post {
					return op
				}
			}
		}
	}
	return nil
}

func (r *runner) opAt(method, path string) *spec.Operation {
	for _, op := range r.spec.Ops {
		if op.Method == method && op.Path == path {
			return op
		}
	}
	return nil
}

// candidates returns new values for bound path parameters that a PUT/PATCH
// changes, e.g. code "l" -> "c" (FR-PARAM-04).
func (r *runner) candidates(c *cases.Case) map[string]any {
	out := map[string]any{}
	if !c.HasBody {
		return out
	}
	for _, b := range r.binds.Of(c.Op) {
		if b.Param.In != openapi3.ParameterInPath || b.Source.Header != "" || b.Source.HasConst || b.Source.Pointer == "" {
			continue
		}
		v, ok := bind.Pointer(c.Body, b.Source.Pointer)
		if !ok {
			continue
		}
		if cur, has := r.values[b.Key()]; has && !compare.Equal(v, cur) {
			out[b.Key()] = v
		}
	}
	return out
}

// verifyStored reads the resource with GET and checks that the sent body was
// stored (FR-VERIFY-01..03). With candidates, the new key values are tried
// first; the binding follows the value that is actually stored (FR-PARAM-04).
func (r *runner) verifyStored(ctx context.Context, o *outcome, get *spec.Operation, candidates map[string]any, poll time.Duration) {
	vc := cases.VerifyCase(get)
	var ex *exchange
	var diffs []compare.Diff
	var err error
	repeat(ctx, poll, func() bool {
		ex, err = r.readBack(ctx, vc, candidates)
		if err != nil || ex.resp.Status/100 != 2 {
			return false
		}
		diffs = r.storedDiffs(o, vc, ex)
		return len(diffs) == 0
	})
	if err != nil {
		if o.status == StatusPassed {
			o.message = "GET check not possible: " + err.Error()
		}
		return
	}
	o.verify = ex
	if o.status != StatusPassed {
		return
	}
	check := fmt.Sprintf("GET %s", ex.prepared.Target)
	switch {
	case ex.resp.Status/100 != 2:
		o.status = StatusDataMismatch
		o.expected += ", " + check + " returns the resource"
		o.actual += fmt.Sprintf(", GET returns %d", ex.resp.Status)
		o.message = fmt.Sprintf("the written resource cannot be read back: %s returns %d", check, ex.resp.Status)
	case len(diffs) > 0:
		o.status, o.diffs = StatusDataMismatch, diffs
		o.expected += ", " + check + " returns the sent values"
		o.actual += ", GET returns different values"
		o.message = fmt.Sprintf("data was not stored as sent: %s differs (%s, first at %s)", check, plural(len(diffs), "difference", "differences"), diffs[0].Pointer)
	}
}

// readBack sends the GET, trying new key values first (FR-PARAM-04).
func (r *runner) readBack(ctx context.Context, vc *cases.Case, candidates map[string]any) (*exchange, error) {
	ex, err := r.send(ctx, vc, candidates)
	if len(candidates) == 0 {
		return ex, err
	}
	if err == nil && ex.resp.Status/100 == 2 {
		for k, v := range candidates {
			r.values[k] = v
		}
		return ex, nil
	}
	return r.send(ctx, vc, nil)
}

// storedDiffs compares the sent body with the body the GET returned.
func (r *runner) storedDiffs(o *outcome, vc *cases.Case, ex *exchange) []compare.Diff {
	if !o.c.HasBody {
		return nil
	}
	ignore := append(append(r.ignore(o.c), vc.Ignore...), writeOnlyNames(o.c)...)
	return compare.Values(o.c.Body, decodeJSON(ex.resp), compare.Options{
		Mode:       compare.ModeSubset,
		Ignore:     ignore,
		Schema:     compare.Schema(vc.Expect.Response, ex.resp.Header.Get("Content-Type")),
		Unordered:  vc.Unordered,
		IgnoreCase: r.cfg.CaseInsensitive,
	})
}

// verifyGone checks that GET returns 404 after a DELETE, if the spec
// documents 404 for the GET (FR-VERIFY-04).
func (r *runner) verifyGone(ctx context.Context, o *outcome, get *spec.Operation, poll time.Duration) {
	if get.Op.Responses == nil || get.Op.Responses.Value("404") == nil {
		return
	}
	var ex *exchange
	var err error
	repeat(ctx, poll, func() bool {
		ex, err = r.send(ctx, cases.VerifyCase(get), nil)
		return err == nil && ex.resp.Status/100 != 2
	})
	if err != nil {
		if o.status == StatusPassed {
			o.message = "GET check not possible: " + err.Error()
		}
		return
	}
	o.verify = ex
	if o.status == StatusPassed && ex.resp.Status/100 == 2 {
		o.status = StatusDataMismatch
		o.expected += ", GET " + ex.prepared.Target + " returns 404"
		o.actual += fmt.Sprintf(", GET returns %d", ex.resp.Status)
		o.message = fmt.Sprintf("the resource still exists after DELETE: GET %s returns %d", ex.prepared.Target, ex.resp.Status)
	}
}

// send prepares and sends an additional request, e.g. the GET check.
func (r *runner) send(ctx context.Context, c *cases.Case, overrides map[string]any) (*exchange, error) {
	p, _, err := r.prepareRequest(ctx, c, overrides)
	if err != nil {
		return nil, err
	}
	resp, err := exec.Send(ctx, r.client, p, r.cfg.RequestTimeout)
	if err != nil {
		return nil, err
	}
	return &exchange{prepared: p, resp: resp}, nil
}

// decodeJSON decodes a JSON response body, or returns nil.
func decodeJSON(resp *exec.Response) any {
	if resp == nil || len(strings.TrimSpace(string(resp.Body))) == 0 {
		return nil
	}
	var v any
	if err := spec.DecodeJSON(resp.Body, &v); err != nil {
		return nil
	}
	return v
}

// cleanToken removes what often slips into copied tokens: surrounding or
// embedded whitespace and line breaks (net/http refuses header values with
// line breaks) and a "Bearer " prefix (apitest adds it itself). A token
// never contains whitespace (RFC 6750). The first change is logged once.
func (r *runner) cleanToken(tok string) string {
	fields := strings.Fields(tok)
	if len(fields) > 1 && strings.EqualFold(fields[0], "bearer") {
		fields = fields[1:]
	}
	clean := strings.Join(fields, "")
	if clean != tok && !r.tokenCleaned {
		r.tokenCleaned = true
		r.warn("the token contained whitespace, line breaks or a \"Bearer \" prefix; they were removed before sending")
	}
	return clean
}

// tamper derives the token of the invalid-token case (Config.TamperToken).
func (r *runner) tamper(tok string) string {
	if r.cfg.TamperToken != nil {
		return r.cfg.TamperToken(tok)
	}
	return tokenpkg.Truncate(tok)
}
