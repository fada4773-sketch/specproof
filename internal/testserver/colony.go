package testserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Colony is the API of testdata/gen/tree-colony.yaml, used by the tests of
// apitest-gen tree: planets (seed) → moons → gardens → beds, persons who
// tend gardens, actions on the state of a garden, and reads that are
// derived from the gardens (details, harvests, reports).
type Colony struct {
	mu      sync.Mutex
	token   string
	next    int
	planets []map[string]any
	moons   map[int]map[string]any
	gardens map[int]map[string]any
	beds    map[int]map[string]any
	persons map[int]map[string]any
	mux     *http.ServeMux
	// Requests counts the requests by method.
	Requests map[string]int
}

// NewColony returns an empty Colony (planets only) that accepts the bearer
// token and hands out ids from first on.
func NewColony(token string, first int) *Colony {
	c := &Colony{token: token, next: first, moons: map[int]map[string]any{}, gardens: map[int]map[string]any{},
		beds: map[int]map[string]any{}, persons: map[int]map[string]any{}, Requests: map[string]int{},
		planets: []map[string]any{
			{"code": "mars", "name": "Mars"},
			{"code": "terra", "name": "Terra"},
			{"code": "venus", "name": "Venus"},
		}}
	m := http.NewServeMux()
	h := func(pattern string, fn http.HandlerFunc) { m.HandleFunc(pattern, c.auth(fn)) }
	h("GET /api/planets", func(w http.ResponseWriter, _ *http.Request) { reply(w, http.StatusOK, c.planets) })
	h("GET /api/planets/{code}", c.getPlanet)
	h("GET /api/planets/{code}/moons", c.listMoons)
	h("POST /api/planets/{code}/moons", c.createMoon)
	h("GET /api/moons/{id}", c.get(c.moons))
	h("DELETE /api/moons/{id}", c.remove(c.moons))
	h("GET /api/moons/{id}/gardens", c.listGardens)
	h("POST /api/moons/{id}/gardens", c.createGarden)
	h("GET /api/gardens/{id}", c.get(c.gardens))
	h("PUT /api/gardens/{id}", c.updateGarden)
	h("DELETE /api/gardens/{id}", c.remove(c.gardens))
	// /gardens/name/{name} and /gardens/{id}/details overlap for the mux
	h("GET /api/gardens/{id}/{sub}", func(w http.ResponseWriter, r *http.Request) {
		switch id, sub := r.PathValue("id"), r.PathValue("sub"); {
		case id == "name" || id == "code":
			c.byField(id, sub, w, r)
		case sub == "details":
			c.details(w, r)
		case sub == "beds":
			c.listBeds(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	for _, f := range []string{"name", "code"} {
		h("PUT /api/gardens/"+f+"/{value}", func(w http.ResponseWriter, r *http.Request) { c.byField(f, r.PathValue("value"), w, r) })
		h("DELETE /api/gardens/"+f+"/{value}", func(w http.ResponseWriter, r *http.Request) { c.byField(f, r.PathValue("value"), w, r) })
	}
	h("POST /api/gardens/{id}/open", c.action("OPEN", "DRAFT", "CLOSED"))
	h("POST /api/gardens/{id}/archive", c.action("ARCHIVED", "DRAFT", "OPEN", "CLOSED"))
	h("POST /api/gardens/{id}/beds", c.createBed)
	h("GET /api/beds/{id}", c.get(c.beds))
	h("PATCH /api/beds/{id}", c.patchBed)
	h("DELETE /api/beds/{id}", c.remove(c.beds))
	h("GET /api/persons", func(w http.ResponseWriter, _ *http.Request) { reply(w, http.StatusOK, all(c.persons, nil)) })
	h("POST /api/persons", c.createPerson)
	h("GET /api/persons/{id}", c.get(c.persons))
	h("DELETE /api/persons/{id}", c.remove(c.persons))
	h("GET /api/garden-search", c.search)
	h("GET /api/person-search", c.personSearch)
	h("GET /api/open-gardens", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, all(c.gardens, func(g map[string]any) bool { return g["status"] == "OPEN" }))
	})
	h("GET /api/harvests", c.harvests)
	h("GET /api/reports/gardens/{id}", c.report)
	h("GET /api/admin/stats", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, map[string]any{"gardens": len(c.gardens)})
	})
	h("DELETE /api/admin/purge", func(w http.ResponseWriter, _ *http.Request) {
		c.moons, c.gardens, c.beds, c.persons = map[int]map[string]any{}, map[int]map[string]any{}, map[int]map[string]any{}, map[int]map[string]any{}
		w.WriteHeader(http.StatusNoContent)
	})
	c.mux = m
	return c
}

func (c *Colony) ServeHTTP(w http.ResponseWriter, r *http.Request) { c.mux.ServeHTTP(w, r) }

func (c *Colony) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+c.token {
			reply(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.Requests[r.Method]++
		next(w, r)
	}
}

func (c *Colony) id() int { c.next++; return c.next - 1 }

// num reads a number of a decoded body.
func num(v any) (int, bool) {
	switch x := v.(type) {
	case json.Number:
		n, err := strconv.Atoi(x.String())
		return n, err == nil
	case int:
		return x, true
	case float64:
		return int(x), true
	}
	return 0, false
}

func all(m map[int]map[string]any, keep func(map[string]any) bool) []any {
	out := []any{}
	for _, id := range sortedIDs(m) {
		if keep == nil || keep(m[id]) {
			out = append(out, m[id])
		}
	}
	return out
}

func (c *Colony) get(m map[int]map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := m[pathID(r)]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reply(w, http.StatusOK, v)
	}
}

func (c *Colony) remove(m map[int]map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		delete(m, pathID(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

func (c *Colony) getPlanet(w http.ResponseWriter, r *http.Request) {
	for _, p := range c.planets {
		if p["code"] == r.PathValue("code") {
			reply(w, http.StatusOK, p)
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

func (c *Colony) listMoons(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	reply(w, http.StatusOK, all(c.moons, func(m map[string]any) bool { return m["planetCode"] == code }))
}

func (c *Colony) createMoon(w http.ResponseWriter, r *http.Request) {
	b := readBody(r)
	id := c.id()
	c.moons[id] = map[string]any{"id": id, "planetCode": r.PathValue("code"), "name": b["name"]}
	reply(w, http.StatusCreated, c.moons[id])
}

func (c *Colony) listGardens(w http.ResponseWriter, r *http.Request) {
	moon := pathID(r)
	reply(w, http.StatusOK, all(c.gardens, func(g map[string]any) bool { return g["moonId"] == moon }))
}

// byField reads, updates or deletes the garden whose field (name, code) has
// the value.
func (c *Colony) byField(field, value string, w http.ResponseWriter, r *http.Request) {
	id := 0
	for _, k := range sortedIDs(c.gardens) {
		if c.gardens[k][field] == value {
			id = k
			break
		}
	}
	g := c.gardens[id]
	if g == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		delete(c.gardens, id)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPut:
		if !c.garden(g, readBody(r)) {
			reply(w, http.StatusUnprocessableEntity, map[string]any{"error": "no such person"})
			return
		}
		reply(w, http.StatusOK, g)
	default:
		reply(w, http.StatusOK, g)
	}
}

// garden takes the fields of a write; a person must exist.
func (c *Colony) garden(g, b map[string]any) bool {
	g["name"] = b["name"]
	g["code"] = b["code"]
	if p, ok := num(b["personId"]); ok {
		if c.persons[p] == nil {
			return false
		}
		g["personId"] = p
	} else {
		delete(g, "personId")
	}
	if s, ok := num(b["size"]); ok {
		g["size"] = s
	}
	if s, ok := b["status"].(string); ok {
		g["status"] = s
	}
	return true
}

func (c *Colony) createGarden(w http.ResponseWriter, r *http.Request) {
	moon := pathID(r)
	if c.moons[moon] == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	g := map[string]any{"moonId": moon, "status": "DRAFT"}
	if !c.garden(g, readBody(r)) {
		reply(w, http.StatusUnprocessableEntity, map[string]any{"error": "no such person"})
		return
	}
	id := c.id()
	g["id"] = id
	c.gardens[id] = g
	reply(w, http.StatusCreated, g)
}

func (c *Colony) updateGarden(w http.ResponseWriter, r *http.Request) {
	g := c.gardens[pathID(r)]
	if g == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if !c.garden(g, readBody(r)) {
		reply(w, http.StatusUnprocessableEntity, map[string]any{"error": "no such person"})
		return
	}
	reply(w, http.StatusOK, g)
}

// details exist for a garden with a size.
func (c *Colony) details(w http.ResponseWriter, r *http.Request) {
	g := c.gardens[pathID(r)]
	if size, _ := num(g["size"]); g == nil || size == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	reply(w, http.StatusOK, map[string]any{"gardenId": g["id"], "soil": "loam", "climate": "dry"})
}

// action moves a garden to a state, from the states it allows.
func (c *Colony) action(to string, from ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g := c.gardens[pathID(r)]
		if g == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		ok := false
		for _, f := range from {
			ok = ok || g["status"] == f
		}
		if !ok {
			reply(w, http.StatusConflict, map[string]any{"error": "the garden is " + g["status"].(string)})
			return
		}
		g["status"] = to
		reply(w, http.StatusOK, g)
	}
}

func (c *Colony) listBeds(w http.ResponseWriter, r *http.Request) {
	garden := pathID(r)
	reply(w, http.StatusOK, all(c.beds, func(b map[string]any) bool { return b["gardenId"] == garden }))
}

func (c *Colony) createBed(w http.ResponseWriter, r *http.Request) {
	garden := pathID(r)
	if c.gardens[garden] == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	b := readBody(r)
	id := c.id()
	bed := map[string]any{"id": id, "gardenId": garden, "plant": b["plant"]}
	if n, ok := num(b["rows"]); ok {
		bed["rows"] = n
	}
	c.beds[id] = bed
	reply(w, http.StatusCreated, bed)
}

func (c *Colony) patchBed(w http.ResponseWriter, r *http.Request) {
	bed := c.beds[pathID(r)]
	if bed == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if n, ok := num(readBody(r)["rows"]); ok {
		bed["rows"] = n
	}
	reply(w, http.StatusOK, bed)
}

func (c *Colony) createPerson(w http.ResponseWriter, r *http.Request) {
	b := readBody(r)
	id := c.id()
	p := map[string]any{"id": id, "name": b["name"]}
	if e, ok := b["email"]; ok {
		p["email"] = e
	}
	c.persons[id] = p
	reply(w, http.StatusCreated, p)
}

// search lists the gardens a person of the name tends.
func (c *Colony) search(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	reply(w, http.StatusOK, all(c.gardens, func(g map[string]any) bool {
		p, _ := num(g["personId"])
		return c.persons[p] != nil && c.persons[p]["name"] == name
	}))
}

// personSearch lists the persons who tend a garden of the name.
func (c *Colony) personSearch(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("gardenName")
	tends := map[int]bool{}
	for _, g := range c.gardens {
		if p, ok := num(g["personId"]); ok && g["name"] == name {
			tends[p] = true
		}
	}
	reply(w, http.StatusOK, all(c.persons, func(p map[string]any) bool { id, _ := num(p["id"]); return tends[id] }))
}

// harvests are derived from the gardens a person tends.
func (c *Colony) harvests(w http.ResponseWriter, r *http.Request) {
	person, _ := strconv.Atoi(r.URL.Query().Get("personId"))
	season := r.URL.Query().Get("season")
	out := []any{}
	for _, id := range sortedIDs(c.gardens) {
		g := c.gardens[id]
		if p, _ := num(g["personId"]); p == person {
			size, _ := num(g["size"])
			out = append(out, map[string]any{"id": id*10 + 1, "personId": person, "gardenId": id, "season": season, "amount": size * 3})
		}
	}
	reply(w, http.StatusOK, out)
}

func (c *Colony) report(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if c.gardens[id] == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	n := 0
	for _, b := range c.beds {
		if b["gardenId"] == id {
			n++
		}
	}
	reply(w, http.StatusOK, map[string]any{"gardenId": id, "total": n})
}

// Fill gives the colony the records of a stage: moons on Mars and Terra
// (none on Venus), gardens in every state, with and without person, size
// and beds, and four persons, one without garden.
func (c *Colony) Fill() {
	person := func(name string) int {
		id := c.id()
		c.persons[id] = map[string]any{"id": id, "name": name, "email": name + "@colony.example"}
		return id
	}
	ada, lin, grace := person("Ada"), person("Lin"), person("Grace")
	person("Otto")
	moon := func(planet, name string) int {
		id := c.id()
		c.moons[id] = map[string]any{"id": id, "planetCode": planet, "name": name}
		return id
	}
	garden := func(moon int, name string, person int, status string, size, beds int) {
		id := c.id()
		g := map[string]any{"id": id, "moonId": moon, "code": strings.ToLower(name), "name": name, "status": status}
		if person > 0 {
			g["personId"] = person
		}
		if size > 0 {
			g["size"] = size
		}
		c.gardens[id] = g
		for i := range beds {
			b := c.id()
			c.beds[b] = map[string]any{"id": b, "gardenId": id, "plant": name + "-" + strconv.Itoa(i+1), "rows": i + 2}
		}
	}
	phobos, deimos, luna := moon("mars", "Phobos"), moon("mars", "Deimos"), moon("terra", "Luna")
	garden(phobos, "Rose", ada, "DRAFT", 4, 2)
	garden(phobos, "Fern", lin, "OPEN", 0, 1) // no details
	garden(phobos, "Moss", 0, "DRAFT", 2, 1)  // no person
	garden(deimos, "Sage", grace, "CLOSED", 3, 1)
	garden(deimos, "Iris", ada, "ARCHIVED", 2, 1)
	garden(luna, "Mint", lin, "DRAFT", 5, 3)
	garden(luna, "Thyme", grace, "DRAFT", 2, 0) // no beds
}
