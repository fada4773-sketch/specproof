// Package scenario makes the examples of a spec consistent with the data
// apitest meets at run time. Every resource of the model gets records: the
// state the test database starts with, fetched from a running instance
// (snapshot) or generated. The cases are played in the order apitest runs
// them (internal/plan, with the Config from "$apitest"); an update changes
// the record, and every example shows the record as it is at the position
// of its case: path parameters, request bodies, responses and lists.
//
// Nothing but examples is written into the spec, apart from one exception:
// a parameter object that is shared by paths of different records is
// copied into the path, so each path can have its own example.
package scenario

import (
	"context"
	"fmt"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/dict"
	"github.com/fada4773-sketch/specproof/internal/gen/model"
	"github.com/fada4773-sketch/specproof/internal/gen/yamldoc"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// Codes of the notes and problems.
const (
	CodeRecord        = "RECORD"            // the records of a resource
	CodeSnapshot      = "SNAPSHOT"          // records fetched from the instance
	CodeSnapshotEmpty = "SNAPSHOT_EMPTY"    // a list returned nothing, records are generated
	CodeGenerated     = "GENERATED"         // records generated on purpose: "$snapshot" without "from"
	CodeSnapshotShort = "SNAPSHOT_SHORT"    // fewer elements than "count"
	CodeSnapshotFail  = "SNAPSHOT_FAILED"   // a request of the snapshot failed
	CodeSnapshotDiff  = "SNAPSHOT_MISMATCH" // two responses disagree about a record
	CodeSnapshotWins  = "SNAPSHOT_WINS"     // a default is not used for a fetched record
	CodeSnapshotKey   = "SNAPSHOT_KEY"      // the record a key default selects does not exist
	CodeLint          = "LINT_IGNORED"      // a schema violation that -ignorelinting lets through
	CodeSeed          = "SNAPSHOT_SEED"     // the "seed" values the requests below take
	CodeUpdate        = "UPDATE"            // an update changes a record
	CodeSideEffect    = "SIDE_EFFECT"       // an operation of no resource changes records
	CodeCreatedKey    = "CREATED_KEY"       // the server assigns a key of a created record
	CodeInlined       = "PARAM_INLINED"     // a shared parameter was copied into the path
	CodeShared        = "EXAMPLE_SHARED"    // a shared example would need two values
	CodeInvalid       = "EXAMPLE_INVALID"   // a projected example violates its schema
	CodeMessage       = "MESSAGE"           // a message field got its pattern
	CodeModel         = "MODEL"             // something the model could not decide
	CodePlan          = "PLAN"              // the order of the cases could not be built
	CodeDefault       = "DEFAULT_CONFLICT"  // two defaults set one key differently
)

// Record is the state of one record: field name (as in the resource's
// DTOs) → value, numbers as json.Number.
type Record map[string]any

func (r Record) clone() Record {
	out := make(Record, len(r))
	for k, v := range r {
		out[k] = v
	}
	return out
}

// Note is one message.
type Note struct {
	Code, Where, Message string
}

func (n Note) String() string { return fmt.Sprintf("%s %s: %s", n.Code, n.Where, n.Message) }

// Fetcher sends a GET to the running instance and returns the decoded
// body. Path is relative to the base URL, with the query.
type Fetcher func(ctx context.Context, path string) (any, error)

// Input of Run.
type Input struct {
	Doc      *yamldoc.Doc
	Spec     *spec.Spec // the file loaded by apitest's loader
	Dict     *dict.Dict
	Defaults *defaults.Defaults
	Model    *model.Model
	Seed     uint64
	// Fetch takes the records from a running instance; nil generates them.
	// With Fetch, the examples of the spec are never used as values.
	Fetch Fetcher
	// IgnoreLinting reports data and examples that violate their schema
	// (CodeLint) instead of stopping the run.
	IgnoreLinting bool
}

// Result of Run. With Problems the document must not be saved.
type Result struct {
	Notes    []Note
	Problems []Note
	Changed  bool
	// Records is the start state, for Verify.
	Records *Store
	Stats   Stats
}

// Stats counts what Run did.
type Stats struct {
	Resources, Records, Fetched, Updates, Examples, Inlined int
}

func (r *Result) note(code, where, format string, args ...any) {
	r.Notes = append(r.Notes, Note{code, where, fmt.Sprintf(format, args...)})
}

// lint reports a schema violation: a problem, or with IgnoreLinting a note
// (CodeLint). It reports whether the run goes on with the value.
func (r *Result) lint(ignore bool, code, where, format string, args ...any) bool {
	if ignore {
		r.note(CodeLint, where, "%s: %s", code, fmt.Sprintf(format, args...))
		return true
	}
	r.problem(code, where, format, args...)
	return false
}

func (r *Result) problem(code, where, format string, args ...any) {
	r.Problems = append(r.Problems, Note{code, where, fmt.Sprintf(format, args...)})
}

// Run builds the records, plays the cases and writes the examples into the
// document.
func Run(ctx context.Context, in Input) *Result {
	res := &Result{}
	if in.Model == nil || len(in.Model.Resources) == 0 {
		res.Records = newStore()
		return res
	}
	for _, n := range in.Model.Notes {
		res.note(CodeModel, n.Where, "%s", n.Message)
	}
	b := &builder{in: in, res: res, store: newStore()}
	if in.Fetch != nil {
		b.snapshot(ctx)
	} else {
		b.generate()
	}
	res.Records = b.store
	res.Stats.Resources = len(in.Model.Resources)
	for _, r := range in.Model.Resources {
		res.Stats.Records += len(b.store.Records(r.Name))
		res.note(CodeRecord, r.Name, "%s", describe(r, b.store.Records(r.Name)))
	}
	if len(res.Problems) > 0 {
		return res
	}
	b.keepInDict()
	t := &timeline{in: in, res: res, store: b.store}
	steps, err := t.play()
	if err != nil {
		res.problem(CodePlan, "plan", "%v", err)
		return res
	}
	w := newWriter(in, res, b.store)
	w.write(steps)
	w.fetched()
	w.messages()
	return res
}

// describe lists the keys of the records of a resource.
func describe(r *model.Resource, recs []Record) string {
	var parts []string
	for i, rec := range recs {
		var keys []string
		for _, k := range r.Keys {
			keys = append(keys, fmt.Sprintf("%s=%s", k, text(rec[k])))
		}
		parts = append(parts, fmt.Sprintf("#%d %s", i+1, strings.Join(keys, " ")))
	}
	if len(parts) == 0 {
		return "no records"
	}
	return strings.Join(parts, ", ")
}
