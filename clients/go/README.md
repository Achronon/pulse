# pulse Go client

Wrap scheduled work with heartbeat check-ins. Fail-open: a monitoring error never
affects your job.

```go
import pulse "github.com/Achronon/pulse/clients/go"

// Configured from env: PULSE_URL, PULSE_TOKEN, PULSE_PROJECT
err := pulse.Run(ctx, "limesindex-refresh",
    pulse.Opts{Schedule: "0 * * * *", Grace: 2 * time.Minute, MaxRuntime: 10 * time.Minute},
    func(ctx context.Context) error { return refresh(ctx) },
)
```

`Run` pings `start`, runs your function, then `ok` (with duration) or `fail`. The next
expected run time is computed locally from `Schedule` (or `Interval`) and pushed to the
server — the server does no cron parsing.

Announce a monitor at startup (so a job that never fires is still detectable):

```go
pulse.Register(ctx, "limesindex-refresh", pulse.Opts{Schedule: "0 * * * *", Grace: 2 * time.Minute})
```

Or construct a client explicitly:

```go
c := &pulse.Client{BaseURL: "https://pulse.example", Token: "…", Project: "limesindex"}
c.Run(ctx, "slug", opts, fn)
```
