// Package pulse is the Go client for the pulse heartbeat monitor.
//
// It wraps a unit of scheduled work with check-ins: a "start" ping, then "ok"
// (with duration) or "fail" on completion. The next expected run time is computed
// locally from the job's own cron expression (or interval) and pushed to the
// server, so the server needs no cron engine.
//
// All check-ins are FAIL-OPEN: a monitoring error never affects the wrapped
// work's result. Configure via env (PULSE_URL, PULSE_TOKEN, PULSE_PROJECT) or by
// constructing a Client.
package pulse

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	cron "github.com/robfig/cron/v3"
)

// Opts describes a monitored job's schedule and metadata.
type Opts struct {
	// Schedule is a standard 5-field cron expression. When set, next_expected_at
	// is computed from it (full cron precision). Takes precedence over Interval.
	Schedule string
	// Interval is a simple fixed period, used when Schedule is empty.
	Interval time.Duration
	// Grace is how long after next_expected before a run is considered late.
	Grace time.Duration
	// MaxRuntime bounds a run; used for hung-job detection.
	MaxRuntime time.Duration
	// Project overrides the client's default project for this monitor.
	Project string
}

// Client talks to a pulse server.
type Client struct {
	BaseURL string
	Token   string
	Project string
	HTTP    *http.Client

	now  func() time.Time
	logf func(string, ...any)
}

// Default builds a Client from the environment:
//
//	PULSE_URL      base URL of the pulse server (no check-ins sent if empty)
//	PULSE_TOKEN    bearer token
//	PULSE_PROJECT  default project label
func Default() *Client {
	return &Client{
		BaseURL: os.Getenv("PULSE_URL"),
		Token:   os.Getenv("PULSE_TOKEN"),
		Project: os.Getenv("PULSE_PROJECT"),
		HTTP:    &http.Client{Timeout: 10 * time.Second},
		now:     time.Now,
		logf:    log.Printf,
	}
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Client) warn(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

// Register announces a monitor at process start, before its first run. This lets
// the server (and alerting) know the monitor exists and when its first run is due,
// so a job that never fires at all is still detectable.
func (c *Client) Register(ctx context.Context, slug string, o Opts) {
	c.send(ctx, slug, c.payload("register", o, 0))
}

// Run wraps fn with start/ok/fail check-ins and returns fn's error unchanged.
func (c *Client) Run(ctx context.Context, slug string, o Opts, fn func(context.Context) error) error {
	c.send(ctx, slug, c.payload("start", o, 0))
	started := c.clock()
	err := fn(ctx)
	dur := c.clock().Sub(started).Seconds()
	status := "ok"
	if err != nil {
		status = "fail"
	}
	c.send(ctx, slug, c.payload(status, o, dur))
	return err
}

type checkinBody struct {
	Status            string  `json:"status"`
	Project           string  `json:"project,omitempty"`
	NextExpectedAt    int64   `json:"next_expected_at,omitempty"`
	IntervalSeconds   int64   `json:"interval_seconds,omitempty"`
	GraceSeconds      int64   `json:"grace_seconds,omitempty"`
	MaxRuntimeSeconds int64   `json:"max_runtime_seconds,omitempty"`
	DurationSeconds   float64 `json:"duration_seconds,omitempty"`
}

func (c *Client) payload(status string, o Opts, dur float64) checkinBody {
	project := o.Project
	if project == "" {
		project = c.Project
	}
	return checkinBody{
		Status:            status,
		Project:           project,
		NextExpectedAt:    c.nextExpected(o),
		IntervalSeconds:   int64(o.Interval.Seconds()),
		GraceSeconds:      int64(o.Grace.Seconds()),
		MaxRuntimeSeconds: int64(o.MaxRuntime.Seconds()),
		DurationSeconds:   dur,
	}
}

// nextExpected computes the next due time from the cron schedule, falling back to
// now+interval. Returns 0 when neither is set (server keeps its prior value).
func (c *Client) nextExpected(o Opts) int64 {
	if o.Schedule != "" {
		if sched, err := cron.ParseStandard(o.Schedule); err == nil {
			return sched.Next(c.clock()).Unix()
		}
		c.warn("pulse: invalid schedule %q; falling back to interval", o.Schedule)
	}
	if o.Interval > 0 {
		return c.clock().Add(o.Interval).Unix()
	}
	return 0
}

func (c *Client) send(ctx context.Context, slug string, body checkinBody) {
	if c.BaseURL == "" {
		return // not configured; silently no-op (dev/local)
	}
	// Detach from the job's context so a cancelled/timed-out job still records its
	// terminal ok/fail ping; keep a short independent timeout.
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	b, err := json.Marshal(body)
	if err != nil {
		c.warn("pulse: marshal check-in for %s: %v", slug, err)
		return
	}
	req, err := http.NewRequestWithContext(sendCtx, http.MethodPost, c.BaseURL+"/v1/checkin/"+slug, bytes.NewReader(b))
	if err != nil {
		c.warn("pulse: build request for %s: %v", slug, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Do(req)
	if err != nil {
		c.warn("pulse: check-in %s: %v", slug, err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		c.warn("pulse: check-in %s: unexpected status %d", slug, resp.StatusCode)
	}
}

// Package-level convenience using a default client built from the environment.
var (
	stdOnce sync.Once
	std     *Client
)

func getStd() *Client {
	stdOnce.Do(func() { std = Default() })
	return std
}

// Run wraps fn using the default (env-configured) client.
func Run(ctx context.Context, slug string, o Opts, fn func(context.Context) error) error {
	return getStd().Run(ctx, slug, o, fn)
}

// Register announces a monitor using the default (env-configured) client.
func Register(ctx context.Context, slug string, o Opts) {
	getStd().Register(ctx, slug, o)
}
