package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type capture struct {
	mu   sync.Mutex
	reqs []checkinBody
	auth []string
}

func (c *capture) record(b checkinBody, auth string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, b)
	c.auth = append(c.auth, auth)
}

func newCaptureServer(t *testing.T) (*httptest.Server, *capture) {
	t.Helper()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b checkinBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		cap.record(b, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func testClient(url string) *Client {
	return &Client{
		BaseURL: url,
		Token:   "tok",
		Project: "ops",
		HTTP:    &http.Client{Timeout: 5 * time.Second},
		now:     func() time.Time { return time.Unix(1_700_000_000, 0) },
		logf:    func(string, ...any) {},
	}
}

func TestRunOK(t *testing.T) {
	srv, cap := newCaptureServer(t)
	c := testClient(srv.URL)

	err := c.Run(context.Background(), "job", Opts{Interval: 5 * time.Minute, Grace: time.Minute}, func(context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
	if len(cap.reqs) != 2 {
		t.Fatalf("got %d check-ins, want 2 (start, ok)", len(cap.reqs))
	}
	if cap.reqs[0].Status != "start" || cap.reqs[1].Status != "ok" {
		t.Fatalf("statuses = %q,%q want start,ok", cap.reqs[0].Status, cap.reqs[1].Status)
	}
	if cap.reqs[1].Project != "ops" {
		t.Errorf("project = %q, want ops", cap.reqs[1].Project)
	}
	if cap.reqs[1].IntervalSeconds != 300 || cap.reqs[1].GraceSeconds != 60 {
		t.Errorf("metadata wrong: %+v", cap.reqs[1])
	}
	if want := int64(1_700_000_000 + 300); cap.reqs[1].NextExpectedAt != want {
		t.Errorf("next_expected = %d, want %d", cap.reqs[1].NextExpectedAt, want)
	}
	if cap.auth[0] != "Bearer tok" {
		t.Errorf("auth header = %q", cap.auth[0])
	}
}

func TestRunFailReportsAndPropagates(t *testing.T) {
	srv, cap := newCaptureServer(t)
	c := testClient(srv.URL)

	sentinel := errors.New("boom")
	err := c.Run(context.Background(), "job", Opts{Interval: time.Minute}, func(context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Run returned %v, want sentinel", err)
	}
	if cap.reqs[1].Status != "fail" {
		t.Errorf("terminal status = %q, want fail", cap.reqs[1].Status)
	}
}

func TestFailOpenOnDeadServer(t *testing.T) {
	// Unroutable base URL: check-ins error, but Run must still return fn's result.
	c := testClient("http://127.0.0.1:1") // connection refused
	ran := false
	err := c.Run(context.Background(), "job", Opts{Interval: time.Minute}, func(context.Context) error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("Run returned %v, want nil (fail-open)", err)
	}
	if !ran {
		t.Fatal("fn did not run")
	}
}

func TestTerminalPingSurvivesCancelledContext(t *testing.T) {
	srv, cap := newCaptureServer(t)
	c := testClient(srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	err := c.Run(ctx, "job", Opts{Interval: time.Minute}, func(context.Context) error {
		cancel() // job cancels its own context before returning
		return nil
	})
	if err != nil {
		t.Fatalf("Run returned %v", err)
	}
	// Both start and ok must have been recorded despite the cancellation.
	if len(cap.reqs) != 2 || cap.reqs[1].Status != "ok" {
		t.Fatalf("expected start+ok despite cancel, got %+v", cap.reqs)
	}
}

func TestNextExpectedFromSchedule(t *testing.T) {
	c := testClient("")
	// 1_700_000_000 = 2023-11-14T22:13:20Z. Every 5 minutes -> next is 22:15:00Z.
	got := c.nextExpected(Opts{Schedule: "*/5 * * * *"})
	next := time.Unix(got, 0).UTC()
	if next.Minute()%5 != 0 || next.Second() != 0 {
		t.Errorf("next_expected %s not aligned to */5", next)
	}
	if !next.After(time.Unix(1_700_000_000, 0)) {
		t.Errorf("next_expected %s not after now", next)
	}
}

func TestRegister(t *testing.T) {
	srv, cap := newCaptureServer(t)
	c := testClient(srv.URL)
	c.Register(context.Background(), "job", Opts{Schedule: "0 * * * *", Grace: 2 * time.Minute})
	if len(cap.reqs) != 1 || cap.reqs[0].Status != "register" {
		t.Fatalf("expected one register, got %+v", cap.reqs)
	}
	if cap.reqs[0].NextExpectedAt == 0 {
		t.Error("register should carry next_expected_at")
	}
}
