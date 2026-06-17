package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Achronon/pulse/server/internal/store"
)

func newTestServer(t *testing.T, auth *Authenticator, allowUnauth bool) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "pulse.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mux := http.NewServeMux()
	New(st, auth, allowUnauth).RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, st
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
