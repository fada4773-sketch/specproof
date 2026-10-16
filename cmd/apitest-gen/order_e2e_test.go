package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/gen/tree"
	"github.com/fada4773-sketch/specproof/internal/scenario"
	"github.com/fada4773-sketch/specproof/internal/testserver"
)

// requestLog serves a Starport and keeps every request it gets, in the
// order it got them ("GET /planets?page=1", relative to the base URL).
type requestLog struct {
	mu   sync.Mutex
	seen []string
}

func (l *requestLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seen)
}

func loggedStarport(t *testing.T, stage bool) (*requestLog, string) {
	t.Helper()
	first := 1
	if stage {
		first = 100
	}
	srv := testserver.NewStarport(starportToken, first)
	if stage {
		srv.Fill()
	}
	log := &requestLog{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.mu.Lock()
		log.seen = append(log.seen, r.Method+" "+strings.TrimPrefix(r.URL.RequestURI(), "/api"))
		log.mu.Unlock()
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	return log, hs.URL + "/api"
}

var pageData = regexp.MustCompile(`(?s)<script id="review-data" type="application/json">(.*?)</script>`)

// timelines reads the timelines of tree-review_gen.html.
func timelines(t *testing.T, page string) map[string][]tree.Entry {
	t.Helper()
	b, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	m := pageData.FindSubmatch(b)
	if m == nil {
		t.Fatalf("%s holds no data", page)
	}
	var data struct {
		Timelines map[string][]tree.Entry `json:"timelines"`
	}
	if err := json.Unmarshal(m[1], &data); err != nil {
		t.Fatal(err)
	}
	return data.Timelines
}

// sent is a line of a timeline as the server logs it.
func sent(e tree.Entry) string { return e.Method + " " + e.Target }

func stepIDs(list []tree.Entry) []string {
	out := make([]string, len(list))
	for i, e := range list {
		out[i] = e.Step
	}
	return out
}

// mdSteps are the steps of a timeline table of the Markdown review, in
// the order of its rows, after the line that starts it.
func mdSteps(md, start string) []string {
	at := strings.Index(md, start)
	if at < 0 {
		return nil
	}
	var out []string
	row := regexp.MustCompile(`^\| (\d+) \| ([^|]+) \|`)
	for _, l := range strings.Split(md[at:], "\n")[1:] {
		if strings.HasPrefix(l, "</details>") {
			break
		}
		if m := row.FindStringSubmatch(l); m != nil {
			out = append(out, strings.TrimSpace(m[2]))
		}
	}
	return out
}

// withoutProbes is what an instance got without the health check (GET /)
// and, after -update, without the second sending of each read: there a
// read comes as a pair, the step and its probe.
func withoutProbes(got []string, update bool) []string {
	var out []string
	for i := 0; i < len(got); i++ {
		g := got[i]
		if g == "GET /" {
			continue
		}
		out = append(out, g)
		if update && strings.HasPrefix(g, "GET ") && i+1 < len(got) && got[i+1] == g {
			i++
		}
	}
	return out
}

// isSubsequence reports whether every element of sub is in list, in the
// same order.
func isSubsequence(sub, list []string) bool {
	i := 0
	for _, x := range list {
		if i < len(sub) && sub[i] == x {
			i++
		}
	}
	return i == len(sub)
}

// TestTreeOrderEndToEnd follows the whole workflow, dry run to -update and
// a run after it, and checks at every step that the order of what the
// review says is the order of what happened: the dry run lists the nodes
// as the fetch reads them; the timeline of the fetch is what the stage
// got, request by request; the test runs its creates in the order of
// tree.yaml and deletes the other way round; building again without a
// fetch gives the same test; the run is what the empty instance got, in
// the order of the test; -update names the steps of the examples in that
// order; the Markdown lists the same steps in the same order as the page.
func TestTreeOrderEndToEnd(t *testing.T) {
	specPath, file := treeProject(t)
	dir := filepath.Dir(file)
	page := filepath.Join(dir, "tree-review_gen.html")
	md := func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "tree-review_gen.md"))
		return string(b)
	}
	if code, out, errOut := cli("tree", "-spec", specPath, "-file", file); code != 0 {
		t.Fatalf("propose: %d\n%s%s", code, out, errOut)
	}
	tr, err := tree.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	// the nodes in the order of the tree, as the fetch walks it
	var walk []string
	tr.Walk(func(n *tree.Node) {
		if !n.IsRequire && n.Enabled() {
			walk = append(walk, n.Name)
		}
	})

	// 1. dry run: nothing sent; one template per node, in the order of the tree
	stageLog, stage := loggedStarport(t, true)
	code, out, errOut := cli("tree", "-spec", specPath, "-file", file, "-base-url", stage, "-token", starportToken, "-dry-run")
	if code != 0 || len(stageLog.list()) != 0 {
		t.Fatalf("dry run: %d, sent %v\n%s%s", code, stageLog.list(), out, errOut)
	}
	_, plan, _ := strings.Cut(out, "the fetch would send")
	plan, _, _ = strings.Cut(plan, "\n\n")
	if n := strings.Count(plan, "\n  "); n != len(walk) {
		t.Errorf("dry run lists %d requests, the tree has %d nodes:\n%s", n, len(walk), out)
	}

	// 2. fetch: the timeline is what the stage got, in that order
	if code, out, errOut = cli("tree", "-spec", specPath, "-file", file, "-base-url", stage, "-token", starportToken); code != 0 {
		t.Fatalf("fetch: %d\n%s%s", code, out, errOut)
	}
	tl := timelines(t, page)
	fetch := tl[tree.ReviewFetch]
	var fetched []string
	for _, e := range fetch {
		fetched = append(fetched, sent(e))
	}
	if got := stageLog.list(); !slices.Equal(fetched, got) {
		t.Errorf("fetch timeline (%d) is not what the stage got (%d):\n%v\n%v", len(fetched), len(got), fetched, got)
	}
	// the first request of each node comes in the order of the tree
	var firsts []string
	for _, e := range fetch {
		name, _, _ := strings.Cut(e.Step, " under ")
		if slices.Contains(walk, name) && !slices.Contains(firsts, name) {
			firsts = append(firsts, name)
		}
	}
	if !isSubsequence(firsts, walk) {
		t.Errorf("the fetch reads the nodes %v, not in the order of the tree %v", firsts, walk)
	}

	// 3. the test: the timeline is the test file, step by step
	sc, err := scenario.Load(filepath.Join(dir, "tree-test_gen.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var planned []string
	for _, st := range sc.Steps {
		planned = append(planned, st.ID)
	}
	if got := stepIDs(tl[tree.ReviewScenario]); !slices.Equal(got, planned) {
		t.Errorf("test timeline is not the test file:\n%v\n%v", got, planned)
	}
	if got := mdSteps(md(), "<summary>Steps in the order they run ("); !slices.Equal(got, planned) {
		t.Errorf("the Markdown lists the test in another order:\n%v\n%v", got, planned)
	}
	// creates in the order of tree.yaml; every delete after the creates,
	// the records the other way round
	tr, _ = tree.Load(file)
	nodeOf := func(id string) string {
		rec, _, _ := strings.Cut(id, ".")
		return strings.TrimRight(rec, "0123456789")
	}
	var created, deleted []string
	lastCreate, firstDelete, place := -1, len(planned), -1
	for i, id := range planned {
		switch {
		case strings.HasSuffix(id, ".create"):
			created = append(created, strings.TrimSuffix(id, ".create"))
			lastCreate = i
			if p := tr.OrderIndex(nodeOf(id)); p >= 0 {
				if p < place {
					t.Errorf("%s is created after a node that comes later in order", id)
				}
				place = max(place, p)
			}
		case strings.Contains(id, ".delete") && !strings.Contains(id, ".read"):
			deleted = append(deleted, strings.SplitN(id, ".", 2)[0])
			firstDelete = min(firstDelete, i)
		}
	}
	// no record is created twice: no two creates send the same
	sameAs := map[string]string{}
	for _, st := range sc.Steps {
		if !strings.HasSuffix(st.ID, ".create") {
			continue
		}
		b, _ := json.Marshal([]any{st.Call, st.Path, st.Body})
		if other, dup := sameAs[string(b)]; dup {
			t.Errorf("%s and %s create the same: %s", other, st.ID, b)
		}
		sameAs[string(b)] = st.ID
	}
	if len(created) == 0 || len(deleted) == 0 || firstDelete < lastCreate {
		t.Errorf("creates %v, deletes %v: every delete after the last create", created, deleted)
	}
	slices.Reverse(created)
	if !isSubsequence(deleted, created) {
		t.Errorf("deletes %v are not the creates the other way round %v", deleted, created)
	}

	// 4. built again without a fetch: the same test, the fetch kept
	before, _ := os.ReadFile(filepath.Join(dir, "tree-test_gen.yaml"))
	if code, out, errOut = cli("tree", "-spec", specPath, "-file", file); code != 0 {
		t.Fatalf("rebuild: %d\n%s%s", code, out, errOut)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "tree-test_gen.yaml")); string(after) != string(before) {
		t.Error("building again gives another test")
	}
	if again := timelines(t, page); !slices.Equal(stepIDs(again[tree.ReviewFetch]), stepIDs(fetch)) || !slices.Equal(stepIDs(again[tree.ReviewScenario]), planned) {
		t.Error("building again lost or changed the timelines of the fetch or the test")
	}

	// 5. run -update against an empty instance: the timeline is what the
	// instance got, in the order of the test; -update names its examples in
	// that order
	emptyLog, empty := loggedStarport(t, false)
	if code, out, errOut = cli("tree", "run", "-spec", specPath, "-file", file, "-base-url", empty, "-token", starportToken, "-update"); code != 0 {
		t.Fatalf("run -update: %d\n%s%s", code, out, errOut)
	}
	tl = timelines(t, page)
	run := tl[tree.ReviewRun]
	var ran []string
	for _, e := range run {
		ran = append(ran, sent(e))
		if e.Failed {
			t.Errorf("%s did not pass: %s", e.Step, e.Result)
		}
	}
	// what the instance got: the health check first, and with -update each
	// read sent twice (the second finds the fields that change by
	// themselves); the rest is the timeline
	if got := withoutProbes(emptyLog.list(), true); !slices.Equal(ran, got) {
		t.Errorf("run timeline (%d) is not what the instance got (%d):\n%v\n%v", len(ran), len(got), ran, got)
	}
	if !strings.Contains(md(), "every read was sent twice") {
		t.Error("the review does not say that -update reads twice")
	}
	if got := stepIDs(run); !isSubsequence(got, planned) || len(got) == 0 {
		t.Errorf("the run is not in the order of the test:\n%v\n%v", got, planned)
	}
	if got := mdSteps(md(), "<summary>Steps in the order they ran ("); !slices.Equal(got, stepIDs(run)) {
		t.Errorf("the Markdown lists the run in another order:\n%v\n%v", got, stepIDs(run))
	}
	update := stepIDs(tl[tree.ReviewUpdate])
	if len(update) == 0 || !isSubsequence(update, stepIDs(run)) {
		t.Errorf("-update: examples %v not in the order of the run %v", update, stepIDs(run))
	}
	if got := mdSteps(md(), "<summary>Examples written, in the order their steps ran ("); !slices.Equal(got, update) {
		t.Errorf("the Markdown lists the examples in another order:\n%v\n%v", got, update)
	}
	if !slices.Equal(stepIDs(tl[tree.ReviewFetch]), stepIDs(fetch)) || !slices.Equal(stepIDs(tl[tree.ReviewScenario]), planned) {
		t.Error("the run lost the timelines of the fetch or the test")
	}

	// 6. a run after it compares with the answers: the same order, the
	// examples of -update kept
	againLog, again := loggedStarport(t, false)
	if code, out, errOut = cli("tree", "run", "-spec", specPath, "-file", file, "-base-url", again, "-token", starportToken); code != 0 {
		t.Fatalf("run: %d\n%s%s", code, out, errOut)
	}
	tl = timelines(t, page)
	if !slices.Equal(stepIDs(tl[tree.ReviewRun]), stepIDs(run)) || !slices.Equal(withoutProbes(againLog.list(), false), ran) {
		t.Errorf("a second run in another order:\n%v\n%v", stepIDs(tl[tree.ReviewRun]), stepIDs(run))
	}
	if !slices.Equal(stepIDs(tl[tree.ReviewUpdate]), update) {
		t.Error("a run without -update lost the examples of -update")
	}
}
