package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "pulse.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestValidSlug(t *testing.T) {
	ok := []string{"a", "empera-booking-expiry", "ops-rule-regen", "x9"}
	bad := []string{"", "-leading", "UPPER", "has_underscore", "white space", "a." + "b"}
	for _, s := range ok {
		if !ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = true, want false", s)
		}
	}
}

func TestApplyLifecycle(t *testing.T) {
	s := newTestStore(t)
	fixed := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return fixed }

	// register carries schedule metadata + initial next_expected via interval.
	m, err := s.Apply("job", CheckIn{Status: StatusRegister, Project: "empera", IntervalSeconds: 300, GraceSeconds: 120, MaxRuntimeSeconds: 240})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if m.Project != "empera" || m.GraceSeconds != 120 || m.MaxRuntimeSeconds != 240 {
		t.Fatalf("register metadata not stored: %+v", m)
	}
	if want := fixed.Unix() + 300; m.NextExpected != want {
		t.Errorf("next_expected = %d, want %d", m.NextExpected, want)
	}

	// start sets last_start only.
	if m, _ = s.Apply("job", CheckIn{Status: StatusStart}); m.LastStart != fixed.Unix() {
		t.Errorf("last_start = %d, want %d", m.LastStart, fixed.Unix())
	}

	// ok sets last_success, duration, increments RunsOK, advances next_expected.
	m, _ = s.Apply("job", CheckIn{Status: StatusOK, IntervalSeconds: 300, DurationSeconds: 1.5})
	if m.LastSuccess != fixed.Unix() || m.RunsOK != 1 || m.LastDuration != 1.5 {
		t.Errorf("ok state wrong: %+v", m)
	}

	// a bare ok must not clobber registration metadata (grace/maxruntime).
	if m.GraceSeconds != 120 || m.MaxRuntimeSeconds != 240 {
		t.Errorf("ok clobbered metadata: %+v", m)
	}

	// fail increments RunsFail.
	if m, _ = s.Apply("job", CheckIn{Status: StatusFail}); m.RunsFail != 1 {
		t.Errorf("runs_fail = %d, want 1", m.RunsFail)
	}
}

func TestNextExpectedAtWins(t *testing.T) {
	s := newTestStore(t)
	explicit := int64(1_700_009_999)
	m, err := s.Apply("job", CheckIn{Status: StatusOK, IntervalSeconds: 300, NextExpectedAt: explicit})
	if err != nil {
		t.Fatal(err)
	}
	if m.NextExpected != explicit {
		t.Errorf("next_expected = %d, want explicit %d", m.NextExpected, explicit)
	}
}

func TestInvalidStatus(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Apply("job", CheckIn{Status: "bogus"}); err == nil {
		t.Fatal("expected error for invalid status")
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pulse.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply("job", CheckIn{Status: StatusOK, Project: "p", IntervalSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	m, found, err := s2.Get("job")
	if err != nil || !found {
		t.Fatalf("get after reopen: found=%v err=%v", found, err)
	}
	if m.Project != "p" || m.RunsOK != 1 {
		t.Errorf("state not persisted: %+v", m)
	}
}

func TestExpireOlderThan(t *testing.T) {
	s := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)

	// stale monitor last seen 48h ago.
	s.now = func() time.Time { return base.Add(-48 * time.Hour) }
	if _, err := s.Apply("stale", CheckIn{Status: StatusOK, IntervalSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	// fresh monitor seen now.
	s.now = func() time.Time { return base }
	if _, err := s.Apply("fresh", CheckIn{Status: StatusOK, IntervalSeconds: 60}); err != nil {
		t.Fatal(err)
	}

	n, err := s.ExpireOlderThan(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expired %d, want 1", n)
	}
	if _, found, _ := s.Get("stale"); found {
		t.Error("stale monitor should have been expired")
	}
	if _, found, _ := s.Get("fresh"); !found {
		t.Error("fresh monitor should remain")
	}
}
