package record

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/gen/scenario"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// CheckOrder compares the record file with the order apitest runs the
// spec in (s is the spec as written): the answers were recorded in the
// order of the file, so apitest sees the same data only if it runs the
// cases in that order too. It also reports entries apitest does not run
// and cases apitest runs that have no entry.
func CheckOrder(s *spec.Spec, f *File, run defaults.Run) ([]Note, error) {
	order, _, err := scenario.Order(s, run)
	if err != nil {
		return nil, orderError(s, run, err)
	}
	index := map[string]int{}
	byIndex := map[int]*cases.Case{}
	for i, c := range order {
		if c.Kind == cases.Positive && c.Skip == "" {
			index[c.Op.ID+"/"+c.Example] = i
			byIndex[i] = c
		}
	}
	var notes []Note
	inFile := map[string]bool{}
	maxIdx, maxStep := -1, (*Step)(nil)
	for _, st := range f.Steps {
		op := lookup(s, st.Key)
		if op == nil {
			continue
		}
		key := op.ID + "/" + st.Example()
		inFile[key] = true
		i, ok := index[key]
		if !ok {
			notes = append(notes, Note{CodeNotRun, st.where(), "apitest does not run this case (ExcludeOps, IncludeOps, Tags or x-apitest-skip); its answer still shapes the data of the entries after it"})
			continue
		}
		if i < maxIdx {
			notes = append(notes, Note{CodeOrder, st.where(), fmt.Sprintf("apitest runs it before %s (line %d), so it sees other data than recorded; move the entry above that one, or change the order of apitest (\"$apitest\": MethodOrder, LastInTag, Tags, DeleteLast)",
				maxStep, maxStep.Line)})
			continue
		}
		maxIdx, maxStep = i, st
	}
	missing := 0
	first := ""
	for _, c := range order {
		if c.Kind != cases.Positive || c.Skip != "" || inFile[c.Op.ID+"/"+c.Example] {
			continue
		}
		if missing == 0 {
			first = c.Name
		}
		missing++
	}
	if missing > 0 {
		notes = append(notes, Note{CodeNotInFile, "record file", fmt.Sprintf("apitest runs %d cases the file has no entry for (first: %s); they keep the examples of the spec. \"apitest-gen record -analyse\" adds them", missing, first)})
	}
	return notes, nil
}

// orderError explains an order apitest refuses: the bindings that make a
// tag run before one "$apitest".Tags lists earlier.
func orderError(s *spec.Spec, run defaults.Run, err error) error {
	set, berr := bind.Resolve(s)
	if berr != nil {
		return err
	}
	var lines []string
	for _, op := range s.Ops {
		for _, b := range set.Of(op) {
			from, to := slices.Index(run.Tags, b.Producer.Group()), slices.Index(run.Tags, op.Group())
			if from >= 0 && to >= 0 && to < from {
				lines = append(lines, fmt.Sprintf("%s (%s) takes {%s} from %s (%s), %s binding", op.ID, op.Group(), b.Param.Name, b.Producer.ID, b.Producer.Group(), b.Kind))
			}
		}
	}
	if len(lines) == 0 {
		return err
	}
	return fmt.Errorf("%w\n  \"$apitest\".Tags of the defaults file is that order; the bindings behind it (the tag that provides a value runs first):\n    %s\n  Put those tags in that order in \"$apitest\".Tags and in Config.Tags of the test; if a binding is wrong, declare the right one with x-apitest-bind",
		err, strings.Join(lines, "\n    "))
}
