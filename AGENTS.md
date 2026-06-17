# AGENTS.md — pulse

Self-hosted heartbeat / cron-liveness monitoring for the Achronon stack. A **dumb
Prometheus exporter** (`server/`) + thin client decorators (`clients/{nest,go,python}/`).
Alerting, dashboards, dedup, routing, and forensics are delegated to the Ops Control
Plane (Prometheus + Alertmanager + Grafana + Loki).

## Golden rule

**The server stays dumb.** It only receives check-ins, persists last-known state
(bbolt), and exposes Prometheus metrics. It does NOT alert, parse cron expressions, or
route notifications. If a change pushes the server to grow a cron engine, a ping-log UI,
pause/resume CRUD, or projects/RBAC — stop and reconsider self-hosting Healthchecks.io
instead (see `kb/plans/active/0001-*.md`, "Re-evaluate trigger").

## Layout

```
server/          Go exporter — main.go + internal/{store,api,metrics}
clients/nest/    @Pulse decorator (TS)
clients/go/      pulse.Run wrapper
clients/python/  @pulse decorator
kb/              repo-mode knowledge base (plans/active, decisions, active.md)
```

## Conventions

- Go: module `github.com/Achronon/pulse/server`; `gofmt`; table tests; `go test ./...`
  must pass; no CGO (static binary, distroless image).
- Clients **fail open**: a failed check-in must never break the wrapped job.
- Slugs: `^[a-z0-9][a-z0-9-]{0,63}$`, namespaced per project (`empera-*`, `ops-*`, ...).
- Schedule: clients compute `next_expected_at` from their own cron expr and push it
  (server does no cron parsing). Simple jobs may push `interval_seconds` instead.

## Build / test (server)

```
cd server
go test ./...
go build .
docker build -t pulse:dev .
```

## Tracking

Linear team **Helheim (HLM)** — epic HLM-486 (subs HLM-487..494). Empera migration is a
downstream EMP ticket.
