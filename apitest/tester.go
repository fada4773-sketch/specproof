package apitest

import (
	"context"
	"testing"
	"time"

	"github.com/fada4773-sketch/specproof/internal/selection"
)

// tester is the part of *testing.T that Run uses. It exists so the
// component tests can observe failures of a run without failing themselves.
type tester interface {
	Helper()
	Name() string
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
	// Skipf marks the (sub)test as skipped. With *testing.T it stops the
	// calling goroutine, so it must be the last call of a subtest.
	Skipf(format string, args ...any)
	Context() context.Context
	Deadline() (time.Time, bool)
	Cleanup(func())
	Run(name string, f func(tester)) bool
	// selects reports whether Run(name, ...) would start the subtest, i.e.
	// whether "go test -run/-skip" selects it (FR-GO-02).
	selects(name string) bool
}

type realT struct{ *testing.T }

func (t realT) selects(name string) bool {
	f, err := selection.FromFlags()
	if err != nil {
		return true // testing has already rejected invalid patterns
	}
	return f.Selected(t.Name() + "/" + name)
}

func (t realT) Run(name string, f func(tester)) bool {
	return t.T.Run(name, func(st *testing.T) { f(realT{st}) })
}
