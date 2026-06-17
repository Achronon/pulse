package store

import (
	"testing"
	"time"
)

// A long-interval job (e.g. a monthly cron) whose next run is still within the
// TTL window must NOT be reaped just because LastSeen is older than the TTL —
// otherwise its series vanishes and a genuine miss can never alert.
func TestExpireSkipsLongIntervalNotYetDue(t *testing.T) {
	s := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)

	// Registered 31 days ago, next run due ~29 days before "now" (i.e. 2 days
	// after the registration) — well inside a 30d sweep.
	s.now = func() time.Time { return base.Add(-31 * 24 * time.Hour) }
	nextDue := base.Add(-29 * 24 * time.Hour).Unix()
	if _, err := s.Apply("monthly", CheckIn{Status: StatusRegister, NextExpectedAt: nextDue}); err != nil {
		t.Fatal(err)
	}

	s.now = func() time.Time { return base }
	n, err := s.ExpireOlderThan(30 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expired %d, want 0 (not yet TTL past due)", n)
	}
	if _, found, _ := s.Get("monthly"); !found {
		t.Error("monthly monitor should survive — next_expected still within TTL")
	}
}

// A start ping advances next_expected to the following run, so a job that starts
// on time but runs long is not falsely flagged late (that's hung detection).
func TestStartAdvancesNextExpected(t *testing.T) {
	s := newTestStore(t)
	fixed := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return fixed }

	if _, err := s.Apply("job", CheckIn{Status: StatusRegister, IntervalSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	nextRun := fixed.Unix() + 300 // clients send the following run's time on start
	m, err := s.Apply("job", CheckIn{Status: StatusStart, NextExpectedAt: nextRun})
	if err != nil {
		t.Fatal(err)
	}
	if m.NextExpected != nextRun {
		t.Errorf("start did not advance next_expected: got %d want %d", m.NextExpected, nextRun)
	}
	if m.LastStart != fixed.Unix() {
		t.Errorf("last_start = %d, want %d", m.LastStart, fixed.Unix())
	}
}

// Once a monitor is BOTH unseen and past-due beyond the TTL, it is reaped.
func TestExpireReapsTrulyStale(t *testing.T) {
	s := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)

	s.now = func() time.Time { return base.Add(-90 * 24 * time.Hour) }
	pastDue := base.Add(-89 * 24 * time.Hour).Unix() // due 89d before now, too
	if _, err := s.Apply("dead", CheckIn{Status: StatusOK, NextExpectedAt: pastDue}); err != nil {
		t.Fatal(err)
	}

	s.now = func() time.Time { return base }
	n, err := s.ExpireOlderThan(30 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expired %d, want 1", n)
	}
	if _, found, _ := s.Get("dead"); found {
		t.Error("truly stale monitor should be reaped")
	}
}
