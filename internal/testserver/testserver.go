// Package testserver is the "Bookstore" API used by the component tests of
// apitest. Its spec is testdata/bookstore.yaml. Each switch in
// Faults builds in exactly one kind of error.
package testserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Faults switch on deliberate errors.
type Faults struct {
	WrongStatus        bool // POST /authors answers 200 instead of 201
	UndocumentedStatus bool // GET /books/{id} answers 202
	SchemaViolation    bool // GET /books/{id} returns authorId as a string
	ExampleMismatch    bool // POST /authors returns a different name than it stores
	Slow               bool // GET /authors answers after 30 s (or when the request ends)
	DropOnUpdate       bool // PUT /books/{id} answers 200 but does not store the title
	DeleteNoop         bool // DELETE /reviews/{id} answers 204 but does not delete
	FailCreateAuthor   bool // POST /authors with a valid body answers 500
	DropShelfName      bool // PUT /shelves/{code} stores the new code but not the name
	IgnoreAuth         bool // requests without token (or api_key) are accepted
	IgnoreSignature    bool // any bearer token is accepted, also manipulated ones
	IgnoreRoles        bool // the reader token may write
	// AsyncUpdate makes PUT /authors/{id} visible only after this many GETs
	// of the author (eventual consistency, FR-VERIFY-06); 0 applies at once.
	AsyncUpdate int
}

// Server is the Bookstore API. It is an http.Handler serving under /v1.
type Server struct {
	Tokens  []string // accepted bearer tokens with full rights
	Readers []string // accepted bearer tokens that may only read (403 on writes)
	APIKey  string   // expected api_key query parameter
	Faults  Faults

	mux     *http.ServeMux
	mu      sync.Mutex
	authors map[int]*author
	books   map[int]*book
	reviews map[int]*review
	shelves map[string]*shelf
	nextID  map[string]int
	pending map[int]*pendingUpdate
}

type pendingUpdate struct {
	gets int
	in   authorWrite
}

type author struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Born *int   `json:"born,omitempty"`
}

type book struct {
	ID       int     `json:"id"`
	Title    string  `json:"title"`
	Price    float64 `json:"price"`
	AuthorID int     `json:"authorId"`
}

type review struct {
	ID     int    `json:"id"`
	BookID int    `json:"bookId"`
	Stars  int    `json:"stars"`
	Text   string `json:"text,omitempty"`
}

type shelf struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// New returns a Bookstore with one seed author (ID 1).
func New(token, apiKey string, f Faults) *Server {
	born := 1906
	s := &Server{
		Tokens:  []string{token},
		APIKey:  apiKey,
		Faults:  f,
		authors: map[int]*author{1: {ID: 1, Name: "Grace Hopper", Born: &born}},
		books:   map[int]*book{},
		reviews: map[int]*review{},
		shelves: map[string]*shelf{},
		nextID:  map[string]int{"author": 2, "book": 1, "review": 1},
		pending: map[int]*pendingUpdate{},
	}
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/health", s.health)
	m.HandleFunc("GET /v1/stats", s.stats)
	m.HandleFunc("POST /v1/authors", s.bearer(s.createAuthor))
	m.HandleFunc("GET /v1/authors", s.bearer(s.listAuthors))
	m.HandleFunc("GET /v1/authors/{id}", s.bearer(s.getAuthor))
	m.HandleFunc("PUT /v1/authors/{id}", s.bearer(s.updateAuthor))
	m.HandleFunc("DELETE /v1/authors/{id}", s.bearer(s.deleteAuthor))
	m.HandleFunc("POST /v1/authors/{id}/books", s.bearer(s.createBook))
	m.HandleFunc("GET /v1/books/{id}", s.bearer(s.getBook))
	m.HandleFunc("PUT /v1/books/{id}", s.bearer(s.updateBook))
	m.HandleFunc("DELETE /v1/books/{id}", s.bearer(s.deleteBook))
	m.HandleFunc("POST /v1/books/{id}/reviews", s.bearer(s.createReview))
	m.HandleFunc("GET /v1/reviews/{id}", s.bearer(s.getReview))
	m.HandleFunc("DELETE /v1/reviews/{id}", s.bearer(s.deleteReview))
	m.HandleFunc("POST /v1/shelves", s.bearer(s.createShelf))
	m.HandleFunc("GET /v1/shelves/{code}", s.bearer(s.getShelf))
	m.HandleFunc("PUT /v1/shelves/{code}", s.bearer(s.updateShelf))
	m.HandleFunc("DELETE /v1/shelves/{code}", s.bearer(s.deleteShelf))
	s.mux = m
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) bearer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch s.role(r.Header.Get("Authorization")) {
		case "admin":
		case "reader":
			if r.Method != http.MethodGet && !s.Faults.IgnoreRoles {
				writeJSON(w, http.StatusForbidden, errBody("forbidden"))
				return
			}
		default:
			writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		next(w, r)
	}
}

// role returns "admin", "reader" or "" for the Authorization header.
func (s *Server) role(auth string) string {
	if auth == "" {
		if s.Faults.IgnoreAuth {
			return "admin"
		}
		return ""
	}
	for _, t := range s.Tokens {
		if auth == "Bearer "+t {
			return "admin"
		}
	}
	for _, t := range s.Readers {
		if auth == "Bearer "+t {
			return "reader"
		}
	}
	if s.Faults.IgnoreSignature && len(auth) > len("Bearer ") {
		return "admin"
	}
	return ""
}

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func (s *Server) id(r *http.Request) int {
	id, _ := strconv.Atoi(r.PathValue("id"))
	return id
}

func (s *Server) next(kind string) int {
	id := s.nextID[kind]
	s.nextID[kind]++
	return id
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid json"))
		return false
	}
	return true
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("api_key")
	ok := key == s.APIKey || (key == "" && s.Faults.IgnoreAuth) || (key != "" && s.Faults.IgnoreSignature)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
		return
	}
	s.mu.Lock()
	n := len(s.authors)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int{"authors": n})
}

type authorWrite struct {
	Name string `json:"name"`
	Born *int   `json:"born"`
}

func (s *Server) createAuthor(w http.ResponseWriter, r *http.Request) {
	var in authorWrite
	if !decode(w, r, &in) {
		return
	}
	if in.Name == "" {
		writeJSON(w, http.StatusBadRequest, errBody("name is required"))
		return
	}
	if s.Faults.FailCreateAuthor {
		writeJSON(w, http.StatusInternalServerError, errBody("database unavailable"))
		return
	}
	a := &author{ID: s.next("author"), Name: in.Name, Born: in.Born}
	s.authors[a.ID] = a
	out := *a
	if s.Faults.ExampleMismatch {
		out.Name = "Someone Else"
	}
	status := http.StatusCreated
	if s.Faults.WrongStatus {
		status = http.StatusOK
	}
	writeJSON(w, status, out)
}

func (s *Server) listAuthors(w http.ResponseWriter, r *http.Request) {
	if s.Faults.Slow {
		s.mu.Unlock()
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		s.mu.Lock()
		if r.Context().Err() != nil {
			return
		}
	}
	list := []*author{}
	for id := 1; id < s.nextID["author"]; id++ {
		if a := s.authors[id]; a != nil {
			list = append(list, a)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getAuthor(w http.ResponseWriter, r *http.Request) {
	if p := s.pending[s.id(r)]; p != nil {
		if p.gets--; p.gets <= 0 {
			if a := s.authors[s.id(r)]; a != nil {
				a.Name, a.Born = p.in.Name, p.in.Born
			}
			delete(s.pending, s.id(r))
		}
	}
	if a := s.authors[s.id(r)]; a != nil {
		writeJSON(w, http.StatusOK, a)
		return
	}
	writeJSON(w, http.StatusNotFound, errBody("not found"))
}

func (s *Server) updateAuthor(w http.ResponseWriter, r *http.Request) {
	a := s.authors[s.id(r)]
	if a == nil {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	var in authorWrite
	if !decode(w, r, &in) {
		return
	}
	if s.Faults.AsyncUpdate > 0 {
		s.pending[a.ID] = &pendingUpdate{gets: s.Faults.AsyncUpdate, in: in}
		writeJSON(w, http.StatusOK, author{ID: a.ID, Name: in.Name, Born: in.Born})
		return
	}
	a.Name, a.Born = in.Name, in.Born
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) deleteAuthor(w http.ResponseWriter, r *http.Request) {
	if s.authors[s.id(r)] == nil {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	delete(s.authors, s.id(r))
	w.WriteHeader(http.StatusNoContent)
}

type bookWrite struct {
	Title string  `json:"title"`
	Price float64 `json:"price"`
}

func (s *Server) createBook(w http.ResponseWriter, r *http.Request) {
	if s.authors[s.id(r)] == nil {
		writeJSON(w, http.StatusNotFound, errBody("author not found"))
		return
	}
	var in bookWrite
	if !decode(w, r, &in) {
		return
	}
	b := &book{ID: s.next("book"), Title: in.Title, Price: in.Price, AuthorID: s.id(r)}
	s.books[b.ID] = b
	w.Header().Set("Location", "/v1/books/"+strconv.Itoa(b.ID))
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) getBook(w http.ResponseWriter, r *http.Request) {
	b := s.books[s.id(r)]
	switch {
	case b == nil:
		writeJSON(w, http.StatusNotFound, errBody("not found"))
	case s.Faults.SchemaViolation:
		writeJSON(w, http.StatusOK, map[string]any{"id": b.ID, "title": b.Title, "price": b.Price, "authorId": strconv.Itoa(b.AuthorID)})
	case s.Faults.UndocumentedStatus:
		writeJSON(w, http.StatusAccepted, b)
	default:
		writeJSON(w, http.StatusOK, b)
	}
}

func (s *Server) updateBook(w http.ResponseWriter, r *http.Request) {
	b := s.books[s.id(r)]
	if b == nil {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	var in bookWrite
	if !decode(w, r, &in) {
		return
	}
	if !s.Faults.DropOnUpdate {
		b.Title = in.Title
	}
	b.Price = in.Price
	// The response claims what was sent, so only the GET check can notice
	// that the title was not stored.
	writeJSON(w, http.StatusOK, book{ID: b.ID, Title: in.Title, Price: in.Price, AuthorID: b.AuthorID})
}

func (s *Server) deleteBook(w http.ResponseWriter, r *http.Request) {
	if s.books[s.id(r)] == nil {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	delete(s.books, s.id(r))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createReview(w http.ResponseWriter, r *http.Request) {
	if s.books[s.id(r)] == nil {
		writeJSON(w, http.StatusNotFound, errBody("book not found"))
		return
	}
	var in struct {
		Stars int    `json:"stars"`
		Text  string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	rv := &review{ID: s.next("review"), BookID: s.id(r), Stars: in.Stars, Text: in.Text}
	s.reviews[rv.ID] = rv
	writeJSON(w, http.StatusCreated, rv)
}

func (s *Server) getReview(w http.ResponseWriter, r *http.Request) {
	if rv := s.reviews[s.id(r)]; rv != nil {
		writeJSON(w, http.StatusOK, rv)
		return
	}
	writeJSON(w, http.StatusNotFound, errBody("not found"))
}

func (s *Server) deleteReview(w http.ResponseWriter, r *http.Request) {
	if s.reviews[s.id(r)] == nil {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	if !s.Faults.DeleteNoop {
		delete(s.reviews, s.id(r))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createShelf(w http.ResponseWriter, r *http.Request) {
	var in shelf
	if !decode(w, r, &in) {
		return
	}
	s.shelves[in.Code] = &shelf{Code: in.Code, Name: in.Name}
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) getShelf(w http.ResponseWriter, r *http.Request) {
	if sh := s.shelves[r.PathValue("code")]; sh != nil {
		writeJSON(w, http.StatusOK, sh)
		return
	}
	writeJSON(w, http.StatusNotFound, errBody("not found"))
}

func (s *Server) updateShelf(w http.ResponseWriter, r *http.Request) {
	old := r.PathValue("code")
	sh := s.shelves[old]
	if sh == nil {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	var in shelf
	if !decode(w, r, &in) {
		return
	}
	delete(s.shelves, old)
	sh.Code = in.Code
	if !s.Faults.DropShelfName {
		sh.Name = in.Name
	}
	s.shelves[sh.Code] = sh
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) deleteShelf(w http.ResponseWriter, r *http.Request) {
	if s.shelves[r.PathValue("code")] == nil {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	delete(s.shelves, r.PathValue("code"))
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
