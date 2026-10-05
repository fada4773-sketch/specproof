package model

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/fada4773-sketch/specproof/internal/gen/defaults"
	"github.com/fada4773-sketch/specproof/internal/spec"
)

func load(t *testing.T, file string) *spec.Spec {
	t.Helper()
	s, err := spec.Load(context.Background(), "../../../testdata/gen/"+file)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDetect(t *testing.T) {
	m := Detect(load(t, "records.yaml"), nil)
	if len(m.Resources) != 2 {
		t.Fatalf("resources: %v", m.Describe())
	}
	dock, ship := m.Resource("Dock"), m.Resource("ship")
	if dock == nil || ship == nil {
		t.Fatalf("resources: %v", m.Describe())
	}
	if dock.Read() != "DockRead" || !slices.Contains(dock.Schemas, "DockUpdate") {
		t.Errorf("Dock schemas: %v", dock.Schemas)
	}
	if !slices.Equal(dock.Keys, []string{"Id", "Code"}) {
		t.Errorf("Dock keys: %v", dock.Keys)
	}
	if ship.Parent != dock || !slices.Equal(ship.Keys, []string{"Id"}) || !slices.Equal(ship.Relations, []string{"DockId"}) {
		t.Errorf("Ship: parent %v keys %v relations %v", ship.Parent, ship.Keys, ship.Relations)
	}
	roles := map[string]Role{}
	for _, r := range m.Resources {
		for _, o := range r.Ops {
			roles[o.Op.ID] = o.Role
		}
	}
	want := map[string]Role{"GetDocks": RoleList, "GetDockById": RoleRead, "GetDock": RoleRead, "UpdateDockById": RoleUpdate,
		"UpdateDock": RoleUpdate, "GetShips": RoleList, "GetShipById": RoleRead, "UpdateShipById": RoleUpdate}
	for id, role := range want {
		if roles[id] != role {
			t.Errorf("%s: role %q, want %q", id, roles[id], role)
		}
	}
	ships := m.OpByID("GetShips")
	if p := ships.Param("Code"); p == nil || p.Resource != dock || p.Field != "Code" {
		t.Errorf("GetShips {Code}: %+v", p)
	}
	if p := m.OpByID("GetShipById").Param("id"); p == nil || p.Resource != ship || p.Field != "Id" {
		t.Errorf("GetShipById {id}: %+v", p)
	}
	if m.Op(load(t, "records.yaml").Ops[0]) != nil {
		t.Error("an operation of another spec has a model")
	}
	if d := strings.Join(m.Describe(), "\n"); !strings.Contains(d, "Ship: schemas ShipRead, ShipUpdate; keys Id; below Dock; list GetShips; read GetShipById; update UpdateShipById") {
		t.Errorf("describe:\n%s", d)
	}
}

func TestDetectFixes(t *testing.T) {
	fixes := map[string]defaults.ModelFix{
		"Dock":    {Keys: []string{"name", "Nope"}},
		"Planet":  {Keys: []string{"Id"}},
		"ShipSet": {Schemas: []string{"Missing"}},
	}
	m := Detect(load(t, "records.yaml"), fixes)
	if dock := m.Resource("Dock"); !slices.Equal(dock.Keys, []string{"Id", "Code", "Name"}) {
		t.Errorf("Dock keys: %v", dock.Keys)
	}
	var notes []string
	for _, n := range m.Notes {
		notes = append(notes, n.Where+": "+n.Message)
	}
	all := strings.Join(notes, "\n")
	for _, want := range []string{`Dock has no field "Nope"`, "$model.Planet: no GET returns this resource", `schema "Missing" does not exist`} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
}

func TestStem(t *testing.T) {
	for in, want := range map[string]string{"BookRead": "Book", "BookUpdateDto": "Book", "Response": "Response", "Book": "Book", "PlanetBase": "Planet",
		"DockUpsert": "Dock", "DockDetailRead": "Dock", "ShipsWithDetailsRead": "ShipsWithDetails", "SortByRequest": "SortByRequest"} {
		if got := Stem(in); got != want {
			t.Errorf("Stem(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Stems("DockDetailRead"); !slices.Equal(got, []string{"DockDetailRead", "DockDetail", "Dock"}) {
		t.Errorf("Stems = %v", got)
	}
}

// The resources of model.yaml: every line is one rule of the detection.
func TestDetectShapes(t *testing.T) {
	m := Detect(load(t, "model.yaml"), nil)
	d := strings.Join(m.Describe(), "\n")
	for _, want := range []string{
		// a Detail DTO with a path of its own is a resource of its own;
		// Upsert bodies create and update; a list parameter filters
		"Dock: schemas DockRead, DockUpsert; keys DockCode; list GetDocks, GetDocksByPhase; read GetDock; create CreateDock; update UpdateDock",
		"DockDetail: schemas DockDetailRead; keys Id; below Dock; list GetDockDetails; read GetDockDetail",
		// the longest chain of keys wins: below the Moon, not the Planet
		"Garden: schemas GardenRead; below Moon; list GetGardensOfPlanet, GetGardensOfMoon",
		// created below two places: one resource per place; the DELETE
		// takes the resource the PATCH at its path updates
		"MoonPerson: schemas Person; below Moon; list GetPersonsOfMoon; create CreatePersonOfMoon",
		"PlanetPerson: schemas Person; keys name; below Planet; list GetPersonsOfPlanet; create CreatePersonOfPlanet; update UpdatePersonOfPlanet; delete DeletePersonOfPlanet",
		// /Pilot/{pilotCode} returns a PilotCard: its own key, not the Pilot's
		"PilotCard: schemas PilotCardRead; keys PilotCode; list GetPilotCards; read GetPilotCard",
		// the report of a booking is a record of its own, with the update
		// whose body belongs to no resource
		"BookingPlanetReport: schemas PlanetReportRead; below Planet; read GetReportOfBooking; update UpdateReportOfBooking",
		"PlanetReport: schemas PlanetReportRead; below Planet; read GetReport",
		"ShipsWithDetails: schemas ShipsWithDetailsRead; list GetShipsWithDetails",
	} {
		if !strings.Contains(d, want+"\n") && !strings.HasSuffix(d, want) {
			t.Errorf("model lacks\n  %s\nin\n%s", want, d)
		}
	}
	// GET /Dock/Planet/{p}/Ship/{s} returns the dock of a ship: no read of
	// a dock the model can name
	if o := m.OpByID("GetDockOfShip"); o != nil {
		t.Errorf("GetDockOfShip belongs to %s", o.Resource.Name)
	}
	// keys found by name where no segment names the resource
	view := m.OpByID("GetView")
	if p := view.Param("planetCode"); p == nil || p.Resource.Name != "Planet" || p.Field != "PlanetCode" {
		t.Errorf("GetView {planetCode}: %+v", p)
	}
	if p := view.Param("moonCode"); p == nil || p.Resource.Name != "Moon" {
		t.Errorf("GetView {moonCode}: %+v", p)
	}
	// relations to the resources above (by their key names) and to others
	garden := m.Resource("Garden")
	if !slices.Equal(garden.Relations, []string{"MoonCode", "MoonId", "PlanetCode"}) {
		t.Errorf("Garden relations: %v", garden.Relations)
	}
	if to, k := garden.Ref("PlanetCode"); to == nil || to.Name != "Planet" || k != "PlanetCode" {
		t.Errorf("Garden.PlanetCode refers to %v %q", to, k)
	}
	dock := m.Resource("Dock")
	if to, k := dock.Ref("ShipCode"); to == nil || to.Name != "Ship" || k != "ShipCode" || !dock.Fixed("ShipCode") {
		t.Errorf("Dock.ShipCode refers to %v %q", to, k)
	}
	if to, k := dock.Ref("PilotId"); to == nil || to.Name != "Pilot" || k != "Id" {
		t.Errorf("Dock.PilotId refers to %v %q", to, k)
	}
	// writes of no resource: the records in their path, without the ones
	// above them
	var touched []string
	for _, r := range m.Touched(load(t, "model.yaml").Op("LinkShipToDock")) {
		touched = append(touched, r.Name)
	}
	if len(touched) != 0 {
		t.Errorf("an operation of another spec touches %v", touched)
	}
	s := load(t, "model.yaml")
	m = Detect(s, nil)
	touched = nil
	for _, r := range m.Touched(s.Op("LinkShipToDock")) {
		touched = append(touched, r.Name)
	}
	if !slices.Equal(touched, []string{"Dock", "Ship"}) {
		t.Errorf("LinkShipToDock touches %v", touched)
	}
	if m.Touched(s.Op("GetDock")) != nil {
		t.Error("a GET touches records")
	}
}

// "$model" params map a parameter whose name is no field; a field of the
// same name wins.
func TestDetectFixParams(t *testing.T) {
	s := load(t, "model.yaml")
	m := Detect(s, nil)
	if m.OpByID("GetShipByRegistry").Param("registry") != nil {
		t.Fatal("{registry} is a key without $model")
	}
	if !slices.ContainsFunc(m.Notes, func(n Note) bool {
		return strings.Contains(n.Message, `Ship has no field for {registry}; map it with "$model": {"Ship": {"params": {"registry": "<field>"}}}`)
	}) {
		t.Errorf("notes: %v", m.Notes)
	}
	m = Detect(s, map[string]defaults.ModelFix{"Ship": {Params: map[string]string{"Registry": "shipcode"}}, "Booking": {Params: map[string]string{"bookingCode": "Seats"}}})
	if p := m.OpByID("GetShipByRegistry").Param("registry"); p == nil || p.Resource.Name != "Ship" || p.Field != "ShipCode" {
		t.Errorf("GetShipByRegistry {registry}: %+v", p)
	}
	if p := m.OpByID("GetBooking").Param("bookingCode"); p == nil || p.Field != "BookingCode" {
		t.Errorf("GetBooking {bookingCode}: %+v", p)
	}
}

func TestSegmentBefore(t *testing.T) {
	for _, c := range []struct{ path, param, want string }{
		{"/Book/id/{id}", "id", "Book"},
		{"/Book/{Code}/Article/id/{id}", "id", "Article"},
		{"/Book/{Code}/Article/id/{id}", "Code", "Book"},
		{"/book/class/{class}", "class", "book"},
		{"/{a}/{b}", "b", ""},
	} {
		if got := segmentBefore(c.path, c.param); got != c.want {
			t.Errorf("segmentBefore(%q, %q) = %q, want %q", c.path, c.param, got, c.want)
		}
	}
}

// An object with one list is a page only with a list field like "items" or
// a DTO name like "…Page"; a Pilot with its Ships is a read of the Pilot.
func TestPage(t *testing.T) {
	m := Detect(load(t, "mandatory.yaml"), nil)
	if o := m.OpByID("GetPilot"); o == nil || o.Role != RoleRead || o.Resource.Name != "Pilot" {
		t.Errorf("GetPilot: %+v", o)
	}
	if m.Resource("ShipInfo") != nil {
		t.Errorf("resources: %v", m.Describe())
	}
	for _, c := range []struct {
		dto, field string
		want       bool
	}{{"ShipPage", "ships", true}, {"Ships", "items", true}, {"PilotRead", "Ships", false}} {
		if got := page(c.dto, c.field); got != c.want {
			t.Errorf("page(%q, %q) = %v", c.dto, c.field, got)
		}
	}
}

// A body or list of an allOf of several DTOs belongs to the resource its
// path names: CreateShip sends allOf [ShipBase, ShipExtra], ListShips
// returns allOf [ShipBase, ShipMeta].
func TestDetectComposed(t *testing.T) {
	s := load(t, "composed.yaml")
	m := Detect(s, nil)
	want := map[string]Role{"ListShips": RoleList, "CreateShip": RoleCreate, "UpdateShip": RoleUpdate, "PatchShip": RoleUpdate, "UpdateDock": RoleUpdate}
	for id, role := range want {
		o := m.OpByID(id)
		if o == nil || o.Role != role {
			t.Errorf("%s: %+v, want %s\n%s", id, o, role, m.Describe())
		}
	}
	if got := requestDTO(s.Op("CreateShip")); got != "ShipBase" {
		t.Errorf("CreateShip DTO %q", got)
	}
}
