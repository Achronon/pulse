// Package api is the pulse HTTP check-in surface.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Achronon/pulse/server/internal/store"
)

// tokenEntry binds a bearer token to a project. A project of "" is a wildcard
// token that may act for any project (and trusts the request's project field).
type tokenEntry struct {
	token   string
	project string
}

// Authenticator validates bearer tokens in constant time.
type Authenticator struct {
	entries []tokenEntry
}

// NewAuthenticator builds an authenticator from an optional single wildcard
// token and a map of token->project pairs.
func NewAuthenticator(single string, pairs map[string]string) *Authenticator {
	a := &Authenticator{}
	for tok, proj := range pairs {
		if tok != "" {
			a.entries = append(a.entries, tokenEntry{token: tok, project: proj})
		}
	}
	if single != "" {
		a.entries = append(a.entries, tokenEntry{token: single, project: ""})
	}
	return a
}

// Enabled reports whether any token is configured. When false, auth is bypassed
// (intended for local dev only).
func (a *Authenticator) Enabled() bool { return len(a.entries) > 0 }

// lookup returns the project bound to tok. It compares every entry without
// short-circuiting to avoid leaking token length/prefix via timing.
func (a *Authenticator) lookup(tok string) (project string, ok bool) {
	for _, e := range a.entries {
		if subtle.ConstantTimeCompare([]byte(e.token), []byte(tok)) == 1 {
			project, ok = e.project, true
		}
	}
	return project, ok
}

// Server wires the store + authenticator into HTTP handlers.
type Server struct {
	store       *store.Store
	auth        *Authenticator
	allowUnauth bool
}

// New returns a Server. allowUnauth must be set explicitly (dev only) to permit
// unauthenticated check-ins when no token is configured; otherwise the endpoint
// fails closed.
func New(s *store.Store, a *Authenticator, allowUnauth bool) *Server {
	return &Server{store: s, auth: a, allowUnauth: allowUnauth}
}

// RegisterRoutes attaches the check-in and health handlers to mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/checkin/{slug}", s.handleCheckin)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

type checkinRequest struct {
	Status            string  `json:"status"`
	Project           string  `json:"project"`
	NextExpectedAt    int64   `json:"next_expected_at"`
	IntervalSeconds   int64   `json:"interval_seconds"`
	GraceSeconds      int64   `json:"grace_seconds"`
	MaxRuntimeSeconds int64   `json:"max_runtime_seconds"`
	DurationSeconds   float64 `json:"duration_seconds"`
}

func (s *Server) handleCheckin(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !store.ValidSlug(slug) {
		http.Error(w, "invalid slug", http.StatusBadRequest)
		return
	}
	tokenProject, ok := s.authProject(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	var req checkinRequest
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !store.ValidStatus(req.Status) {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}

	// A scoped token forces its project; a wildcard token trusts the request.
	project := req.Project
	if tokenProject != "" {
		project = tokenProject
	}

	if _, err := s.store.Apply(slug, store.CheckIn{
		Status:            store.Status(req.Status),
		Project:           project,
		NextExpectedAt:    req.NextExpectedAt,
		IntervalSeconds:   req.IntervalSeconds,
		GraceSeconds:      req.GraceSeconds,
		MaxRuntimeSeconds: req.MaxRuntimeSeconds,
		DurationSeconds:   req.DurationSeconds,
	}); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// authProject returns the project a request is authorized for. When no token is
// configured it fails closed unless allowUnauth was explicitly set (dev only).
func (s *Server) authProject(r *http.Request) (project string, ok bool) {
	if !s.auth.Enabled() {
		return "", s.allowUnauth
	}
	tok, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found {
		return "", false
	}
	return s.auth.lookup(tok)
}
