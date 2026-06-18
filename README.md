# pulse

Self-hosted, low-footprint heartbeat / cron-liveness monitoring for the Achronon stack.
Replaces per-monitor SaaS cron monitoring (e.g. Sentry crons) with a **dumb Prometheus
exporter** + thin client decorators for **NestJS, Go, and Python** (+ a `curl` path).

Alerting, dashboards, dedup, routing, and forensics are delegated to the existing Ops
Control Plane (**Prometheus + Alertmanager + Grafana + Loki**) — `pulse` only remembers
check-in state and exposes it as metrics.

> **Status: LIVE** at `pulse.helhe.im`. **[Onboard a job →
> `docs/onboarding.md`](docs/onboarding.md)** · full design in
> [`kb/plans/active/0001-pulse-heartbeat-monitoring-primitive.md`](kb/plans/active/0001-pulse-heartbeat-monitoring-primitive.md).
> Tracked under Linear team **Helheim (HLM)**, epic HLM-486.

## How it works (one screen)

```
job + @Pulse/pulse.Run/@pulse  ──register/start/ok/fail──▶  pulse server (Go, bbolt)
                                                                   │ /metrics
   Prometheus ◀───── scrape ──────────────────────────────────────┘
       │
       ├─▶ Alertmanager  (3 generic rules: Late / Hung / Failed → tiered routing)
       └─▶ Grafana       (status + duration dashboards)
```

The client computes `next_expected_at` from its own cron expression, so complex
schedules work without a server-side cron engine. The server stays dumb on purpose.

## Layout (planned)

```
server/          Go dumb exporter (check-in API, bbolt state, /metrics, TTL expiry)
clients/nest/    @Pulse decorator (TS)
clients/go/      pulse.Run wrapper
clients/python/  @pulse decorator
```
