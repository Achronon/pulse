package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Achronon/pulse/server/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

func newTestServer(t *testing.T, auth *Authenticator, allowUnauth bool) (*httptest.Server, *store.Store) {
	ts, st, _ := newInstrumentedTestServer(t, auth, allowUnauth)
	return ts, st
}

func newInstrumentedTestServer(t *testing.T, auth *Authenticator, allowUnauth bool) (*httptest.Server, *store.Store, *Metrics) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "pulse.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	instrumentation := NewMetrics()
	mux := http.NewServeMux()
	NewWithMetrics(st, auth, allowUnauth, instrumentation).RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, st, instrumentation
}

func post(t *testing.T, ts *httptest.Server, slug, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/checkin/"+slug, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func gatheredCounterValue(t *testing.T, instrumentation *Metrics, name string, labels map[string]string) float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	instrumentation.Register(reg)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			matches := true
			for key, want := range labels {
				found := false
				for _, label := range metric.GetLabel() {
					if label.GetName() == key && label.GetValue() == want {
						found = true
						break
					}
				}
				if !found {
					matches = false
					break
				}
			}
			if matches {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestCheckinRequiresAuth(t *testing.T) {
	ts, _ := newTestServer(t, NewAuthenticator("secret", nil), false)
	resp := post(t, ts, "job", "", `{"status":"ok"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestCheckinWrongToken(t *testing.T) {
	ts, _ := newTestServer(t, NewAuthenticator("secret", nil), false)
	resp := post(t, ts, "job", "nope", `{"status":"ok"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestCheckinOK(t *testing.T) {
	ts, st := newTestServer(t, NewAuthenticator("secret", nil), false)
	resp := post(t, ts, "job", "secret", `{"status":"ok","project":"empera","interval_seconds":300,"duration_seconds":2}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	m, found, err := st.Get("job")
	if err != nil || !found {
		t.Fatalf("monitor not stored: found=%v err=%v", found, err)
	}
	if m.RunsOK != 1 || m.Project != "empera" || m.LastDuration != 2 {
		t.Errorf("unexpected state: %+v", m)
	}
}

func TestScopedTokenForcesProject(t *testing.T) {
	// token scoped to project "empera"; request claims "evil" — must be overridden.
	ts, st := newTestServer(t, NewAuthenticator("", map[string]string{"tok-emp": "empera"}), false)
	resp := post(t, ts, "job", "tok-emp", `{"status":"ok","project":"evil","interval_seconds":60}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	m, _, _ := st.Get("job")
	if m.Project != "empera" {
		t.Errorf("project = %q, want empera (scoped token must win)", m.Project)
	}
}

func TestCheckinRejectsBadInput(t *testing.T) {
	ts, _ := newTestServer(t, NewAuthenticator("secret", nil), false)
	cases := []struct {
		name, slug, body string
	}{
		{"bad slug", "BAD SLUG", `{"status":"ok"}`},
		{"bad status", "job", `{"status":"bogus"}`},
		{"bad json", "job", `{not json`},
		{"unknown field", "job", `{"status":"ok","wat":1}`},
		{"trailing data", "job", `{"status":"ok"} garbage`},
		{"two objects", "job", `{"status":"ok"}{"status":"fail"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := post(t, ts, c.slug, "secret", c.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestAuthDisabledBypassesOnlyWhenAllowed(t *testing.T) {
	// No tokens + allowUnauth=true (explicit dev opt-in) => check-ins accepted.
	ts, st := newTestServer(t, NewAuthenticator("", nil), true)
	resp := post(t, ts, "job", "", `{"status":"ok","project":"dev","interval_seconds":60}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (auth disabled, explicitly allowed)", resp.StatusCode)
	}
	if m, found, _ := st.Get("job"); !found || m.Project != "dev" {
		t.Errorf("unexpected state: found=%v %+v", found, m)
	}
}

func TestNoTokenFailsClosedByDefault(t *testing.T) {
	// No tokens + allowUnauth=false (default) => endpoint must reject, not fail open.
	ts, _ := newTestServer(t, NewAuthenticator("", nil), false)
	resp := post(t, ts, "job", "", `{"status":"ok","interval_seconds":60}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (must fail closed on missing token)", resp.StatusCode)
	}
}

func TestCheckinOutcomeMetrics(t *testing.T) {
	auth := NewAuthenticator("", map[string]string{"tok-emp": "empera", "tok-ops": "ops"})
	ts, _, instrumentation := newInstrumentedTestServer(t, auth, false)

	resp := post(t, ts, "job", "tok-emp", `{"status":"ok","project":"empera"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("valid check-in status = %d, want 204", resp.StatusCode)
	}
	resp = post(t, ts, "job-unauthorized", "wrong", `{"status":"ok"}`)
	resp.Body.Close()
	resp = post(t, ts, "job-invalid", "tok-emp", `{"status":"ok","unknown":1}`)
	resp.Body.Close()
	resp = post(t, ts, "job", "tok-ops", `{"status":"ok"}`)
	resp.Body.Close()

	for _, outcome := range []string{"ok", "unauthorized", "invalid", "forbidden"} {
		if got := gatheredCounterValue(t, instrumentation, "pulse_checkins_total", map[string]string{"outcome": outcome}); got != 1 {
			t.Errorf("check-in outcome %q = %v, want 1", outcome, got)
		}
	}
}

func TestStoreWriteErrorsAreCountedAndLogged(t *testing.T) {
	ts, st, instrumentation := newInstrumentedTestServer(t, NewAuthenticator("secret", nil), false)
	_ = st.Close()

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	resp := post(t, ts, "write-error", "secret", `{"status":"ok"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("store error status = %d, want 500", resp.StatusCode)
	}
	if got := gatheredCounterValue(t, instrumentation, "pulse_store_write_errors_total", nil); got != 1 {
		t.Errorf("store write errors = %v, want 1", got)
	}
	if !strings.Contains(logs.String(), "check-in store write failed") || !strings.Contains(logs.String(), "write-error") {
		t.Errorf("store write log = %q, want safe error with slug", logs.String())
	}
}

func TestHealthzProbesStore(t *testing.T) {
	ts, st, _ := newInstrumentedTestServer(t, NewAuthenticator("secret", nil), false)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthy status = %d, want 200", resp.StatusCode)
	}

	_ = st.Close()
	resp, err = http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("closed-store status = %d, want 503", resp.StatusCode)
	}
}
