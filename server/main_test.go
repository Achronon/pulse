package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Achronon/pulse/server/internal/api"
	"github.com/Achronon/pulse/server/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

func TestParseTokensRejectsEmptyProject(t *testing.T) {
	// Valid: empera:tok-emp, ops:tok-ops. Rejected: ":bad" (empty project),
	// "badproj:" (empty token), "noColon" (no separator).
	got := parseTokens("empera:tok-emp, ops:tok-ops , :bad , noColon , badproj: ")

	if len(got) != 2 {
		t.Fatalf("got %d valid tokens, want 2: %#v", len(got), got)
	}
	if got["tok-emp"] != "empera" || got["tok-ops"] != "ops" {
		t.Errorf("unexpected mapping: %#v", got)
	}
	if _, ok := got["bad"]; ok {
		t.Error("empty-project token must be rejected (no wildcard via PULSE_TOKENS)")
	}
	for tok, proj := range got {
		if proj == "" {
			t.Errorf("token %q stored with empty (wildcard) project", tok)
		}
	}
}

func TestExpireStaleRecordsAndLogsSlugs(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "pulse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Apply("stale-job", store.CheckIn{Status: store.StatusRegister, Project: "ops"}); err != nil {
		t.Fatal(err)
	}

	instrumentation := api.NewMetrics()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// A negative TTL makes the cutoff deterministic without changing Store's
	// production expiry semantics: the monitor is definitely older than it.
	expireStale(st, -time.Second, instrumentation)
	if _, found, err := st.Get("stale-job"); err != nil || found {
		t.Fatalf("stale monitor after expiry: found=%v err=%v", found, err)
	}
	if !strings.Contains(logs.String(), "stale-job") {
		t.Fatalf("expiry log = %q, want victim slug", logs.String())
	}

	reg := prometheus.NewRegistry()
	instrumentation.Register(reg)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "pulse_monitors_expired_total" {
			if len(mf.GetMetric()) != 1 || mf.GetMetric()[0].GetCounter().GetValue() != 1 {
				t.Fatalf("expired metric = %v, want 1", mf)
			}
			return
		}
	}
	t.Fatal("pulse_monitors_expired_total was not registered")
}
