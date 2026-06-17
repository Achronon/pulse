// Package metrics exposes the store's monitor state as Prometheus metrics.
//
// It is a pull-time collector: on every scrape it reflects current store state,
// so there is no separate bookkeeping to drift. Alerting decisions live entirely
// in Prometheus/Alertmanager rules over these series — the exporter stays dumb.
package metrics

import (
	"github.com/Achronon/pulse/server/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// Collector implements prometheus.Collector over a store.Store.
type Collector struct {
	store *store.Store

	lastSuccess  *prometheus.Desc
	lastStart    *prometheus.Desc
	lastFailure  *prometheus.Desc
	nextExpected *prometheus.Desc
	grace        *prometheus.Desc
	maxRuntime   *prometheus.Desc
	lastDuration *prometheus.Desc
	runs         *prometheus.Desc
	info         *prometheus.Desc
}

// NewCollector builds a Collector reading from s.
func NewCollector(s *store.Store) *Collector {
	labels := []string{"monitor", "project"}
	return &Collector{
		store:        s,
		lastSuccess:  prometheus.NewDesc("pulse_last_success_timestamp_seconds", "Unix time of last successful completion.", labels, nil),
		lastStart:    prometheus.NewDesc("pulse_last_start_timestamp_seconds", "Unix time of last run start.", labels, nil),
		lastFailure:  prometheus.NewDesc("pulse_last_failure_timestamp_seconds", "Unix time of last reported failure.", labels, nil),
		nextExpected: prometheus.NewDesc("pulse_next_expected_timestamp_seconds", "Unix time the next run is expected.", labels, nil),
		grace:        prometheus.NewDesc("pulse_grace_seconds", "Grace window after next_expected before a run is considered late.", labels, nil),
		maxRuntime:   prometheus.NewDesc("pulse_max_runtime_seconds", "Max expected runtime, used for hung-job detection.", labels, nil),
		lastDuration: prometheus.NewDesc("pulse_last_duration_seconds", "Duration of the last completed run.", labels, nil),
		runs:         prometheus.NewDesc("pulse_runs_total", "Total runs by terminal status.", []string{"monitor", "project", "status"}, nil),
		info:         prometheus.NewDesc("pulse_monitor_info", "Monitor registration info; constant 1.", labels, nil),
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		c.lastSuccess, c.lastStart, c.lastFailure, c.nextExpected,
		c.grace, c.maxRuntime, c.lastDuration, c.runs, c.info,
	} {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	monitors, err := c.store.List()
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.info, err)
		return
	}
	for _, m := range monitors {
		gauge := func(d *prometheus.Desc, v float64) {
			ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, m.Slug, m.Project)
		}
		gauge(c.info, 1)
		// last_success and grace are ALWAYS emitted (even when 0) because the alert
		// rules combine them with binary operators, and PromQL drops samples that
		// lack a matching series on the other side:
		//   - hung rule needs last_success as the RHS of last_start > last_success,
		//     so a started-but-never-succeeded monitor (last_success=0) must still
		//     emit a series or first-run hangs never alert.
		//   - late rule adds next_expected + grace, so a 0-grace monitor must still
		//     emit grace=0 or it can never become late.
		gauge(c.lastSuccess, float64(m.LastSuccess))
		gauge(c.grace, float64(m.GraceSeconds))
		// The rest are emitted only when meaningful; their absence is correct (no
		// last_failure ⇒ never failed; no max_runtime ⇒ opted out of hung detection;
		// no next_expected ⇒ schedule unknown, cannot be late).
		if m.LastStart > 0 {
			gauge(c.lastStart, float64(m.LastStart))
		}
		if m.LastFailure > 0 {
			gauge(c.lastFailure, float64(m.LastFailure))
		}
		if m.NextExpected > 0 {
			gauge(c.nextExpected, float64(m.NextExpected))
		}
		if m.MaxRuntimeSeconds > 0 {
			gauge(c.maxRuntime, float64(m.MaxRuntimeSeconds))
		}
		gauge(c.lastDuration, m.LastDuration)
		ch <- prometheus.MustNewConstMetric(c.runs, prometheus.CounterValue, float64(m.RunsOK), m.Slug, m.Project, "ok")
		ch <- prometheus.MustNewConstMetric(c.runs, prometheus.CounterValue, float64(m.RunsFail), m.Slug, m.Project, "fail")
	}
}
