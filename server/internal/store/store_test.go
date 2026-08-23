package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func stringPtr(s string) *string { return &s }

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

func TestSeverityDefaultsAndBarePingsPreserveIt(t *testing.T) {
	s := newTestStore(t)
	m, err := s.Apply("job", CheckIn{Status: StatusRegister, Project: "p", Severity: stringPtr(SeverityCritical)})
	if err != nil {
		t.Fatal(err)
	}
	if m.Severity != SeverityCritical {
		t.Fatalf("severity = %q, want %q", m.Severity, SeverityCritical)
	}

	m, err = s.Apply("job", CheckIn{Status: StatusOK})
	if err != nil {
		t.Fatal(err)
	}
	if m.Severity != SeverityCritical {
		t.Errorf("bare ping changed severity to %q", m.Severity)
	}

	m, err = s.Apply("defaulted", CheckIn{Status: StatusRegister})
	if err != nil {
		t.Fatal(err)
	}
	if m.Severity != SeverityWarning {
		t.Errorf("missing registration severity = %q, want %q", m.Severity, SeverityWarning)
	}
}

func TestInvalidSeverity(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Apply("job", CheckIn{Status: StatusRegister, Severity: stringPtr("urgent")}); !errors.Is(err, ErrInvalidSeverity) {
		t.Fatalf("error = %v, want ErrInvalidSeverity", err)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pulse.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply("job", CheckIn{Status: StatusRegister, Project: "p", Severity: stringPtr(SeverityCritical), IntervalSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply("job", CheckIn{Status: StatusOK}); err != nil {
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
	if m.Project != "p" || m.RunsOK != 1 || m.Severity != SeverityCritical {
		t.Errorf("state not persisted: %+v", m)
	}
}

func TestLegacySchemaFixtureCompatibility(t *testing.T) {
	s := newTestStore(t)
	legacy := map[string]any{
		"slug": "legacy", "project": "p", "last_success": int64(1_700_000_000),
		"runs_ok": uint64(4), "last_seen": int64(1_700_000_100),
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).Put([]byte("legacy"), raw)
	}); err != nil {
		t.Fatal(err)
	}

	m, found, err := s.Get("legacy")
	if err != nil || !found {
		t.Fatalf("legacy get: found=%v err=%v", found, err)
	}
	if m.Project != "p" || m.RunsOK != 4 || m.Severity != SeverityWarning {
		t.Errorf("legacy row was not read with compatibility default: %+v", m)
	}

	// A pre-severity reader ignores the additive JSON field and can still read
	// the fields it owns. This is the rollback-direction schema check.
	newRaw, err := json.Marshal(Monitor{Slug: "new", Project: "p", Severity: SeverityCritical, RunsOK: 2})
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Slug    string `json:"slug"`
		Project string `json:"project"`
		RunsOK  uint64 `json:"runs_ok"`
	}
	if err := json.Unmarshal(newRaw, &old); err != nil {
		t.Fatal(err)
	}
	if old.Slug != "new" || old.Project != "p" || old.RunsOK != 2 {
		t.Errorf("legacy reader could not read new row: %+v", old)
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
