//go:build race

package apitest

// raceEnabled reports whether the race detector is on; timing limits are
// only checked without it.
const raceEnabled = true
