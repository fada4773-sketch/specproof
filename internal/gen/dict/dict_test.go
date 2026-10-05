package dict

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/fada4773-sketch/specproof/internal/spec"
)

func load(t *testing.T) *spec.Spec {
	t.Helper()
	s, err := spec.Load(t.Context(), "../../../testdata/gen/shop.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func codes(notes []Note) map[string][]string {
	m := map[string][]string{}
	for _, n := range notes {
		m[n.Code] = append(m[n.Code], n.Where)
	}
	return m
}

func TestBuildStructure(t *testing.T) {
	d, notes, st := Build(load(t), nil, Options{Seed: 1})
	garden := d.Schemas["Garden"]
	if garden == nil || garden.Type != "object" {
		t.Fatalf("Garden: %+v", garden)
	}
	// a $ref plus extensions in allOf stays a reference
	if m := garden.Properties["Manager"]; m == nil || m.Ref != "Person" || m.Properties != nil {
		t.Errorf("Manager: %+v", m)
	}
	// arrays of DTOs reference the item DTO
	if items := d.Schemas["Order"].Properties["Items"]; items == nil || items.Items == nil || items.Items.Ref != "OrderItem" {
		t.Errorf("Order.Items: %+v", items)
	}
	// an alias stays an alias
	if a := d.Schemas["GardenAlias"]; a == nil || a.Ref != "Garden" {
		t.Errorf("GardenAlias: %+v", a)
	}
	// an inline object keeps its own properties
	if city := garden.Properties["Address"].Properties["City"]; city == nil || city.Value == nil {
		t.Errorf("Address.City: %+v", city)
	}
	// free objects get {} or, with typed additionalProperties, one entry
	if v, ok := garden.Properties["Settings"].Value.(map[string]any); !ok || len(v) != 0 {
		t.Errorf("Settings: %#v", garden.Properties["Settings"].Value)
	}
	if v, ok := garden.Properties["Labels"].Value.(map[string]any); !ok || len(v) != 1 {
		t.Errorf("Labels: %#v", garden.Properties["Labels"].Value)
	}
	if !garden.Properties["GardenCode"].Required || garden.Properties["Email"].Required {
		t.Error("required flags are wrong")
	}
	if st.DTOs != 6 || st.Parameters != 2 {
		t.Errorf("stats: %+v", st)
	}

	c := codes(notes)
	for code, where := range map[string]string{
		CodePatternPending: "schemas.Garden.Address.Impossible",
		CodeNoValue:        "schemas.Garden.Sealed",
		CodeTypeConflict:   "schemas.Garden.Kind",
		CodeParamConflict:  "parameters.path.gardenCode",
	} {
		if !contains(c[code], where) {
			t.Errorf("%s missing for %s: %v", code, where, c[code])
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestBuildValuesFitAndAreConsistent(t *testing.T) {
	s := load(t)
	d, _, _ := Build(s, nil, Options{Seed: 1})
	garden := d.Schemas["Garden"].Properties
	order := d.Schemas["Order"].Properties

	// the same field name gets the same value across DTOs and parameters
	if garden["GardenCode"].Value != order["GardenCode"].Value || d.Parameters["path.gardenCode"].Value != garden["GardenCode"].Value {
		t.Errorf("GardenCode values differ: %v %v %v", garden["GardenCode"].Value, order["GardenCode"].Value, d.Parameters["path.gardenCode"].Value)
	}
	// constraints are met
	if name, _ := garden["Name"].Value.(string); len(name) < 3 || len(name) > 20 {
		t.Errorf("Name %q violates its length", name)
	}
	if email, _ := garden["Email"].Value.(string); !strings.Contains(email, "@") {
		t.Errorf("Email %q", email)
	}
	tags, _ := garden["Tags"].Value.([]any)
	if len(tags) < 2 || tags[0] == tags[1] {
		t.Errorf("Tags %v must have two unique entries", tags)
	}
	q, _ := order["Quantity"].Value.(json.Number)
	if n, err := q.Int64(); err != nil || n <= 10 || n%5 != 0 {
		t.Errorf("Quantity %v must be > 10 and a multiple of 5", order["Quantity"].Value)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	s := load(t)
	a, _, _ := Build(s, nil, Options{Seed: 7})
	b, _, _ := Build(s, nil, Options{Seed: 7})
	if !reflect.DeepEqual(a, b) {
		t.Error("two builds with the same seed differ")
	}
	c, _, _ := Build(s, nil, Options{Seed: 8})
	if reflect.DeepEqual(a, c) {
		t.Error("another seed gives the same values")
	}
}

func TestBuildKeepsValues(t *testing.T) {
	s := load(t)
	old, _, _ := Build(s, nil, Options{Seed: 1})
	old.Schemas["Garden"].Properties["Name"].Value = "Hand made" // fits the schema
	old.Schemas["Garden"].Properties["Rating"].Value = 9         // violates maximum 5
	old.Schemas["Gone"] = &Node{Type: "object"}

	d, notes, st := Build(s, old, Options{Seed: 99})
	if v := d.Schemas["Garden"].Properties["Name"].Value; v != "Hand made" {
		t.Errorf("a valid value was replaced: %v", v)
	}
	if v := d.Schemas["Garden"].Properties["Rating"].Value; v != 9 {
		t.Errorf("an invalid value must be kept without -repair: %v", v)
	}
	c := codes(notes)
	if !contains(c[CodeValueInvalid], "schemas.Garden.Rating") {
		t.Errorf("VALUE_INVALID missing: %v", c[CodeValueInvalid])
	}
	if !contains(c[CodeRemoved], "schemas.Gone") {
		t.Errorf("REMOVED missing: %v", c[CodeRemoved])
	}
	if st.New != 0 {
		t.Errorf("%d new values although every field had one", st.New)
	}
	// a hand-made value also reaches other fields of the same name
	if v := d.Schemas["Person"].Properties["Name"].Value; v == nil {
		t.Error("Person.Name has no value")
	}

	repaired, _, st := Build(s, old, Options{Seed: 99, Repair: true})
	if v := repaired.Schemas["Garden"].Properties["Rating"].Value; v == 9 || st.Repaired != 1 {
		t.Errorf("repair: %v, %+v", v, st)
	}
}

func TestLoadSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "dict.json")
	d, exists, err := Load(path)
	if err != nil || exists || d == nil {
		t.Fatalf("missing file: %v %v %v", d, exists, err)
	}
	built, _, _ := Build(load(t), nil, Options{Seed: 1})
	if err := built.Save(path); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	again, exists, err := Load(path)
	if err != nil || !exists {
		t.Fatalf("load: %v %v", exists, err)
	}
	if err := again.Save(path); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Error("load and save change the file")
	}

	if err := os.WriteFile(path, []byte(`{"version": 1, "schemas": {}, "extra": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "extra") {
		t.Errorf("unknown field: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Errorf("newer version: %v", err)
	}
}

func TestDTOParts(t *testing.T) {
	s, err := spec.Load(context.Background(), "../../../testdata/gen/composed.yaml")
	if err != nil {
		t.Fatal(err)
	}
	body := s.Op("CreateShip").Op.RequestBody.Value.Content["application/json"].Schema
	captain := s.Doc.Components.Schemas["ShipExtra"].Value.Properties["Captain"]
	read := s.Op("GetShip").Op.Responses.Value("200").Value.Content["application/json"].Schema
	for name, tc := range map[string]struct {
		ref  *openapi3.SchemaRef
		want []string
	}{
		"inline body":  {body, []string{"ShipBase", "ShipExtra"}},
		"property":     {captain, []string{"Person", "PilotLicense"}},
		"named DTO":    {read, []string{"ShipRead"}},
		"own fields":   {s.Op("UpdateDock").Op.RequestBody.Value.Content["application/json"].Schema, []string{"DockBase", "PilotLicense"}},
		"only fields":  {s.Doc.Components.Schemas["DockRead"].Value.AllOf[1], nil},
		"no reference": {s.Doc.Components.Schemas["ShipMeta"].Value.Properties["Id"], nil},
	} {
		if got := DTOParts(tc.ref); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
		if DTORef(tc.ref) != "" && len(tc.want) > 1 {
			t.Errorf("%s: DTORef %q of several DTOs", name, DTORef(tc.ref))
		}
	}
}

// A property declared again in a part of an allOf keeps both
// declarations: one with only a description keeps the DTO, two that
// constrain become an allOf.
func TestPropertiesJoin(t *testing.T) {
	s, err := spec.Load(context.Background(), "../../../testdata/gen/composed.yaml")
	if err != nil {
		t.Fatal(err)
	}
	body := s.Op("CreateShip").Op.RequestBody.Value.Content["application/json"].Schema
	props, req := Properties(body.Value)
	if c := props["Captain"]; c == nil || DTOParts(c) == nil || props["Registry"] == nil || !contains(req, "Registry") {
		t.Errorf("Captain %+v, Registry %v, required %v", c, props["Registry"], req)
	}
	a := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, MaxLength: openapi3.Ptr(uint64(5))}}
	b := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, Pattern: "^a", ReadOnly: true}}
	if j := joinProperty(a, b); len(j.Value.AllOf) != 2 || !j.Value.ReadOnly {
		t.Errorf("joined %+v", j.Value)
	}
	if j := joinProperty(a, &openapi3.SchemaRef{Value: &openapi3.Schema{ReadOnly: true}}); j.Value.MaxLength == nil || !j.Value.ReadOnly || a.Value.ReadOnly {
		t.Errorf("flags %+v, original %+v", j.Value, a.Value)
	}
}
