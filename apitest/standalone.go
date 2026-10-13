package apitest

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/fada4773-sketch/specproof/internal/selection"
)

// StandaloneOptions configure RunScenarioStandalone.
type StandaloneOptions struct {
	// Out receives the progress like "go test -v": one line per step and
	// the messages of failed steps. nil discards it.
	Out io.Writer
	// Run and Skip select steps like "go test -run" and "-skip", matched
	// against "<tag>/<id>".
	Run, Skip string
}

// RunScenarioStandalone runs a scenario outside of "go test", e.g. from a
// command line tool in a pipeline. It behaves like RunScenario; the result
// says whether it failed.
func RunScenarioStandalone(ctx context.Context, cfg ScenarioConfig, opt StandaloneOptions) (*Result, error) {
	f, err := selection.New(opt.Run, opt.Skip)
	if err != nil {
		return nil, err
	}
	out := opt.Out
	if out == nil {
		out = io.Discard
	}
	root := &standaloneT{ctx: ctx, out: out, filter: f, mu: &sync.Mutex{}}
	res := runScenario(root, cfg)
	root.runCleanups()
	if root.failed {
		res.Failed = true
	}
	return res, nil
}

// standaloneT implements tester without the testing package.
type standaloneT struct {
	name     string
	ctx      context.Context
	out      io.Writer
	filter   *selection.Filter
	mu       *sync.Mutex
	failed   bool
	skipped  bool
	logs     []string
	cleanups []func()
}

func (t *standaloneT) Helper()      {}
func (t *standaloneT) Name() string { return t.name }

func (t *standaloneT) Errorf(format string, args ...any) {
	t.failed = true
	t.log(fmt.Sprintf(format, args...))
}

func (t *standaloneT) Logf(format string, args ...any) { t.log(fmt.Sprintf(format, args...)) }

func (t *standaloneT) Skipf(format string, args ...any) {
	t.skipped = true
	t.log(fmt.Sprintf(format, args...))
}

func (t *standaloneT) log(msg string) {
	if t.name == "" {
		// the run itself: print at once
		t.mu.Lock()
		fmt.Fprintln(t.out, msg)
		t.mu.Unlock()
		return
	}
	t.logs = append(t.logs, msg)
}

func (t *standaloneT) Context() context.Context    { return t.ctx }
func (t *standaloneT) Deadline() (time.Time, bool) { return t.ctx.Deadline() }
func (t *standaloneT) Cleanup(f func())            { t.cleanups = append(t.cleanups, f) }

func (t *standaloneT) runCleanups() {
	for i := len(t.cleanups) - 1; i >= 0; i-- {
		t.cleanups[i]()
	}
}

func (t *standaloneT) Run(name string, f func(tester)) bool {
	full := name
	if t.name != "" {
		full = t.name + "/" + name
	}
	if !t.selects(name) {
		return true
	}
	sub := &standaloneT{name: full, ctx: t.ctx, out: t.out, filter: t.filter, mu: t.mu}
	start := time.Now()
	f(sub)
	sub.runCleanups()
	state := "PASS"
	switch {
	case sub.failed:
		state = "FAIL"
		t.failed = true
	case sub.skipped:
		state = "SKIP"
	}
	t.mu.Lock()
	fmt.Fprintf(t.out, "--- %s: %s (%.2fs)\n", state, full, time.Since(start).Seconds())
	for _, l := range sub.logs {
		for _, line := range strings.Split(l, "\n") {
			fmt.Fprintf(t.out, "    %s\n", line)
		}
	}
	t.mu.Unlock()
	return !sub.failed
}

func (t *standaloneT) selects(name string) bool {
	full := name
	if t.name != "" {
		full = t.name + "/" + name
	}
	return t.filter.Selected(full)
}
