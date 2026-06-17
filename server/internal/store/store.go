// Package store is the persisted, dumb state for pulse monitors.
//
// It holds exactly what clients report (last check-in times, schedule metadata,
// run counters) and nothing more. It does NOT evaluate cron expressions, decide
// liveness, or alert — that is delegated to Prometheus/Alertmanager scraping the
// metrics derived from this state.
package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	bolt "go.etcd.io/bbolt"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidSlug reports whether s is an acceptable monitor slug.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// Status is a check-in lifecycle event.
type Status string

const (
	StatusRegister Status = "register"
	StatusStart    Status = "start"
	StatusOK       Status = "ok"
	StatusFail     Status = "fail"
)

// ValidStatus reports whether s is a known check-in status.
func ValidStatus(s string) bool {
	switch Status(s) {
	case StatusRegister, StatusStart, StatusOK, StatusFail:
		return true
	}
	return false
}

// Monitor is the persisted state for a single monitored job.
// All timestamps are unix seconds; 0 means "never".
type Monitor struct {
	Slug              string  `json:"slug"`
	Project           string  `json:"project"`
	LastSuccess       int64   `json:"last_success"`
	LastStart         int64   `json:"last_start"`
	LastFailure       int64   `json:"last_failure"`
	NextExpected      int64   `json:"next_expected"`
	GraceSeconds      int64   `json:"grace_seconds"`
	MaxRuntimeSeconds int64   `json:"max_runtime_seconds"`
	IntervalSeconds   int64   `json:"interval_seconds"`
	LastDuration      float64 `json:"last_duration"`
	RunsOK            uint64  `json:"runs_ok"`
	RunsFail          uint64  `json:"runs_fail"`
	LastSeen          int64   `json:"last_seen"`
}

// CheckIn is an incoming check-in event applied to a monitor.
type CheckIn struct {
	Status            Status
	Project           string
	NextExpectedAt    int64
	IntervalSeconds   int64
	GraceSeconds      int64
	MaxRuntimeSeconds int64
	DurationSeconds   float64
}

var bucket = []byte("monitors")

// Store is a bbolt-backed monitor store.
type Store struct {
	db  *bolt.DB
	now func() time.Time
}

// Open opens (creating if needed) the bbolt database at path.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bolt %q: %w", path, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, e := tx.CreateBucketIfNotExists(bucket)
		return e
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Apply mutates (creating if absent) the monitor identified by slug per c, and
// returns the resulting state. Schedule metadata fields are only overwritten when
// provided (> 0), so a bare start/ok/fail ping does not clobber registration data.
func (s *Store) Apply(slug string, c CheckIn) (Monitor, error) {
	if !ValidStatus(string(c.Status)) {
		return Monitor{}, fmt.Errorf("invalid status %q", c.Status)
	}
	var out Monitor
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		m := Monitor{Slug: slug}
		if raw := b.Get([]byte(slug)); raw != nil {
			if e := json.Unmarshal(raw, &m); e != nil {
				return fmt.Errorf("unmarshal %s: %w", slug, e)
			}
		}
		now := s.now().Unix()
		m.LastSeen = now
		if c.Project != "" {
			m.Project = c.Project
		}
		if c.GraceSeconds > 0 {
			m.GraceSeconds = c.GraceSeconds
		}
		if c.MaxRuntimeSeconds > 0 {
			m.MaxRuntimeSeconds = c.MaxRuntimeSeconds
		}
		if c.IntervalSeconds > 0 {
			m.IntervalSeconds = c.IntervalSeconds
		}
		// next_expected: an explicit client-computed timestamp wins (full cron
		// precision); otherwise fall back to now+interval (simple-period path).
		setNext := func() {
			switch {
			case c.NextExpectedAt > 0:
				m.NextExpected = c.NextExpectedAt
			case c.IntervalSeconds > 0:
				m.NextExpected = now + c.IntervalSeconds
			}
		}
		switch c.Status {
		case StatusRegister:
			setNext()
		case StatusStart:
			m.LastStart = now
			// Advance next_expected to the FOLLOWING run as soon as a run starts.
			// Otherwise a job that starts on time but runs longer than its grace
			// window would trip the late rule (time() > next_expected + grace) even
			// though it did start punctually — "running too long" is hung detection
			// (max_runtime), not lateness. Clients send next_expected_at on start.
			setNext()
		case StatusOK:
			m.LastSuccess = now
			m.LastDuration = c.DurationSeconds
			m.RunsOK++
			setNext()
		case StatusFail:
			m.LastFailure = now
			m.LastDuration = c.DurationSeconds
			m.RunsFail++
			setNext()
		}
		raw, e := json.Marshal(m)
		if e != nil {
			return e
		}
		out = m
		return b.Put([]byte(slug), raw)
	})
	return out, err
}

// Get returns the monitor for slug, if present.
func (s *Store) Get(slug string) (Monitor, bool, error) {
	var m Monitor
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucket).Get([]byte(slug))
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, &m)
	})
	return m, found, err
}

// List returns all monitors.
func (s *Store) List() ([]Monitor, error) {
	var ms []Monitor
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).ForEach(func(_, v []byte) error {
			var m Monitor
			if e := json.Unmarshal(v, &m); e != nil {
				return e
			}
			ms = append(ms, m)
			return nil
		})
	})
	return ms, err
}

// ExpireOlderThan removes monitors not seen within ttl and returns the count
// removed. This is the TTL auto-expiry that stops decommissioned crons from
// phantom-alerting forever.
func (s *Store) ExpireOlderThan(ttl time.Duration) (int, error) {
	cutoff := s.now().Add(-ttl).Unix()
	var removed int
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		// Collect keys first — deleting during cursor iteration is unsafe.
		var keys [][]byte
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var m Monitor
			if e := json.Unmarshal(v, &m); e != nil {
				continue
			}
			// Reap only a monitor that is BOTH unseen for the TTL window AND past
			// its next expected run by that window (or has no known schedule). This
			// keeps long-interval jobs alive until well after they were actually
			// due — e.g. a monthly cron must not be deleted a day before its next
			// run by a 30d sweep, which would erase its series so a real miss can
			// never alert.
			if m.LastSeen < cutoff && (m.NextExpected == 0 || m.NextExpected < cutoff) {
				kk := make([]byte, len(k))
				copy(kk, k)
				keys = append(keys, kk)
			}
		}
		for _, k := range keys {
			if e := b.Delete(k); e != nil {
				return e
			}
			removed++
		}
		return nil
	})
	return removed, err
}
