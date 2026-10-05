// Package server is an in-memory School API for the path-conflict example.
//
// A router cannot serve both /book/{id} and /book/{class}: they are the
// same template. Go's ServeMux panics when both are registered, so New
// serves only /book/{id}, the way most servers end up. NewFixed serves the
// class operations at /book/class/{class}, as in openapi.fixed.yaml.
package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"sync"
)

// Token is the bearer token the server accepts.
const Token = "school-secret"

type store struct {
	mu    sync.Mutex
	next  int
	items map[int]map[string]any // id → JSON object as sent, plus Id
}

// New returns the server for openapi.yaml: only the {id} routes exist.
func New() http.Handler { return build(false) }

// NewFixed returns the server for openapi.fixed.yaml.
func NewFixed() http.Handler { return build(true) }

func build(fixed bool) http.Handler {
	mux := http.NewServeMux()
	for _, res := range []struct{ path, name string }{{"/book", "Title"}, {"/student", "Name"}} {
		s := &store{items: map[int]map[string]any{}}
		mux.HandleFunc("POST "+res.path, s.create(res.name))
		mux.HandleFunc("GET "+res.path+"/{id}", s.get)
		mux.HandleFunc("PUT "+res.path+"/{id}", s.update(res.name))
		mux.HandleFunc("DELETE "+res.path+"/{id}", s.delete)
		if fixed {
			mux.HandleFunc("GET "+res.path+"/class/{class}", s.byClass)
			mux.HandleFunc("PUT "+res.path+"/class/{class}", s.moveClass)
			mux.HandleFunc("DELETE "+res.path+"/class/{class}", s.deleteClass)
		}
	}
	return auth(mux)
}

func auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+Token {
			problem(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func problem(w http.ResponseWriter, status int, msg string) {
	write(w, status, map[string]any{"Message": msg})
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// valid checks the fields the spec requires: a name and a class A–Z.
func valid(obj map[string]any, name string) bool {
	n, _ := obj[name].(string)
	c, _ := obj["Class"].(string)
	return n != "" && len(n) <= 60 && validClass(c)
}

func validClass(c string) bool { return len(c) == 1 && c[0] >= 'A' && c[0] <= 'Z' }

func decode(r *http.Request) (map[string]any, bool) {
	var obj map[string]any
	if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
		return nil, false
	}
	delete(obj, "Id") // readOnly
	return obj, true
}

func (s *store) create(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		obj, ok := decode(r)
		if !ok || !valid(obj, name) {
			problem(w, http.StatusBadRequest, name+" and Class (A-Z) are required")
			return
		}
		s.mu.Lock()
		s.next++
		obj["Id"] = s.next
		s.items[s.next] = obj
		s.mu.Unlock()
		write(w, http.StatusCreated, obj)
	}
}

// id parses {id}. A class like "A" arrives here too, because the router has
// only one route for /book/{…}.
func id(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || n < 1 {
		problem(w, http.StatusBadRequest, "id must be a positive number")
		return 0, false
	}
	return n, true
}

func (s *store) get(w http.ResponseWriter, r *http.Request) {
	n, ok := id(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	obj := s.items[n]
	s.mu.Unlock()
	if obj == nil {
		problem(w, http.StatusNotFound, "not found")
		return
	}
	write(w, http.StatusOK, obj)
}

func (s *store) update(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := id(w, r)
		if !ok {
			return
		}
		obj, ok := decode(r)
		if !ok || !valid(obj, name) {
			problem(w, http.StatusBadRequest, name+" and Class (A-Z) are required")
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.items[n] == nil {
			problem(w, http.StatusNotFound, "not found")
			return
		}
		obj["Id"] = n
		s.items[n] = obj
		write(w, http.StatusOK, obj)
	}
}

func (s *store) delete(w http.ResponseWriter, r *http.Request) {
	n, ok := id(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items[n] == nil {
		problem(w, http.StatusNotFound, "not found")
		return
	}
	delete(s.items, n)
	w.WriteHeader(http.StatusNoContent)
}

// inClass returns the items of a class, ordered by id; the lock is held.
func (s *store) inClass(class string) []map[string]any {
	out := []map[string]any{}
	ids := make([]int, 0, len(s.items))
	for n := range s.items {
		ids = append(ids, n)
	}
	slices.Sort(ids)
	for _, n := range ids {
		if s.items[n]["Class"] == class {
			out = append(out, s.items[n])
		}
	}
	return out
}

func (s *store) byClass(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	write(w, http.StatusOK, s.inClass(r.PathValue("class")))
}

func (s *store) moveClass(w http.ResponseWriter, r *http.Request) {
	obj, ok := decode(r)
	to, _ := obj["Class"].(string)
	if !ok || !validClass(to) {
		problem(w, http.StatusBadRequest, "Class (A-Z) is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	moved := s.inClass(r.PathValue("class"))
	for _, it := range moved {
		it["Class"] = to
	}
	write(w, http.StatusOK, moved)
}

func (s *store) deleteClass(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.inClass(r.PathValue("class")) {
		delete(s.items, it["Id"].(int))
	}
	w.WriteHeader(http.StatusNoContent)
}
