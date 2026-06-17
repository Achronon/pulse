// Command pulse is a dumb heartbeat/cron-liveness exporter.
//
// It receives check-ins over HTTP, persists last-known monitor state in bbolt,
// and exposes that state as Prometheus metrics. It does not alert, parse cron
// expressions, or route notifications — those are delegated to Prometheus,
// Alertmanager, and Grafana. See kb/plans/active/0001-*.md for the full design.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Achronon/pulse/server/internal/api"
	"github.com/Achronon/pulse/server/internal/metrics"
	"github.com/Achronon/pulse/server/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := env("PULSE_ADDR", ":8080")
	dbPath := env("PULSE_DB", "/data/pulse.db")
	ttl := envDuration("PULSE_TTL", 30*24*time.Hour)

	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	auth := api.NewAuthenticator(os.Getenv("PULSE_TOKEN"), parseTokens(os.Getenv("PULSE_TOKENS")))
	allowUnauth := os.Getenv("PULSE_ALLOW_UNAUTHENTICATED") == "true"
	if !auth.Enabled() {
		// Fail closed: a missing/malformed token Secret must NOT silently expose
		// the public check-in endpoint. Local dev opts in explicitly.
		if !allowUnauth {
			return errors.New("no PULSE_TOKEN/PULSE_TOKENS configured; refusing to start unauthenticated (set PULSE_ALLOW_UNAUTHENTICATED=true for local dev)")
		}
		slog.Warn("PULSE_ALLOW_UNAUTHENTICATED=true — check-in endpoint is UNAUTHENTICATED (dev only)")
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(metrics.NewCollector(st))

	mux := http.NewServeMux()
	api.New(st, auth, allowUnauth).RegisterRoutes(mux)
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go expiryLoop(ctx, st, ttl)

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("pulse listening", "addr", addr, "db", dbPath, "ttl", ttl.String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	// Return a real error if the listener fails (e.g. port in use) so supervisors
	// and CI see a non-zero exit instead of a misleading clean shutdown.
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}

func expiryLoop(ctx context.Context, st *store.Store, ttl time.Duration) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := st.ExpireOlderThan(ttl); err != nil {
				slog.Error("expiry sweep", "err", err)
			} else if n > 0 {
				slog.Info("expired stale monitors", "count", n)
			}
		}
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		slog.Warn("invalid duration, using default", "key", key, "value", v, "default", def.String())
	}
	return def
}

// parseTokens parses "project:token,project2:token2" into a token->project map.
func parseTokens(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		proj, tok, ok := strings.Cut(pair, ":")
		proj, tok = strings.TrimSpace(proj), strings.TrimSpace(tok)
		// Require a non-empty project: a per-project token with an empty project
		// would be stored as a wildcard (handleCheckin treats project "" as
		// any-project), letting a malformed entry write to every project. Wildcard
		// access must come only from the explicit PULSE_TOKEN.
		if !ok || tok == "" || proj == "" {
			slog.Warn("ignoring malformed PULSE_TOKENS entry (want non-empty project:token)")
			continue
		}
		out[tok] = proj
	}
	return out
}
