package store

import (
	"errors"
	"testing"
)

// A scoped check-in must not be able to hijack/move a monitor owned by another
// project, even if it knows the slug.
func TestCrossProjectWriteRejected(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Apply("shared-slug", CheckIn{Status: StatusOK, Project: "empera", IntervalSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Apply("shared-slug", CheckIn{Status: StatusOK, Project: "ops", IntervalSeconds: 60})
	if !errors.Is(err, ErrProjectMismatch) {
		t.Fatalf("expected ErrProjectMismatch, got %v", err)
	}
	// original owner unchanged
	if m, _, _ := s.Get("shared-slug"); m.Project != "empera" {
		t.Errorf("project was overwritten to %q", m.Project)
	}
	// the rightful owner can still write
	if _, err := s.Apply("shared-slug", CheckIn{Status: StatusOK, Project: "empera", IntervalSeconds: 60}); err != nil {
		t.Errorf("owner write rejected: %v", err)
	}
	// a wildcard/admin check-in (empty project) is exempt
	if _, err := s.Apply("shared-slug", CheckIn{Status: StatusOK, IntervalSeconds: 60}); err != nil {
		t.Errorf("wildcard write rejected: %v", err)
	}
}

func TestNegativeValuesRejected(t *testing.T) {
	s := newTestStore(t)
	for _, c := range []CheckIn{
		{Status: StatusRegister, GraceSeconds: -1},
		{Status: StatusRegister, MaxRuntimeSeconds: -5},
		{Status: StatusRegister, IntervalSeconds: -10},
		{Status: StatusOK, DurationSeconds: -0.5},
		{Status: StatusRegister, NextExpectedAt: -1},
	} {
		if _, err := s.Apply("job", c); !errors.Is(err, ErrNegativeValue) {
			t.Errorf("CheckIn %+v: expected ErrNegativeValue, got %v", c, err)
		}
	}
}
