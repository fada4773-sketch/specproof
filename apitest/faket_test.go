package apitest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeT records what a run reports instead of failing the real test. Subtests
// run synchronously, like subtests of *testing.T without t.Parallel.
type fakeT struct {
	name     string
	parent   *fakeT
	ctx      context.Context
	deadline time.Time
	filter   func(fullName string) bool

	mu       sync.Mutex
	failed   bool
	errors   []string
	logs     []string
	skipped  string
	subs     []*fakeT
	cleanups []func()
}

func newFakeT(t *testing.T, name string) *fakeT {
	return &fakeT{name: name, ctx: t.Context()}
}

func (f *fakeT) Helper()      {}
func (f *fakeT) Name() string { return f.name }

func (f *fakeT) Errorf(format string, args ...any) {
	f.mu.Lock()
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
	f.mu.Unlock()
	for p := f; p != nil; p = p.parent {
		p.mu.Lock()
		p.failed = true
		p.mu.Unlock()
	}
}

func (f *fakeT) Logf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

func (f *fakeT) Skipf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.skipped = fmt.Sprintf(format, args...)
}

func (f *fakeT) Context() context.Context { return f.ctx }

func (f *fakeT) selects(name string) bool {
	return f.filter == nil || f.filter(f.name+"/"+name)
}

func (f *fakeT) Deadline() (time.Time, bool) { return f.deadline, !f.deadline.IsZero() }

func (f *fakeT) Cleanup(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanups = append(f.cleanups, fn)
}

func (f *fakeT) Run(name string, fn func(tester)) bool {
	full := f.name + "/" + name
	if f.filter != nil && !f.filter(full) {
		return true
	}
	sub := &fakeT{name: full, parent: f, ctx: f.ctx, deadline: f.deadline, filter: f.filter}
	f.mu.Lock()
	f.subs = append(f.subs, sub)
	f.mu.Unlock()
	fn(sub)
	sub.finish()
	return !sub.failed
}

// finish runs the cleanups in reverse order, like testing does.
func (f *fakeT) finish() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
	f.cleanups = nil
}

// output returns every message of f and its subtests.
func (f *fakeT) output() string {
	var b strings.Builder
	var walk func(*fakeT)
	walk = func(t *fakeT) {
		for _, e := range t.errors {
			b.WriteString(e + "\n")
		}
		for _, l := range t.logs {
			b.WriteString(l + "\n")
		}
		if t.skipped != "" {
			b.WriteString(t.skipped + "\n")
		}
		for _, s := range t.subs {
			walk(s)
		}
	}
	walk(f)
	return b.String()
}

// errorCount counts Errorf calls in f and its subtests.
func (f *fakeT) errorCount() int {
	n := len(f.errors)
	for _, s := range f.subs {
		n += s.errorCount()
	}
	return n
}
