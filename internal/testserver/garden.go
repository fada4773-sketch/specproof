package testserver

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
)

// Garden is the API of testdata/gen/tree-garden.yaml, used by the tests of
// apitest-gen tree -fix: persons choose their code, seeds belong to a
// person, moons are read by their number (no read by id), gardens bundle
// seeds (a list of embedded seeds), are tended by persons inside an
// embedded kind and lie on a moon. Every create checks what it names: a
// seed, a person or a moon that does not exist answers 404, so a test that
// sends keys of the stage fails.
type Garden struct {
	mu      sync.Mutex
	next    int
	persons map[int]map[string]any
	seeds   map[int]map[string]any
	moons   map[int]map[string]any
	gardens map[int]map[string]any
	mux     *http.ServeMux
	// Closed counts the calls of GET /close/gardens/{id}: a fetch must
	// never send it.
	Closed int
}

// NewGarden returns an empty Garden that hands out ids from first on.
func NewGarden(first int) *Garden {
	g := &Garden{next: first, persons: map[int]map[string]any{}, seeds: map[int]map[string]any{},
		moons: map[int]map[string]any{}, gardens: map[int]map[string]any{}}
	m := http.NewServeMux()
	h := func(pattern string, fn http.HandlerFunc) {
		m.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			g.mu.Lock()
			defer g.mu.Unlock()
			fn(w, r)
		})
	}
	h("GET /api/persons", func(w http.ResponseWriter, _ *http.Request) { reply(w, http.StatusOK, all(g.persons, nil)) })
	h("POST /api/persons", g.createPerson)
	h("GET /api/persons/{id}", g.getPerson)
	h("PUT /api/persons/{id}", g.updatePerson)
	h("DELETE /api/persons/{id}", g.deletePerson)
	h("GET /api/personCodes/{code}", g.personByCode)
	h("GET /api/persons/{id}/seeds", g.seedsOf)
	h("POST /api/persons/{id}/seeds", g.createSeed)
	h("GET /api/seeds/{seedId}", g.getSeed)
	h("DELETE /api/seeds/{seedId}", g.deleteSeed)
	h("GET /api/moons", func(w http.ResponseWriter, _ *http.Request) { reply(w, http.StatusOK, all(g.moons, nil)) })
	h("POST /api/moons", g.createMoon)
	h("GET /api/moons/{moonNumber}", g.getMoon)
	h("PUT /api/moons/{moonNumber}", g.updateMoon)
	h("GET /api/gardens", func(w http.ResponseWriter, _ *http.Request) {
		out := []any{}
		for _, id := range sortedIDs(g.gardens) {
			out = append(out, g.gardenRead(id))
		}
		reply(w, http.StatusOK, out)
	})
	h("POST /api/gardens", g.createGarden)
	h("GET /api/gardens/{id}", g.getGarden)
	h("PUT /api/gardens/{id}", g.updateGarden)
	h("GET /api/close/gardens/{id}", func(w http.ResponseWriter, r *http.Request) {
		gd := g.gardens[pathID(r)]
		if gd == nil {
			fail(w, http.StatusNotFound, "no garden %d", pathID(r))
			return
		}
		g.Closed++
		gd["Closed"] = true
		done(w, pathID(r), "garden closed")
	})
	h("GET /api/gardens/slug/{slug}", g.gardenBySlug)
	h("DELETE /api/gardens/slug/{slug}", g.deleteGarden)
	h("GET /api/gardenKinds", func(w http.ResponseWriter, _ *http.Request) { reply(w, http.StatusOK, []any{}) })
	g.mux = m
	return g
}

func (g *Garden) ServeHTTP(w http.ResponseWriter, r *http.Request) { g.mux.ServeHTTP(w, r) }

func (g *Garden) id() int { g.next++; return g.next - 1 }

func fail(w http.ResponseWriter, status int, format string, a ...any) {
	reply(w, status, map[string]any{"Code": strconv.Itoa(status), "Message": fmt.Sprintf(format, a...)})
}

func done(w http.ResponseWriter, id int, msg string) {
	reply(w, http.StatusOK, map[string]any{"Id": id, "Message": msg})
}

func copyOf(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ---- persons ----

func (g *Garden) personOf(code string) (int, map[string]any) {
	for _, id := range sortedIDs(g.persons) {
		if g.persons[id]["Code"] == code {
			return id, g.persons[id]
		}
	}
	return 0, nil
}

func (g *Garden) writePerson(w http.ResponseWriter, r *http.Request, self int) map[string]any {
	b := readBody(r)
	if b["Name"] == nil {
		fail(w, http.StatusBadRequest, "Name is required")
		return nil
	}
	if code, ok := b["Code"].(string); ok {
		if id, p := g.personOf(code); p != nil && id != self {
			fail(w, http.StatusConflict, "a person with code %s exists", code)
			return nil
		}
	}
	return b
}

func (g *Garden) createPerson(w http.ResponseWriter, r *http.Request) {
	b := g.writePerson(w, r, 0)
	if b == nil {
		return
	}
	id := g.id()
	g.persons[id] = map[string]any{"Id": id, "Name": b["Name"], "Code": b["Code"]}
	done(w, id, "person created")
}

func (g *Garden) getPerson(w http.ResponseWriter, r *http.Request) {
	if p := g.persons[pathID(r)]; p != nil {
		reply(w, http.StatusOK, p)
		return
	}
	fail(w, http.StatusNotFound, "no person %d", pathID(r))
}

func (g *Garden) personByCode(w http.ResponseWriter, r *http.Request) {
	if _, p := g.personOf(r.PathValue("code")); p != nil {
		reply(w, http.StatusOK, p)
		return
	}
	fail(w, http.StatusNotFound, "no person %s", r.PathValue("code"))
}

func (g *Garden) updatePerson(w http.ResponseWriter, r *http.Request) {
	p := g.persons[pathID(r)]
	if p == nil {
		fail(w, http.StatusNotFound, "no person %d", pathID(r))
		return
	}
	b := g.writePerson(w, r, pathID(r))
	if b == nil {
		return
	}
	p["Name"], p["Code"] = b["Name"], b["Code"]
	done(w, pathID(r), "person updated")
}

func (g *Garden) deletePerson(w http.ResponseWriter, r *http.Request) {
	if g.persons[pathID(r)] == nil {
		fail(w, http.StatusNotFound, "no person %d", pathID(r))
		return
	}
	for id, s := range g.seeds {
		if n, _ := num(s["PersonId"]); n == pathID(r) {
			delete(g.seeds, id)
		}
	}
	delete(g.persons, pathID(r))
	done(w, pathID(r), "person deleted")
}

// ---- seeds ----

func (g *Garden) seedRead(id int) map[string]any {
	s := copyOf(g.seeds[id])
	if n, _ := num(s["PersonId"]); g.persons[n] != nil {
		s["Person"] = g.persons[n]
	}
	return s
}

func (g *Garden) seedsOf(w http.ResponseWriter, r *http.Request) {
	if g.persons[pathID(r)] == nil {
		fail(w, http.StatusNotFound, "no person %d", pathID(r))
		return
	}
	out := []any{}
	for _, id := range sortedIDs(g.seeds) {
		if n, _ := num(g.seeds[id]["PersonId"]); n == pathID(r) {
			out = append(out, g.seedRead(id))
		}
	}
	reply(w, http.StatusOK, out)
}

func (g *Garden) createSeed(w http.ResponseWriter, r *http.Request) {
	if g.persons[pathID(r)] == nil {
		fail(w, http.StatusNotFound, "no person %d", pathID(r))
		return
	}
	b := readBody(r)
	if b["Version"] == nil {
		fail(w, http.StatusBadRequest, "Version is required")
		return
	}
	id := g.id()
	g.seeds[id] = map[string]any{"Id": id, "PersonId": pathID(r), "Version": b["Version"]}
	done(w, id, "seed created")
}

func (g *Garden) seedID(r *http.Request) int { n, _ := strconv.Atoi(r.PathValue("seedId")); return n }

func (g *Garden) getSeed(w http.ResponseWriter, r *http.Request) {
	if g.seeds[g.seedID(r)] == nil {
		fail(w, http.StatusNotFound, "no seed %d", g.seedID(r))
		return
	}
	reply(w, http.StatusOK, g.seedRead(g.seedID(r)))
}

func (g *Garden) deleteSeed(w http.ResponseWriter, r *http.Request) {
	id := g.seedID(r)
	if g.seeds[id] == nil {
		fail(w, http.StatusNotFound, "no seed %d", id)
		return
	}
	for _, gd := range g.gardens {
		for _, s := range gd["Seeds"].([]any) {
			if n, _ := num(s.(map[string]any)["Id"]); n == id {
				fail(w, http.StatusConflict, "seed %d is in garden %v", id, gd["Slug"])
				return
			}
		}
	}
	delete(g.seeds, id)
	done(w, id, "seed deleted")
}

// ---- moons ----

func (g *Garden) moonOf(number string) (int, map[string]any) {
	for _, id := range sortedIDs(g.moons) {
		if g.moons[id]["Number"] == number {
			return id, g.moons[id]
		}
	}
	return 0, nil
}

func (g *Garden) createMoon(w http.ResponseWriter, r *http.Request) {
	b := readBody(r)
	number, _ := b["Number"].(string)
	if number == "" || b["Name"] == nil {
		fail(w, http.StatusBadRequest, "Name and Number are required")
		return
	}
	if _, m := g.moonOf(number); m != nil {
		fail(w, http.StatusConflict, "moon %s exists", number)
		return
	}
	id := g.id()
	g.moons[id] = map[string]any{"Id": id, "Number": number, "Name": b["Name"]}
	done(w, id, "moon created")
}

func (g *Garden) getMoon(w http.ResponseWriter, r *http.Request) {
	if _, m := g.moonOf(r.PathValue("moonNumber")); m != nil {
		reply(w, http.StatusOK, m)
		return
	}
	fail(w, http.StatusNotFound, "no moon %s", r.PathValue("moonNumber"))
}

func (g *Garden) updateMoon(w http.ResponseWriter, r *http.Request) {
	id, m := g.moonOf(r.PathValue("moonNumber"))
	if m == nil {
		fail(w, http.StatusNotFound, "no moon %s", r.PathValue("moonNumber"))
		return
	}
	b := readBody(r)
	if b["Name"] == nil || b["Number"] != m["Number"] {
		fail(w, http.StatusBadRequest, "Name is required, the Number stays")
		return
	}
	m["Name"] = b["Name"]
	done(w, id, "moon updated")
}

// ---- gardens ----

func (g *Garden) gardenRead(id int) map[string]any {
	gd := copyOf(g.gardens[id])
	seeds := []any{}
	for _, s := range gd["Seeds"].([]any) {
		if n, _ := num(s.(map[string]any)["Id"]); g.seeds[n] != nil {
			seeds = append(seeds, g.seedRead(n))
		}
	}
	gd["Seeds"] = seeds
	return gd
}

// checkGarden checks what a garden names: its seeds, the tenders of its
// kind, its moon and its person; it answers the seeds to keep.
func (g *Garden) checkGarden(w http.ResponseWriter, b map[string]any, self int) ([]any, bool) {
	code, _ := b["Slug"].(string)
	if code == "" || b["Name"] == nil {
		fail(w, http.StatusBadRequest, "Slug and Name are required")
		return nil, false
	}
	for id, gd := range g.gardens {
		if id != self && gd["Slug"] == code {
			fail(w, http.StatusConflict, "garden %s exists", code)
			return nil, false
		}
	}
	seeds := []any{}
	list, _ := b["Seeds"].([]any)
	for _, s := range list {
		m, _ := s.(map[string]any)
		n, ok := num(m["Id"])
		if !ok || g.seeds[n] == nil {
			fail(w, http.StatusNotFound, "seed %v does not exist", m["Id"])
			return nil, false
		}
		seeds = append(seeds, map[string]any{"Id": n})
	}
	if kind, ok := b["GardenKind"].(map[string]any); ok {
		tenders, _ := kind["Tenders"].([]any)
		for _, t := range tenders {
			m, _ := t.(map[string]any)
			if n, ok := num(m["PersonId"]); ok && g.persons[n] == nil {
				fail(w, http.StatusNotFound, "tender: person %d does not exist", n)
				return nil, false
			}
		}
	}
	if v := b["MoonId"]; v != nil {
		if n, _ := num(v); g.moons[n] == nil {
			fail(w, http.StatusNotFound, "moon %v does not exist", v)
			return nil, false
		}
	}
	if code, ok := b["PersonCode"].(string); ok {
		if _, p := g.personOf(code); p == nil {
			fail(w, http.StatusNotFound, "person %s does not exist", code)
			return nil, false
		}
	}
	return seeds, true
}

func (g *Garden) setGarden(gd, b map[string]any, seeds []any) {
	for _, f := range []string{"Slug", "Name", "GardenKind", "MoonId", "PersonCode"} {
		if v, ok := b[f]; ok {
			gd[f] = v
		}
	}
	gd["Seeds"] = seeds
}

func (g *Garden) createGarden(w http.ResponseWriter, r *http.Request) {
	b := readBody(r)
	seeds, ok := g.checkGarden(w, b, 0)
	if !ok {
		return
	}
	id := g.id()
	g.gardens[id] = map[string]any{"Id": id}
	g.setGarden(g.gardens[id], b, seeds)
	done(w, id, "garden created")
}

func (g *Garden) getGarden(w http.ResponseWriter, r *http.Request) {
	if g.gardens[pathID(r)] == nil {
		fail(w, http.StatusNotFound, "no garden %d", pathID(r))
		return
	}
	reply(w, http.StatusOK, g.gardenRead(pathID(r)))
}

func (g *Garden) gardenOf(code string) int {
	for _, id := range sortedIDs(g.gardens) {
		if g.gardens[id]["Slug"] == code {
			return id
		}
	}
	return 0
}

func (g *Garden) gardenBySlug(w http.ResponseWriter, r *http.Request) {
	if id := g.gardenOf(r.PathValue("slug")); id != 0 {
		reply(w, http.StatusOK, g.gardenRead(id))
		return
	}
	fail(w, http.StatusNotFound, "no garden %s", r.PathValue("slug"))
}

func (g *Garden) updateGarden(w http.ResponseWriter, r *http.Request) {
	gd := g.gardens[pathID(r)]
	if gd == nil {
		fail(w, http.StatusNotFound, "no garden %d", pathID(r))
		return
	}
	b := readBody(r)
	seeds, ok := g.checkGarden(w, b, pathID(r))
	if !ok {
		return
	}
	g.setGarden(gd, b, seeds)
	done(w, pathID(r), "garden updated")
}

func (g *Garden) deleteGarden(w http.ResponseWriter, r *http.Request) {
	id := g.gardenOf(r.PathValue("slug"))
	if id == 0 {
		fail(w, http.StatusNotFound, "no garden %s", r.PathValue("slug"))
		return
	}
	delete(g.gardens, id)
	done(w, id, "garden deleted")
}

// Fill gives the garden registry the records of a stage: three persons
// (one without code), two seeds each, two moons, and gardens that bundle
// seeds of several persons; only some name a moon, a person or tenders.
func (g *Garden) Fill() {
	person := func(code, name string) int {
		id := g.id()
		g.persons[id] = map[string]any{"Id": id, "Name": name, "Code": code}
		return id
	}
	ada, lin, otto := person("ada", "Ada"), person("lin", "Lin"), person("otto", "Otto")
	var seeds []int
	for _, p := range []int{ada, lin, otto} {
		for v := 1; v <= 2; v++ {
			id := g.id()
			g.seeds[id] = map[string]any{"Id": id, "PersonId": p, "Version": fmt.Sprintf("%d.%d", p, v)}
			seeds = append(seeds, id)
		}
	}
	moon := func(number, name string) int {
		id := g.id()
		g.moons[id] = map[string]any{"Id": id, "Number": number, "Name": name}
		return id
	}
	phobos := moon("M001", "Phobos")
	moon("M002", "Deimos")
	garden := func(code, name string, ids []int, extra map[string]any) {
		id := g.id()
		var list []any
		for _, s := range ids {
			list = append(list, map[string]any{"Id": s})
		}
		gd := map[string]any{"Id": id, "Slug": code, "Name": name, "Seeds": list}
		for k, v := range extra {
			gd[k] = v
		}
		g.gardens[id] = gd
	}
	garden("rose", "Rose", []int{seeds[0], seeds[2]}, map[string]any{"MoonId": phobos,
		"GardenKind": map[string]any{"Label": "herbs", "Tenders": []any{map[string]any{"PersonId": lin}}}})
	garden("fern", "Fern", []int{seeds[1], seeds[3], seeds[4]}, map[string]any{"PersonCode": "ada"})
	garden("moss", "Moss", []int{seeds[5]}, nil)
}
