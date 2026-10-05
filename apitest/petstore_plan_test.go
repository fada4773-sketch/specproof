package apitest

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/bind"
	"github.com/fada4773-sketch/specproof/internal/cases"
	"github.com/fada4773-sketch/specproof/internal/deviations"
	"github.com/fada4773-sketch/specproof/internal/exec"
	"github.com/fada4773-sketch/specproof/internal/params"
	"github.com/fada4773-sketch/specproof/internal/plan"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

// petstoreSpec is the frozen Petstore spec of the reference test.
const petstoreSpec = "../examples/petstore/spec/openapi.yaml"

// TestPetstoreSpecLoadsAndPlans checks the real Petstore spec without a
// container (work packages 1.12 and 2.12): it loads, every path parameter
// gets a binding from the heuristic, and only the cases that cannot be
// built for a documented reason remain NOT_BUILDABLE. The planned cases are
// logged for review with -v.
func TestPetstoreSpecLoadsAndPlans(t *testing.T) {
	s, err := spec.Load(t.Context(), petstoreSpec)
	if err != nil {
		t.Fatalf("Petstore spec does not load: %v", err)
	}
	if _, err := deviations.Load("../examples/petstore/apitest_deviations.yaml"); err != nil {
		t.Errorf("deviations file of the example: %v", err)
	}
	if got := len(s.Ops); got != 19 {
		t.Errorf("operations: got %d, want 19 (independent count of the paths in openapi.yaml)", got)
	}
	set, err := bind.Resolve(s)
	if err != nil {
		t.Fatal(err)
	}
	all, err := cases.Build(s, cases.Options{})
	if err != nil {
		t.Fatalf("planning failed: %v", err)
	}
	p, err := plan.Build(all, func(*cases.Case) bool { return true }, set, plan.Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Every path parameter is bound to the POST of its collection.
	wantBindings := map[string]string{
		"getPetById.petId":        "addPet /id",
		"updatePetWithForm.petId": "addPet /id",
		"deletePet.petId":         "addPet /id",
		"uploadFile.petId":        "addPet /id",
		"getOrderById.orderId":    "placeOrder /id",
		"deleteOrder.orderId":     "placeOrder /id",
		"getUserByName.username":  "createUser /username",
		"updateUser.username":     "createUser /username",
		"deleteUser.username":     "createUser /username",
	}
	got := map[string]string{}
	for _, op := range s.Ops {
		for _, prm := range op.Params {
			if prm.In != openapi3.ParameterInPath {
				continue
			}
			if b := set.For(op, prm); b != nil {
				got[op.ID+"."+prm.Name] = b.Producer.ID + " " + b.Source.Pointer
			} else {
				got[op.ID+"."+prm.Name] = "(none)"
			}
		}
	}
	if !reflect.DeepEqual(got, wantBindings) {
		t.Errorf("bindings:\n got  %v\n want %v", got, wantBindings)
	}

	var b strings.Builder
	var notBuildable []string
	for _, c := range p.Cases() {
		state := "buildable"
		_, err := exec.Prepare(c, exec.Input{
			Base: "http://127.0.0.1:8080/api/v3",
			Params: params.Inputs{Binding: func(prm *openapi3.Parameter) (any, bool) {
				// At run time the producer provides the value.
				return "1", set.For(c.Op, prm) != nil
			}},
			Auth:  exec.ResolveAuth(c.Op.Security, s.Doc.Components.SecuritySchemes),
			Token: "t",
		})
		var nb *exec.NotBuildableError
		switch {
		case errors.As(err, &nb):
			state = "NOT_BUILDABLE: " + nb.Reason
			notBuildable = append(notBuildable, c.Op.ID)
		case err != nil:
			t.Errorf("%s: %v", c.Name, err)
		}
		fmt.Fprintf(&b, "%-40s expect %-4s example=%-5v %s\n", c.Name, c.Expect, c.Expect.HasExample, state)
	}
	t.Logf("%d cases in execution order:\n%s", len(p.Cases()), b.String())
	for _, f := range append(s.Findings, set.Findings...) {
		t.Logf("spec finding %s: %s", f.Where, f.Message)
	}
	sort.Strings(notBuildable)
	// uploadFile sends application/octet-stream, findPetsByTags has a
	// required parameter without any example (see examples/petstore/README.md).
	if want := []string{"findPetsByTags", "uploadFile"}; !reflect.DeepEqual(notBuildable, want) {
		t.Errorf("NOT_BUILDABLE: got %v, want %v", notBuildable, want)
	}
}
