package testserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Starport is the API of testdata/gen/tree-starport.yaml, used by the tests
// of apitest-gen tree. Planets are its seed; docks, pilots and ships are
// created through the API.
type Starport struct {
	mu      sync.Mutex
	token   string
	next    int
	planets map[string]map[string]any
	docks   map[int]*dock
	pilots  map[int]map[string]any
	ships   map[int]map[string]any
	mux     *http.ServeMux
	// Requests counts the requests by method.
	Requests map[string]int
}

type dock struct {
	v       map[string]any
	version int
}

// NewStarport returns a Starport that accepts the bearer token and hands
// out ids from first on, so a stage and an empty instance differ in ids.
func NewStarport(token string, first int) *Starport {
	s := &Starport{token: token, next: first, docks: map[int]*dock{}, pilots: map[int]map[string]any{}, ships: map[int]map[string]any{},
		Requests: map[string]int{},
		planets: map[string]map[string]any{
			"mars":  {"code": "mars", "name": "Mars"},
			"venus": {"code": "venus", "name": "Venus"},
			"terra": {"code": "terra", "name": "Terra"},
		}}
	m := http.NewServeMux()
	m.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	m.HandleFunc("GET /api/planets", s.auth(s.listPlanets))
	m.HandleFunc("GET /api/planets/{code}", s.auth(s.getPlanet))
	m.HandleFunc("GET /api/planets/{code}/docks", s.auth(s.listDocks))
	m.HandleFunc("POST /api/planets/{code}/docks", s.auth(s.createDock))
	m.HandleFunc("GET /api/docks/{id}", s.auth(s.getDock))
	m.HandleFunc("PUT /api/docks/{id}", s.auth(s.updateDock))
	m.HandleFunc("PATCH /api/docks/{id}", s.auth(s.patchDock))
	m.HandleFunc("DELETE /api/docks/{id}", s.auth(s.deleteDock))
	m.HandleFunc("GET /api/pilots", s.auth(s.listPilots))
	m.HandleFunc("POST /api/pilots", s.auth(s.createPilot))
	m.HandleFunc("GET /api/pilots/{id}", s.auth(s.getPilot))
	m.HandleFunc("GET /api/pilots/{id}/profile", s.auth(s.getProfile))
	m.HandleFunc("GET /api/licenses/{id}", s.auth(s.getLicense))
	m.HandleFunc("GET /api/docks/{dock}/pilots/{id}/permit", s.auth(s.getPermit))
	m.HandleFunc("DELETE /api/pilots/{id}", s.auth(s.deletePilot))
	m.HandleFunc("GET /api/docks/{id}/ships", s.auth(s.listShips))
	m.HandleFunc("POST /api/docks/{id}/ships", s.auth(s.createShip))
	m.HandleFunc("GET /api/ships/{id}", s.auth(s.getShip))
	m.HandleFunc("DELETE /api/ships/{id}", s.auth(s.deleteShip))
	m.HandleFunc("GET /api/ships/{id}/log", s.auth(s.getLog))
	m.HandleFunc("POST /api/ships/search", s.auth(s.search))
	s.mux = m
	return s
}

func (s *Starport) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Starport) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.token {
			reply(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.Requests[r.Method]++
		next(w, r)
	}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Starport) id() int { s.next++; return s.next - 1 }

func pathID(r *http.Request) int { n, _ := strconv.Atoi(r.PathValue("id")); return n }

func readBody(r *http.Request) map[string]any {
	var m map[string]any
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	_ = dec.Decode(&m)
	return m
}

func sortedIDs[V any](m map[int]V) []int {
	var ids []int
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (s *Starport) listPlanets(w http.ResponseWriter, _ *http.Request) {
	var out []any
	for _, c := range []string{"mars", "terra", "venus"} {
		out = append(out, s.planets[c])
	}
	reply(w, http.StatusOK, out)
}

func (s *Starport) getPlanet(w http.ResponseWriter, r *http.Request) {
	if p := s.planets[r.PathValue("code")]; p != nil {
		reply(w, http.StatusOK, p)
		return
	}
	reply(w, http.StatusNotFound, map[string]any{"error": "not found"})
}

func (s *Starport) listDocks(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 2
	}
	var all []any
	for _, id := range sortedIDs(s.docks) {
		if d := s.docks[id]; d.v["planetCode"] == code {
			all = append(all, d.v)
		}
	}
	from, to := min((page-1)*size, len(all)), min(page*size, len(all))
	items := all[from:to]
	if items == nil {
		items = []any{}
	}
	reply(w, http.StatusOK, map[string]any{"items": items, "total": len(all)})
}

func (s *Starport) createDock(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if s.planets[code] == nil {
		reply(w, http.StatusNotFound, map[string]any{"error": "no planet " + code})
		return
	}
	in := readBody(r)
	if in["name"] == nil || in["capacity"] == nil {
		reply(w, http.StatusUnprocessableEntity, map[string]any{"error": "name and capacity are required"})
		return
	}
	d := &dock{v: map[string]any{"id": s.id(), "planetCode": code, "name": in["name"], "capacity": in["capacity"]}, version: 1}
	s.docks[d.v["id"].(int)] = d
	w.Header().Set("ETag", `"v1"`)
	reply(w, http.StatusCreated, d.v)
}

func (s *Starport) getDock(w http.ResponseWriter, r *http.Request) {
	d := s.docks[pathID(r)]
	if d == nil {
		reply(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	w.Header().Set("ETag", `"v`+strconv.Itoa(d.version)+`"`)
	reply(w, http.StatusOK, d.v)
}

func (s *Starport) updateDock(w http.ResponseWriter, r *http.Request) {
	d := s.docks[pathID(r)]
	if d == nil {
		reply(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if r.Header.Get("If-Match") != `"v`+strconv.Itoa(d.version)+`"` {
		reply(w, http.StatusPreconditionFailed, map[string]any{"error": "stale"})
		return
	}
	in := readBody(r)
	d.v["name"], d.v["capacity"] = in["name"], in["capacity"]
	d.version++
	w.Header().Set("ETag", `"v`+strconv.Itoa(d.version)+`"`)
	reply(w, http.StatusOK, d.v)
}

func (s *Starport) patchDock(w http.ResponseWriter, r *http.Request) {
	d := s.docks[pathID(r)]
	if d == nil {
		reply(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	for k, v := range readBody(r) {
		if k == "name" || k == "capacity" {
			d.v[k] = v
		}
	}
	d.version++
	reply(w, http.StatusOK, d.v)
}

func (s *Starport) deleteDock(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	for _, sh := range s.ships {
		if sh["dockId"] == id {
			reply(w, http.StatusConflict, map[string]any{"error": "the dock has ships"})
			return
		}
	}
	delete(s.docks, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Starport) listPilots(w http.ResponseWriter, _ *http.Request) {
	out := []any{}
	for _, id := range sortedIDs(s.pilots) {
		out = append(out, pilotSummary(s.pilots[id]))
	}
	reply(w, http.StatusOK, out)
}

func (s *Starport) createPilot(w http.ResponseWriter, r *http.Request) {
	in := readBody(r)
	p := map[string]any{"id": s.id(), "name": in["name"]}
	for _, k := range []string{"license", "homePlanet"} {
		if in[k] != nil {
			p[k] = in[k]
		}
	}
	s.pilots[p["id"].(int)] = p
	reply(w, http.StatusCreated, pilotSummary(p))
}

// pilotSummary is a pilot as lists and reads show it: without homePlanet,
// which only the profile has.
func pilotSummary(p map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range p {
		if k != "homePlanet" {
			out[k] = v
		}
	}
	return out
}

func (s *Starport) getPilot(w http.ResponseWriter, r *http.Request) {
	if p := s.pilots[pathID(r)]; p != nil {
		reply(w, http.StatusOK, pilotSummary(p))
		return
	}
	reply(w, http.StatusNotFound, map[string]any{"error": "not found"})
}

func (s *Starport) getProfile(w http.ResponseWriter, r *http.Request) {
	p := s.pilots[pathID(r)]
	if p == nil {
		reply(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	out := map[string]any{"flights": 0}
	for k, v := range p {
		out[k] = v
	}
	reply(w, http.StatusOK, out)
}

func (s *Starport) getLicense(w http.ResponseWriter, r *http.Request) {
	if s.pilots[pathID(r)] == nil {
		reply(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	reply(w, http.StatusOK, map[string]any{"pilotId": pathID(r), "level": "A"})
}

func (s *Starport) getPermit(w http.ResponseWriter, r *http.Request) {
	dock, _ := strconv.Atoi(r.PathValue("dock"))
	if s.docks[dock] == nil || s.pilots[pathID(r)] == nil {
		reply(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	reply(w, http.StatusOK, map[string]any{"dockId": dock, "pilotId": pathID(r), "granted": true})
}

func (s *Starport) deletePilot(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	for _, sh := range s.ships {
		if sh["pilotId"] == id {
			reply(w, http.StatusConflict, map[string]any{"error": "the pilot flies a ship"})
			return
		}
	}
	delete(s.pilots, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Starport) listShips(w http.ResponseWriter, r *http.Request) {
	out := []any{}
	for _, id := range sortedIDs(s.ships) {
		if s.ships[id]["dockId"] == pathID(r) {
			out = append(out, s.ships[id])
		}
	}
	reply(w, http.StatusOK, out)
}

func (s *Starport) createShip(w http.ResponseWriter, r *http.Request) {
	dockID := pathID(r)
	if s.docks[dockID] == nil {
		reply(w, http.StatusNotFound, map[string]any{"error": "no dock"})
		return
	}
	in := readBody(r)
	pilot, _ := strconv.Atoi(fmt.Sprint(in["pilotId"]))
	if s.pilots[pilot] == nil {
		reply(w, http.StatusUnprocessableEntity, map[string]any{"error": fmt.Sprintf("no pilot %v", in["pilotId"])})
		return
	}
	sh := map[string]any{"id": s.id(), "dockId": dockID, "name": in["name"], "pilotId": pilot}
	s.ships[sh["id"].(int)] = sh
	w.Header().Set("Location", "/api/ships/"+strconv.Itoa(sh["id"].(int)))
	w.WriteHeader(http.StatusCreated)
}

func (s *Starport) getShip(w http.ResponseWriter, r *http.Request) {
	if sh := s.ships[pathID(r)]; sh != nil {
		reply(w, http.StatusOK, sh)
		return
	}
	reply(w, http.StatusNotFound, map[string]any{"error": "not found"})
}

func (s *Starport) deleteShip(w http.ResponseWriter, r *http.Request) {
	delete(s.ships, pathID(r))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Starport) getLog(w http.ResponseWriter, r *http.Request) {
	if s.ships[pathID(r)] == nil {
		reply(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	reply(w, http.StatusOK, map[string]any{"shipId": pathID(r), "entries": []any{"launched"}})
}

func (s *Starport) search(w http.ResponseWriter, r *http.Request) {
	in := readBody(r)
	out := []any{}
	for _, id := range sortedIDs(s.ships) {
		if in["pilotId"] == nil || fmt.Sprint(s.ships[id]["pilotId"]) == fmt.Sprint(in["pilotId"]) {
			out = append(out, s.ships[id])
		}
	}
	reply(w, http.StatusOK, out)
}

// Fill gives the Starport the records of a stage: three pilots, docks on
// mars and venus, two ships per dock.
func (s *Starport) Fill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pilots []int
	for _, name := range []string{"Ada", "Grace", "Lin"} {
		id := s.id()
		s.pilots[id] = map[string]any{"id": id, "name": name, "license": "L-" + strings.ToUpper(name[:1]), "homePlanet": "mars"}
		pilots = append(pilots, id)
	}
	for i, spec := range []struct{ planet, name string }{{"mars", "North"}, {"mars", "South"}, {"mars", "East"}, {"venus", "Cloud"}} {
		id := s.id()
		s.docks[id] = &dock{v: map[string]any{"id": id, "planetCode": spec.planet, "name": spec.name, "capacity": 4 + i}, version: 1}
		for j := range 2 {
			sid := s.id()
			s.ships[sid] = map[string]any{"id": sid, "dockId": id, "name": spec.name + "-" + strconv.Itoa(j+1), "pilotId": pilots[(i+j)%len(pilots)]}
		}
	}
}
