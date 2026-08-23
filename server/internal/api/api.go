// Package api is the pulse HTTP check-in surface.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Achronon/pulse/server/internal/store"
	"github.com/prometheus/client_golang/prometheus"
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

// Metrics contains process-scoped counters for the server's own ingest and
// storage outcomes. Labels are intentionally closed and never include slug,
// project, token, or remote address.
type Metrics struct {
	checkins        *prometheus.CounterVec
	storeWriteErrs  prometheus.Counter
	monitorsExpired prometheus.Counter
}

// NewMetrics creates the counters used by the HTTP surface and expiry loop.
func NewMetrics() *Metrics {
	return &Metrics{
		checkins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pulse_checkins_total",
			Help: "Check-in requests by closed outcome class.",
		}, []string{"outcome"}),
		storeWriteErrs: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pulse_store_write_errors_total",
			Help: "Store write failures while applying check-ins.",
		}),
		monitorsExpired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pulse_monitors_expired_total",
			Help: "Monitors removed by the TTL expiry sweep.",
		}),
	}
}

// Register adds the server counters to a Prometheus registry.
func (m *Metrics) Register(reg prometheus.Registerer) {
	reg.MustRegister(m.checkins, m.storeWriteErrs, m.monitorsExpired)
}

// RecordExpired records monitors removed by a successful TTL sweep.
func (m *Metrics) RecordExpired(n int) {
	if n > 0 {
		m.monitorsExpired.Add(float64(n))
	}
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
	metrics     *Metrics
}

// New returns a Server. allowUnauth must be set explicitly (dev only) to permit
// unauthenticated check-ins when no token is configured; otherwise the endpoint
// fails closed.
func New(s *store.Store, a *Authenticator, allowUnauth bool) *Server {
	return NewWithMetrics(s, a, allowUnauth, NewMetrics())
}

// NewWithMetrics returns a Server using the supplied process-scoped metrics.
// It is separate from New so existing embedders keep the simple constructor.
func NewWithMetrics(s *store.Store, a *Authenticator, allowUnauth bool, m *Metrics) *Server {
	if m == nil {
		m = NewMetrics()
	}
	return &Server{store: s, auth: a, allowUnauth: allowUnauth, metrics: m}
}

// RegisterRoutes attaches the check-in and health handlers to mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/checkin/{slug}", s.handleCheckin)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	// Get performs a single cheap bbolt View without scanning monitor state.
	if _, _, err := s.store.Get("__pulse_healthz_probe__"); err != nil {
		slog.Error("healthz store probe failed", "err", err)
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
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
		s.metrics.checkins.WithLabelValues("invalid").Inc()
		http.Error(w, "invalid slug", http.StatusBadRequest)
		return
	}
	tokenProject, ok := s.authProject(r)
	if !ok {
		s.metrics.checkins.WithLabelValues("unauthorized").Inc()
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	var req checkinRequest
	if err := dec.Decode(&req); err != nil {
		s.metrics.checkins.WithLabelValues("invalid").Inc()
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Reject trailing data (e.g. `{...} garbage` or two concatenated objects) so a
	// malformed body cannot smuggle a mutation past the field validation above.
	if dec.Decode(&struct{}{}) != io.EOF {
		s.metrics.checkins.WithLabelValues("invalid").Inc()
		http.Error(w, "unexpected trailing data", http.StatusBadRequest)
		return
	}
	if !store.ValidStatus(req.Status) {
		s.metrics.checkins.WithLabelValues("invalid").Inc()
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}

	// A scoped token forces its project; a wildcard token trusts the request.
	project := req.Project
	if tokenProject != "" {
		project = tokenProject
	}

	_, err := s.store.Apply(slug, store.CheckIn{
		Status:            store.Status(req.Status),
		Project:           project,
		NextExpectedAt:    req.NextExpectedAt,
		IntervalSeconds:   req.IntervalSeconds,
		GraceSeconds:      req.GraceSeconds,
		MaxRuntimeSeconds: req.MaxRuntimeSeconds,
		DurationSeconds:   req.DurationSeconds,
	})
	switch {
	case err == nil:
		s.metrics.checkins.WithLabelValues("ok").Inc()
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNegativeValue):
		s.metrics.checkins.WithLabelValues("invalid").Inc()
		http.Error(w, "negative timing values not allowed", http.StatusBadRequest)
	case errors.Is(err, store.ErrProjectMismatch):
		s.metrics.checkins.WithLabelValues("forbidden").Inc()
		// Slug is owned by another project — don't leak which; just forbid.
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		s.metrics.checkins.WithLabelValues("store_error").Inc()
		s.metrics.storeWriteErrs.Inc()
		slog.Error("check-in store write failed", "slug", slug, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
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
