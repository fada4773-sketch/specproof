package selection

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// names are subtests of TestHelperNames, written the way apitest names cases.
var names = []string{
	"Author/createAuthor/valid-author",
	"Author/getAuthor/default",
	"Book/getBook/default",
	"Book/updateBook/default",
	"Book/updateBook/rename",
	"Client_Scopes/listScopes/default",
	"untagged/POST__x_{id}/default",
}

// TestHelperNames is started as a subprocess by TestTPL1_29_MatchesGoTest.
func TestHelperNames(t *testing.T) {
	if os.Getenv("SELECTION_HELPER") == "" {
		t.Skip("helper for TestTPL1_29_MatchesGoTest")
	}
	for _, n := range names {
		t.Run(n, func(t *testing.T) { t.Log("RAN " + t.Name()) })
	}
}

// TestTPL1_29_MatchesGoTest compares Filter.Selected with what "go test"
// really runs for the same -run and -skip patterns.
func TestTPL1_29_MatchesGoTest(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	tests := []struct{ run, skip string }{
		{"TestHelperNames", ""},
		{"TestHelperNames/Book", ""},
		{"TestHelperNames/Book/updateBook/default", ""},
		{"TestHelperNames/Book/update", ""},
		{"TestHelperNames/^Book$/getBook", ""},
		{"TestHelperNames/(Book|Author)/get", ""},
		{"Nope|TestHelperNames/Author", ""},
		{"TestHelperNames/Client Scopes", ""},
		{"TestHelperNames/[BA]", ""},
		{"TestHelperNames//default", ""},
		{"TestHelperNames/untagged/POST__x_\\{id\\}", ""},
		{"TestHelperNames/Book/updateBook/default/deeper", ""},
		{"TestHelperNames", "TestHelperNames/Book"},
		{"TestHelperNames", "TestHelperNames/Book/updateBook/default"},
		{"TestHelperNames", "TestHelperNames/Book/updateBook/default/deeper"},
		{"TestHelperNames", "/getAuthor"},
		{"TestHelperNames", "TestHelperNames/Book|TestHelperNames/Author"},
	}
	if all := goTestRuns(t, "TestHelperNames", ""); len(all) != len(names) {
		t.Fatalf("helper subprocess ran %d of %d subtests: %v", len(all), len(names), all)
	}
	for _, tc := range tests {
		t.Run(tc.run+" -skip "+tc.skip, func(t *testing.T) {
			want := goTestRuns(t, tc.run, tc.skip)
			f, err := New(tc.run, tc.skip)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, n := range names {
				full := "TestHelperNames/" + n
				if f.Selected(full) {
					got = append(got, full)
				}
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("run %q skip %q:\n got  %v\n want %v (go test)", tc.run, tc.skip, got, want)
			}
		})
	}
}

func goTestRuns(t *testing.T, run, skip string) []string {
	t.Helper()
	args := []string{"-test.v", "-test.run=" + run}
	if skip != "" {
		args = append(args, "-test.skip="+skip)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], args...)
	cmd.Env = append(os.Environ(), "SELECTION_HELPER=1")
	out, _ := cmd.CombinedOutput()
	var ran []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "RAN "); i >= 0 {
			ran = append(ran, strings.TrimSpace(line[i+4:]))
		}
	}
	sort.Strings(ran)
	return ran
}

func TestEmptyPatternsSelectEverything(t *testing.T) {
	f, err := New("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Selected("TestX/a/b/c") {
		t.Error("empty -run must select everything")
	}
	if _, err := New("(", ""); err == nil {
		t.Error("invalid regexp must be reported")
	}
}
