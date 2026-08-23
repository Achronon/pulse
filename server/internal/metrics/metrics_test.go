package metrics

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Achronon/pulse/server/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	bolt "go.etcd.io/bbolt"
)

// A started-but-never-succeeded monitor with no grace must still emit
// pulse_last_success_timestamp_seconds (0) and pulse_grace_seconds (0), or the
// hung/late alert rules drop it for lack of a matching series.
func TestCollectorAlwaysEmitsGraceAndLastSuccess(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "pulse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := st.Apply("job", store.CheckIn{Status: store.StatusStart, Project: "p", IntervalSeconds: 60}); err != nil {
		t.Fatal(err)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(NewCollector(st))
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	vals := map[string]float64{}
	present := map[string]bool{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			present[mf.GetName()] = true
			vals[mf.GetName()] = m.GetGauge().GetValue()
		}
	}

	for _, name := range []string{"pulse_grace_seconds", "pulse_last_success_timestamp_seconds"} {
		if !present[name] {
			t.Errorf("%s not emitted; alert rules need it even at 0", name)
		}
		if vals[name] != 0 {
			t.Errorf("%s = %v, want 0", name, vals[name])
		}
	}
	if !present["pulse_last_start_timestamp_seconds"] {
		t.Error("pulse_last_start_timestamp_seconds should be present after a start")
	}
	// last_failure must be absent — the monitor has never failed.
	if present["pulse_last_failure_timestamp_seconds"] {
		t.Error("pulse_last_failure_timestamp_seconds should be absent (never failed)")
	}
}

func TestCollectorIsolatesUnreadableRowsAndReportsCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pulse.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Apply("good", store.CheckIn{Status: store.StatusOK, Project: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("monitors")).Put([]byte("poisoned"), []byte(`{"slug":"poisoned","project":"p","runs_ok":"5"}`))
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewCollector(st))

	rr := httptest.NewRecorder()
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `pulse_monitor_info{monitor="good",project="p"} 1`) {
		t.Fatalf("valid monitor series missing from body:\n%s", body)
	}
	if !strings.Contains(body, "pulse_store_unreadable_rows 1") {
		t.Fatalf("unreadable-row gauge missing from body:\n%s", body)
	}
}
