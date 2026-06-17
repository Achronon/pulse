package store

import (
	"testing"
	"time"
)

// register is authoritative: re-registering with grace/max_runtime cleared to 0
// must overwrite the previous nonzero values (so a job can disable hung detection).
func TestRegisterClearsStaleMetadata(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.Apply("job", CheckIn{Status: StatusRegister, GraceSeconds: 120, MaxRuntimeSeconds: 240, IntervalSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Apply("job", CheckIn{Status: StatusRegister, IntervalSeconds: 300}) // grace+max omitted (0)
	if err != nil {
		t.Fatal(err)
	}
	if m.GraceSeconds != 0 || m.MaxRuntimeSeconds != 0 {
		t.Errorf("re-register did not clear stale metadata: grace=%d max=%d", m.GraceSeconds, m.MaxRuntimeSeconds)
	}

	// But a bare start/ok must NOT clobber an existing registration's metadata.
	if _, err := s.Apply("job", CheckIn{Status: StatusRegister, GraceSeconds: 60, MaxRuntimeSeconds: 90, IntervalSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Apply("job", CheckIn{Status: StatusStart})
	if m.GraceSeconds != 60 || m.MaxRuntimeSeconds != 90 {
		t.Errorf("bare start clobbered metadata: grace=%d max=%d", m.GraceSeconds, m.MaxRuntimeSeconds)
	}
}

// A terminal ok/fail clears LastStart so the hung rule (last_start > last_success)
// cannot re-fire for an already-completed/failed run.
func TestTerminalClearsLastStart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal Status
	}{
		{"ok clears", StatusOK},
		{"fail clears", StatusFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			fixed := time.Unix(1_700_000_000, 0)
			s.now = func() time.Time { return fixed }

			if _, err := s.Apply("job", CheckIn{Status: StatusStart, IntervalSeconds: 300}); err != nil {
				t.Fatal(err)
			}
			if m, _, _ := s.Get("job"); m.LastStart == 0 {
				t.Fatal("start should set LastStart")
			}
			m, err := s.Apply("job", CheckIn{Status: tc.terminal, IntervalSeconds: 300})
			if err != nil {
				t.Fatal(err)
			}
			if m.LastStart != 0 {
				t.Errorf("%s did not clear LastStart: %d", tc.terminal, m.LastStart)
			}
		})
	}
}
