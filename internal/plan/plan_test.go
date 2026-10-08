package plan

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

func setup(t *testing.T, file string) ([]*cases.Case, *bind.Set) {
	t.Helper()
	s, err := spec.Load(t.Context(), "../../testdata/specs/"+file)
	if err != nil {
		t.Fatal(err)
	}
	all, err := cases.Build(s, cases.Options{})
	if err != nil {
		t.Fatal(err)
	}
	set, err := bind.Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	return all, set
}

func all(*cases.Case) bool { return true }

func names(list []*cases.Case) []string {
	var out []string
	for _, c := range list {
		out = append(out, c.Name)
	}
	return out
}

func TestOrderGroupsAndDeferredDeletes(t *testing.T) {
	list, set := setup(t, "plan.yaml")
	p, err := Build(list, all, set, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Author", "Book", "Review", "Stats"}; !reflect.DeepEqual(p.Groups, want) {
		t.Errorf("groups: got %v, want %v", p.Groups, want)
	}
	want := []string{
		"Author/createAuthor/default",
		"Author/getAuthor/default",
		"Book/createBook/default",
		"Book/getBook/default",
		"Review/createReview/default",
		// getReview is an item GET (rank 1) and runs before the list GET
		// (rank 2); x-apitest-order only orders cases of the same rank.
		"Review/getReview/default",
		"Review/listReviews/default",
		"Review/deleteReview/default",
		"Stats/getStats/default",
		"Book/deleteBook/default",
		"Author/deleteAuthor/default",
	}
	if got := names(p.Cases()); !reflect.DeepEqual(got, want) {
		t.Errorf("order:\n got  %v\n want %v", got, want)
	}
	// Segments: BeforeGroup/AfterGroup are called once per group.
	first, last := map[string]int{}, map[string]int{}
	for _, s := range p.Segments {
		if s.First {
			first[s.Group]++
		}
		if s.Last {
			last[s.Group]++
		}
	}
	for _, g := range p.Groups {
		if first[g] != 1 || last[g] != 1 {
			t.Errorf("group %s: %d first and %d last segments", g, first[g], last[g])
		}
	}
}

func TestPreconditions(t *testing.T) {
	list, set := setup(t, "plan.yaml")
	onlyGetReview := func(c *cases.Case) bool { return c.Op.ID == "getReview" }
	p, err := Build(list, onlyGetReview, set, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Author/createAuthor/default", "Book/createBook/default", "Review/createReview/default", "Review/getReview/default"}
	if got := names(p.Cases()); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, c := range p.Cases() {
		if p.Precondition[c] != (c.Op.ID != "getReview") {
			t.Errorf("%s: precondition %v", c.Name, p.Precondition[c])
		}
	}
}

func TestTPL1_22_Cycle(t *testing.T) {
	list, set := setup(t, "cycle.yaml")
	_, err := Build(list, all, set, Options{})
	if err == nil || !strings.Contains(err.Error(), "A → B → A") {
		t.Fatalf("got %v, want cycle A → B → A", err)
	}
}

func TestTagsContradictGraph(t *testing.T) {
	list, set := setup(t, "plan.yaml")
	_, err := Build(list, all, set, Options{Tags: []string{"Review", "Book", "Author"}})
	if err == nil || !strings.Contains(err.Error(), `lists "Book" before "Author"`) {
		t.Fatalf("got %v", err)
	}
	p, err := Build(list, all, set, Options{Tags: []string{"Stats", "Author"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Groups[0] != "Stats" {
		t.Errorf("Tags order must be used where compatible: %v", p.Groups)
	}
}

func TestDeps(t *testing.T) {
	list, set := setup(t, "plan.yaml")
	p, err := Build(list, all, set, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range p.Cases() {
		if c.Op.ID == "getBook" {
			if got := names(p.Deps[c]); !reflect.DeepEqual(got, []string{"Book/createBook/default"}) {
				t.Errorf("deps of getBook: %v", got)
			}
		}
	}
}

// LastInTag puts operations after all other cases of their group, whatever
// their method, in the order listed; the DELETEs still follow, and a case
// that needs a value from a listed operation still runs after it.
func TestLastInTag(t *testing.T) {
	list, set := setup(t, "plan.yaml")
	review := func(last ...string) []string {
		t.Helper()
		p, err := Build(list, all, set, Options{LastInTag: last})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range p.Cases() {
			if c.Group == "Review" {
				out = append(out, c.Op.ID)
			}
		}
		return out
	}
	for _, c := range []struct {
		last []string
		want string
	}{
		{nil, "createReview getReview listReviews deleteReview"},
		{[]string{"getReview"}, "createReview listReviews getReview deleteReview"},
		{[]string{"getReview", "listReviews"}, "createReview getReview listReviews deleteReview"},
		{[]string{"listReviews", "GET /reviews/{reviewId}"}, "createReview listReviews getReview deleteReview"},
		// getReview takes its id from createReview, so it waits for it
		{[]string{"createReview"}, "listReviews createReview getReview deleteReview"},
		{[]string{"deleteReview"}, "createReview getReview listReviews deleteReview"},
	} {
		if got := strings.Join(review(c.last...), " "); got != c.want {
			t.Errorf("LastInTag %v:\n got  %s\n want %s", c.last, got, c.want)
		}
	}
	s, err := spec.Load(t.Context(), "../../testdata/specs/plan.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckLastInTag(s, []string{"getReview", "archiveMoon"}); err == nil || !strings.Contains(err.Error(), `"archiveMoon" from LastInTag does not exist`) {
		t.Errorf("unknown operation: %v", err)
	}
}
