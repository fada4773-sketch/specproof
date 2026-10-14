package tree

import (
	"fmt"
	"sort"
	"strings"
)

// ReviewInput is what the review shows.
type ReviewInput struct {
	Spec, SpecVersion string
	Data              *Data
	Built             *Built
	Changes           []Change
	Stale             []string
	// Unset are the entries of params without a value.
	Unset []string
}

// Review renders the review list (tree-review_gen.md): what was fetched,
// what the scenario creates, what the instance must hold, and every note.
func Review(in ReviewInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review: tree\n\n")
	if in.Spec != "" {
		fmt.Fprintf(&b, "Spec %s %s", in.Spec, in.SpecVersion)
	}
	if in.Data != nil {
		fmt.Fprintf(&b, " · %d records from %s (%d requests)", len(in.Data.Records), in.Data.Source, in.Data.Requests)
	}
	b.WriteString("\n")
	if in.Built != nil && in.Built.Scenario != nil {
		fmt.Fprintf(&b, "Scenario: %d steps, %d records created.\n", len(in.Built.Scenario.Steps), len(in.Built.Created))
	}
	if len(in.Changes) > 0 {
		fmt.Fprintf(&b, "\n## Changes of tree.yaml (%d)\n\n| Where | What |\n|---|---|\n", len(in.Changes))
		for _, c := range in.Changes {
			fmt.Fprintf(&b, "| %s | %s |\n", cell(c.Where), cell(c.What))
		}
	}
	if len(in.Unset) > 0 {
		fmt.Fprintf(&b, "\n## Params without value (%d)\n\nNo record of the tree gives them; set them under params at the top of tree.yaml.\nThe nodes that use them were left out: %s\n", len(in.Unset), strings.Join(in.Unset, ", "))
	}
	if in.Built != nil && len(in.Built.Seeds) > 0 {
		fmt.Fprintf(&b, "\n## Seed: the instance must hold these records (%d)\n\n", len(in.Built.Seeds))
		b.WriteString("The scenario reads them by the values of the stage and creates none of them\n(create: none, or no create in the spec). Put them into the seed of the test instance.\n\n")
		b.WriteString("| Record | Request | Key |\n|---|---|---|\n")
		for _, r := range in.Built.Seeds {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", r.ID, cell(r.Request), cell(KeyText(r.Value, keyOf(r.Node))))
		}
	}
	if in.Data != nil && len(in.Data.Notes) > 0 {
		fmt.Fprintf(&b, "\n## Fetch (%d)\n\n", len(in.Data.Notes))
		for _, n := range in.Data.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	if len(in.Stale) > 0 {
		fmt.Fprintf(&b, "\n## Records of nodes the tree no longer has (%d)\n\nFetch again with -base-url: %s\n", len(in.Stale), strings.Join(in.Stale, ", "))
	}
	if in.Built != nil && len(in.Built.Notes) > 0 {
		notes := append([]Note(nil), in.Built.Notes...)
		sort.SliceStable(notes, func(i, j int) bool { return notes[i].Step < notes[j].Step })
		fmt.Fprintf(&b, "\n## Decisions and gaps (%d)\n\n| Step | What | Why |\n|---|---|---|\n", len(notes))
		for _, n := range notes {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", cell(n.Step), cell(n.What), cell(n.Why))
		}
	}
	return b.String()
}

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}
