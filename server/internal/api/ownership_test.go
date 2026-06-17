package api

import (
	"net/http"
	"testing"
)

// A scoped token must get 403 when posting to a slug owned by another project,
// and 400 when sending negative timing values.
func TestScopedTokenCannotCrossProject(t *testing.T) {
	auth := NewAuthenticator("", map[string]string{
		"tok-emp": "empera",
		"tok-ops": "ops",
	})
	ts, st := newTestServer(t, auth, false)

	// empera registers its monitor
	resp := post(t, ts, "empera-booking-expiry", "tok-emp", `{"status":"ok","interval_seconds":300}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("empera write status %d", resp.StatusCode)
	}
	// ops token tries to hijack the same slug -> 403
	resp = post(t, ts, "empera-booking-expiry", "tok-ops", `{"status":"ok","interval_seconds":300}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-project write status = %d, want 403", resp.StatusCode)
	}
	if m, _, _ := st.Get("empera-booking-expiry"); m.Project != "empera" {
		t.Errorf("project hijacked to %q", m.Project)
	}
}

func TestNegativeValueRejected(t *testing.T) {
	ts, _ := newTestServer(t, NewAuthenticator("secret", nil), false)
	resp := post(t, ts, "job", "secret", `{"status":"register","grace_seconds":-5}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative grace status = %d, want 400", resp.StatusCode)
	}
}
