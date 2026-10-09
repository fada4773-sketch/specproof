//go:build integration

// Package petstore is the reference integration test of apitest: the
// public Swagger Petstore v3 API runs locally in a container
// and is tested against its own frozen spec. It uses only the public API of
// apitest, exactly like a project that imports the library.
package petstore

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gopkg.in/yaml.v3"

	"github.com/fada4773-sketch/specproof/apitest"
)

// petstoreImage is pinned by tag and digest so runs are reproducible. It must
// match spec/SOURCE.md.
const petstoreImage = "swaggerapi/petstore3:1.0.28@sha256:dc991f49b62fed5ece2816a1b22aeeeb49278cdf56fb87dc9d69e30541d6cd6d"

const specPath = "spec/openapi.yaml"

// env is set up once per "go test" run in TestMain. Every run gets a fresh
// container, so every run starts from the same state.
var env struct {
	baseURL string // e.g. http://localhost:32768/api/v3
	skip    string // reason if no container runtime is available
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, petstoreImage,
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForHTTP("/api/v3/openapi.json").WithPort("8080/tcp").WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		env.skip = fmt.Sprintf("cannot start Petstore container (is Docker or Podman running? see README.md): %v", err)
	} else {
		endpoint, err := ctr.PortEndpoint(ctx, "8080/tcp", "http")
		if err != nil {
			env.skip = fmt.Sprintf("port of the Petstore container unknown: %v", err)
		}
		env.baseURL = endpoint + "/api/v3"
	}

	code := m.Run()

	if ctr != nil {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintf(os.Stderr, "terminate Petstore container: %v\n", err)
		}
	}
	os.Exit(code)
}

func requireContainer(t *testing.T) {
	t.Helper()
	if env.skip != "" {
		t.Skip(env.skip)
	}
}

// TestPetstore runs apitest against the Petstore.
//
// covers: TP-L3-01 TP-L3-02 TP-L3-06
// (run; strict run with APITEST_STRICT=true after classifying the findings;
// the random token must not appear in the reports).
//
// Every real difference between the Petstore and its spec fails this test
// until it is classified in findings.md and, if it is a deviation of the API
// or the spec, accepted in apitest_deviations.yaml (TP-L3-02). Set
// APITEST_STRICT=true for the acceptance run.
func TestPetstore(t *testing.T) {
	requireContainer(t)
	token := randomToken(t) // TP-L3-06: must not appear in any output

	res := apitest.Run(t, apitest.Config{
		SpecPath:   specPath,
		BaseURL:    env.baseURL,
		HealthPath: "/openapi.json",
		// The Petstore does not check tokens, but its spec declares
		// petstore_auth (oauth2) and api_key. apitest sends this token.
		Token: apitest.StaticToken(token),
		// Path parameters (petId, orderId, username) come from the responses
		// of addPet, placeOrder and createUser through the heuristic; the
		// report lists them as spec findings.
		DeviationsPath: "apitest_deviations.yaml",
		ReportPath:     cmp.Or(os.Getenv("APITEST_PETSTORE_REPORT"), "apitest-report/TestPetstore.md"),
		Strict:         os.Getenv("APITEST_STRICT") == "true",
		ReportJSON:     true,
		RequestTimeout: 10 * time.Second,
	})

	t.Logf("Report: %s", res.Report)
	for _, p := range []string{res.Report, strings.TrimSuffix(res.Report, ".md") + ".json"} {
		if b, err := os.ReadFile(p); err != nil {
			t.Errorf("report missing: %v", err)
		} else if strings.Contains(string(b), token) {
			t.Errorf("the token appears in %s (NFR-06)", p)
		}
	}
	t.Logf("operations with request: %d of %d, statuses: %v", res.Summary.OperationsCovered, res.Summary.Operations, res.Summary.Counts)

	// The library itself must work: every case reached the server or has a
	// documented reason why not. ERROR means network or timeout problems.
	for _, c := range res.Cases {
		if c.Status == apitest.StatusError {
			t.Errorf("%s: technical error instead of a check result: %s", c.Name, c.Message)
		}
	}
	if res.Summary.Operations != 19 {
		t.Errorf("operations: %d, want 19 (spec/openapi.yaml)", res.Summary.Operations)
	}
}

// TestServedSpecMatchesFrozenSpec checks work package 2.12: the container
// must serve the same operations as the frozen spec in spec/openapi.yaml.
// If this fails, the image and the frozen spec are out of sync and
// spec/SOURCE.md must be updated.
func TestServedSpecMatchesFrozenSpec(t *testing.T) {
	requireContainer(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, env.baseURL+"/openapi.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	served, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var servedDoc map[string]any
	if err := json.Unmarshal(served, &servedDoc); err != nil {
		t.Fatalf("served spec is not JSON: %v", err)
	}

	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var frozenDoc map[string]any
	if err := yaml.Unmarshal(raw, &frozenDoc); err != nil {
		t.Fatal(err)
	}

	got, want := operations(servedDoc), operations(frozenDoc)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("operations differ.\nserved:\n  %s\nfrozen:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if v, _ := servedDoc["info"].(map[string]any)["version"].(string); v != "1.0.28" {
		t.Errorf("info.version of the served spec: %q, want 1.0.28", v)
	}
}

// operations lists "METHOD path operationId" for every operation.
func operations(doc map[string]any) []string {
	var out []string
	paths, _ := doc["paths"].(map[string]any)
	for p, item := range paths {
		ops, _ := item.(map[string]any)
		for method, op := range ops {
			o, ok := op.(map[string]any)
			if !ok {
				continue
			}
			id, _ := o["operationId"].(string)
			out = append(out, fmt.Sprintf("%s %s %s", strings.ToUpper(method), p, id))
		}
	}
	sort.Strings(out)
	return out
}

func randomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// TestPetstoreRunSelectsPreconditions runs "go test -run" on a single case
// in a subprocess with its own container: only that case runs as a subtest,
// the case that creates the pet runs as a precondition.
//
// covers: TP-L3-05
func TestPetstoreRunSelectsPreconditions(t *testing.T) {
	requireContainer(t)
	if os.Getenv("APITEST_PETSTORE_REPORT") != "" {
		t.Skip("running inside the subprocess")
	}
	report := filepath.Join(t.TempDir(), "report.md")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.v", "-test.run=^TestPetstore$/pet/getPetById/default")
	cmd.Env = append(os.Environ(), "APITEST_PETSTORE_REPORT="+report)
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "--- PASS: TestPetstore/pet/getPetById/default") {
		t.Fatalf("subprocess: %v\n%s", err, out)
	}
	var ran []string
	for _, line := range strings.Split(string(out), "\n") {
		if name, ok := strings.CutPrefix(line, "=== RUN   "); ok {
			ran = append(ran, name)
		}
	}
	if want := []string{"TestPetstore", "TestPetstore/pet/getPetById/default"}; strings.Join(ran, ",") != strings.Join(want, ",") {
		t.Errorf("subtests: %v, want %v", ran, want)
	}
	raw, err := os.ReadFile(strings.TrimSuffix(report, ".md") + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Cases []struct {
			Name         string `json:"name"`
			Precondition bool   `json:"precondition"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range res.Cases {
		got[c.Name] = c.Precondition
	}
	if len(got) != 2 || !got["pet/addPet/default"] || got["pet/getPetById/default"] {
		t.Errorf("cases in the report: %v, want addPet as precondition and getPetById", got)
	}
}
