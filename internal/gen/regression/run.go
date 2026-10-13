package regression

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/compare"
	"github.com/fada4773-sketch/specproof/internal/scenario"
)

// ReadInput configures one read of a stage.
type ReadInput struct {
	Spec     string // the spec of the version that runs on the stage
	Scenario string // the reads (regression-scenario_gen.yaml)
	BaseURL  string
	Token    string
	Headers  map[string]string
	Report   string // Markdown report; "" writes none
	Probe    time.Duration
	Out      io.Writer
	Client   *http.Client
	// Kind, Pre and Seed go into the snapshot.
	Kind, Pre, Seed string
	// Timeout limits each request; 0 is apitest's default.
	Timeout time.Duration
}

// Read sends every read of the scenario (GET only) and keeps the answers
// as a snapshot; with a probe it reads once more after that time and marks
// what changed as volatile.
func Read(ctx context.Context, in ReadInput) (*scenario.Snapshot, *apitest.Result, error) {
	first, res, err := pass(ctx, in, in.Report, in.Out)
	if err != nil {
		return nil, nil, err
	}
	second := map[string]apitest.StepExchange{}
	if in.Probe > 0 {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(in.Probe):
		}
		again, _, err := pass(ctx, in, "", nil)
		if err != nil {
			return nil, nil, err
		}
		for _, ex := range again {
			second[ex.ID] = ex
		}
	}
	snap := &scenario.Snapshot{Kind: in.Kind, Spec: in.Spec, BaseURL: in.BaseURL, Pre: in.Pre, Seed: in.Seed,
		Created: time.Now().UTC().Format(time.RFC3339), Reread: in.Probe > 0}
	got := map[string]bool{}
	for _, ex := range first {
		got[ex.ID] = true
		sh := &scenario.Shot{ID: ex.ID, Op: ex.OperationID, Request: ex.Method + " " + ex.Target, Status: ex.Status,
			DurationMs: ex.Duration.Milliseconds(), Problems: ex.Problems}
		if ct := ex.ResponseHeader.Get("Content-Type"); ct != "" {
			if mt, _, err := mime.ParseMediaType(ct); err == nil {
				ct = mt
			}
			sh.Headers = map[string]string{"Content-Type": ct}
		}
		if ex.ResponseBody != nil {
			sh.Body, sh.HasBody = ex.ResponseBody, true
		}
		if ex.Outcome == apitest.StatusFailed && ex.Message != "" && len(sh.Problems) == 0 {
			sh.Problems = append(sh.Problems, ex.Message)
		}
		if b, ok := second[ex.ID]; ok && b.Status == ex.Status {
			for _, p := range changed("", ex.ResponseBody, b.ResponseBody) {
				g := scenario.Generalize(p)
				if !slices.Contains(sh.Volatile, g) {
					sh.Volatile = append(sh.Volatile, g)
				}
			}
			slices.Sort(sh.Volatile)
		}
		snap.Steps = append(snap.Steps, sh)
	}
	// steps without an answer
	for _, c := range res.Cases {
		id := c.Name[strings.Index(c.Name, "/")+1:]
		if got[id] {
			continue
		}
		snap.Steps = append(snap.Steps, &scenario.Shot{ID: id, Op: c.Operation, Request: c.Operation, Error: fmt.Sprintf("%s: %s", c.Status, c.Message)})
	}
	return snap, res, nil
}

func pass(ctx context.Context, in ReadInput, report string, out io.Writer) ([]apitest.StepExchange, *apitest.Result, error) {
	var got []apitest.StepExchange
	cfg := apitest.ScenarioConfig{
		Config: apitest.Config{
			SpecPath:       in.Spec,
			BaseURL:        in.BaseURL,
			Headers:        in.Headers,
			HTTPClient:     in.Client,
			ReportPath:     report,
			RequestTimeout: in.Timeout,
		},
		Scenario: in.Scenario,
		OnStep:   func(ex apitest.StepExchange) { got = append(got, ex) },
	}
	if in.Token != "" {
		cfg.Token = apitest.StaticToken(in.Token)
	}
	if report == "" {
		cfg.DisableReports = true
	}
	res, err := apitest.RunScenarioStandalone(ctx, cfg, apitest.StandaloneOptions{Out: out})
	if err != nil {
		return nil, nil, err
	}
	return got, res, nil
}

// changed lists the JSON pointers where two values differ.
func changed(ptr string, a, b any) []string {
	root := ptr
	if root == "" {
		root = "/"
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return []string{root}
		}
		var out []string
		for _, k := range sortedKeys(x) {
			out = append(out, changed(ptr+"/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"), x[k], y[k])...)
		}
		return out
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return []string{root}
		}
		var out []string
		for i := range x {
			out = append(out, changed(fmt.Sprintf("%s/%d", ptr, i), x[i], y[i])...)
		}
		return out
	}
	if !compare.Equal(a, b) {
		return []string{root}
	}
	return nil
}
