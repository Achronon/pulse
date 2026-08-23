package metrics

import (
	"path/filepath"
	"testing"

	"github.com/Achronon/pulse/server/internal/store"
	"github.com/prometheus/client_golang/prometheus"
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

func TestCollectorEmitsSeverityOnlyOnInfo(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "pulse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	severity := store.SeverityCritical
	if _, err := st.Apply("job", store.CheckIn{Status: store.StatusRegister, Project: "p", Severity: &severity}); err != nil {
		t.Fatal(err)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(NewCollector(st))
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundCritical := false
	for _, mf := range mfs {
		for _, metric := range mf.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "severity" {
					if mf.GetName() != "pulse_monitor_info" {
						t.Errorf("severity label leaked onto %s", mf.GetName())
					}
					if label.GetValue() == store.SeverityCritical {
						foundCritical = true
					}
				}
			}
		}
	}
	if !foundCritical {
		t.Fatal("pulse_monitor_info did not expose critical severity")
	}
}
