# 0001 — `pulse`: reusable heartbeat / cron-liveness monitoring primitive

> Status: **Design / pre-implementation** · Owner: HLM · Created 2026-06-17
> Codename `pulse` is a placeholder — rename before repo init if a better one lands.

## Goal

Replace per-monitor SaaS cron monitoring (currently 14 Sentry cron monitors, all
`empera-*`) with a **self-hosted, low-footprint, open-source heartbeat primitive** that:

1. Works identically across our whole stack via a **similar decorator** in **NestJS (TS)**,
   **Go**, and **Python** — plus a plain `curl` path for shell crons.
2. Relies on infra we already run: **Prometheus + Alertmanager + Grafana + Loki**
   (the Ops Control Plane). No new alerting brain, no Django/Postgres.
3. Is reusable again and again as an internal platform primitive. Empera adopts it to
   retire its 14 Sentry crons; the Ops Control Plane's own scheduled jobs check in too.

## Core principle: the server stays DUMB

The single decision that makes BUILD cheap and correct: **the `pulse` server does not
alert, does not parse cron expressions, does not own notification routing.** It only:

- receives check-ins (`register` / `start` / `ok` / `fail`),
- remembers last-known state per monitor (persisted), and
- re-exposes that state as **Prometheus metrics**.

Everything else is delegated to tools that already do it better:

| Concern              | Owner                                            |
|----------------------|--------------------------------------------------|
| "Is it late / down?" | **Alertmanager** rules over `pulse` metrics      |
| Who gets paged       | **Alertmanager** tiered routing (HLM-401)        |
| Dedup / flap / nag   | Alertmanager grouping / inhibition / repeat      |
| Dashboard / history  | **Grafana** (TSDB-backed)                         |
| Failure forensics    | **Loki** (job logs to stdout, alert links query) |
| Cron-expr evaluation | **The client** (it already declares the schedule)|

**Re-evaluate trigger:** if implementation pressures the server to grow a cron engine,
a ping-log store with a UI, pause/resume CRUD, or projects/RBAC — STOP. At that point
self-host Healthchecks.io and scrape its Prometheus endpoint instead. The build only
wins while the server is a dumb exporter.

## Non-goals

- Not an uptime/HTTP-endpoint monitor (that's blackbox-exporter / Gatus territory).
- Not a status page for non-eng stakeholders (v1 is eng-facing Grafana).
- No per-check pause UI (use Alertmanager silences).
- No server-side cron-expression parser (see "schedule" below).

## Architecture

```
  ┌─────────────┐  register/start/ok/fail (HTTPS + token)   ┌──────────────┐
  │ job + client │ ─────────────────────────────────────▶  │ pulse server │
  │  decorator   │                                          │  (Go, dumb)  │
  └─────────────┘                                           │  bbolt state │
   Nest / Go / Py / curl                                    └──────┬───────┘
   (any runtime: Railway,                                          │ /metrics
    Vercel, VPS, in-cluster)                                       ▼
                                                          ┌──────────────────┐
                                                          │   Prometheus     │
                                                          │  (ServiceMonitor)│
                                                          └────────┬─────────┘
                                                                   ▼
                                              Alertmanager rules ──▶ tiered routing
                                              Grafana dashboard      (Slack / PD)
```

`pulse` lives **in the Helheim cluster** (the infra we trust most) behind a public,
token-authed ingress so external clients (empera on Railway, serverless birrday) can
reach it. It is itself scraped by Prometheus.

## Check-in protocol (HTTP)

Outbound-only HTTP so it works from anywhere. Bearer token per project.

```
POST /v1/checkin/<monitor-slug>
Authorization: Bearer <PULSE_TOKEN>
Body (JSON):
  { "status": "register" | "start" | "ok" | "fail",
    "next_expected_at": 1750000000,   # unix secs (client-computed; full cron precision)
    "interval_seconds": 300,          # OR this (server adds to now; simple-period path)
    "grace_seconds": 120,
    "max_runtime_seconds": 240,
    "duration_seconds": 1.8,          # on ok/fail
    "project": "empera" }
```

- Clients **fail open**: a failed ping must never break the real job (swallow + log).
- `register` is sent once at process start per monitor (carries schedule metadata +
  an initial `next_expected_at`). Persisted so "registered but never ran" is detectable
  even before the first run.
- A `start` with no following `ok` within `max_runtime` ⇒ hung-job detection.

### Schedule: client computes `next_expected_at` (the key refinement)

This closes Healthchecks' biggest advantage (true cron-expression + TZ awareness)
**without** a server-side cron engine. The client already declares its schedule
(`@nestjs/schedule`, `robfig/cron`, `croniter`), so it computes "next run after now"
locally and pushes the absolute timestamp. The server just stores it.

- Complex schedules (`0 9 * * 1-5`, business hours, monthly) work — they'd false-alarm
  under a flat-interval model.
- Dead-man's-switch holds: if the process dies, `next_expected_at` freezes at its last
  pushed value while `time()` marches on ⇒ the late rule fires.
- Simple path: clients may instead send `interval_seconds`; the server computes
  `next_expected_at = now + interval` (trivial arithmetic, NOT a cron parser). 90% case.

## Metrics exposed (`/metrics`)

```
pulse_last_success_timestamp_seconds{monitor,project}   gauge
pulse_last_start_timestamp_seconds{monitor,project}     gauge
pulse_last_failure_timestamp_seconds{monitor,project}   gauge
pulse_next_expected_timestamp_seconds{monitor,project}  gauge
pulse_grace_seconds{monitor,project}                    gauge
pulse_max_runtime_seconds{monitor,project}              gauge
pulse_last_duration_seconds{monitor,project}            gauge
pulse_runs_total{monitor,project,status}                counter
```

## Alerting (Alertmanager — generic, scales to N monitors with 0 new rules)

```promql
# 1. Late / missed (also covers process-dead dead-man's-switch)
- alert: PulseMonitorLate
  expr: time() > pulse_next_expected_timestamp_seconds + pulse_grace_seconds

# 2. Hung — started, exceeded max runtime, never reported ok
- alert: PulseMonitorHung
  expr: pulse_last_start_timestamp_seconds > pulse_last_success_timestamp_seconds
        and time() - pulse_last_start_timestamp_seconds > pulse_max_runtime_seconds

# 3. Explicit failure reported
- alert: PulseMonitorFailed
  expr: increase(pulse_runs_total{status="fail"}[10m]) > 0
```

Adding a new cron in any language requires **zero** new alert config — these rules are
generic over the `monitor` label. Severity/routing comes from labels the client sets
(`project`, optional `tier`) folded into HLM-401 tiered routing.

## Client libraries (the real reusable surface)

Identical ergonomics; each is ~30–60 LOC, fail-open, configurable base URL + token.

**Nest** — drops straight in where `@SentryCron` is today:
```ts
@Pulse('empera-booking-expiry', { schedule: '*/5 * * * *', grace: '2m', maxRuntime: '4m' })
@Cron('*/5 * * * *')
async expireStaleBookings() { /* ... */ }
```
A `PulseModule` batches `register` calls on bootstrap; the decorator wraps invocation
with start → run → ok(duration)/fail, computing `next_expected_at` via a cron lib.

**Go:**
```go
pulse.Run(ctx, "limesindex-refresh",
  pulse.Opts{Schedule: "0 * * * *", Grace: 2*time.Minute, MaxRuntime: 10*time.Minute},
  func(ctx context.Context) error { return refresh(ctx) })
```

**Python:**
```python
@pulse("ingest-nightly", schedule="0 3 * * *", grace="10m", max_runtime="1h")
def run_ingest(): ...
```

**Shell / anything:** `curl -fsS -H "Authorization: Bearer $T" \
  -d '{"status":"ok","interval_seconds":3600}' $PULSE_URL/v1/checkin/<slug>`

## State / persistence

- **bbolt** (pure-Go, embedded, no CGO, single file on a small PVC). Data model is a
  trivial `map[slug]MonitorState`, so a KV store is right; SQLite/Postgres is overkill.
- Persistence matters so a server restart doesn't blank a daily cron's last-seen and
  fire a false "missing" before the next check-in.
- **TTL auto-expiry**: a monitor with no check-in for `N × interval` (configurable,
  default e.g. 30d) is dropped, so decommissioned crons stop phantom-alerting — the
  Healthchecks gap that matters most for a permanent primitive.

## Deployment

- New repo `pulse` (monorepo): `server/` (Go) + `clients/{nest,go,python}/`.
- Deploy the server via **k8s-gitops-helheim** (new ArgoCD app): Deployment + Service +
  ServiceMonitor + Ingress (public, token-auth) + sealed-secret for `PULSE_TOKEN` +
  small PVC for bbolt.
- Alert rules + Grafana dashboard land in the Ops Control Plane (generated-rules path).

## What we forgo vs Healthchecks.io (and the mitigation)

| Healthchecks gives | `pulse` answer |
|---|---|
| Cron-expr + TZ awareness | Client pushes `next_expected_at` (closed) |
| Hung-job detection | Alert rule #2 (one rule) |
| Per-run ping log + payload | Loki (job logs to stdout; alert links query) |
| Polished status UI / badges | Grafana panels (eng-facing); no public page |
| Per-check pause | Alertmanager silences |
| Notifications / escalation | Alertmanager tiered routing — **one** plane, net win |
| Dedup / flap / nag | Alertmanager grouping/inhibition |
| Historical stats / SLO | Prometheus TSDB + Grafana — richer |

Net forgone: bundled status UI + per-check pause. Low value for an internal eng
primitive. Verdict: **BUILD** (server stays dumb).

## Security

- Public ingress with per-project static bearer tokens (sealed-secret / 1Password),
  rate-limited. Slug namespacing per project (`empera-*`, `birrday-*`, `ops-*`).
- v1 token-per-project; revisit mTLS / short-lived tokens if the surface grows.

## Sequencing (sub-tickets)

1. **Server** (Go dumb exporter) + repo scaffold + kb + AGENTS.md + git init.
2. **Deploy** ArgoCD app in k8s-gitops-helheim (ingress, sealed-secret, PVC, ServiceMonitor).
3. **Alerting** PromRules (3 generic) + Grafana dashboard, into HLM-401 routing.
4. **Nest client** `@Pulse` (what empera consumes).
5. **Go client** `pulse.Run`.
6. **Python client** `@pulse`.
7. **Dogfood**: ops-control-plane scheduled jobs check in (prove before empera adopts).
8. **Docs / onboarding** (curl recipe, per-language quickstart, registry row).

Downstream (separate **EMP** ticket, not HLM): empera migrates its 14 `@SentryCron`
call sites to `@Pulse`, schedules unchanged; then deletes the Sentry cron monitors.

## Defaults chosen (override at impl time)

- Codename `pulse`; embedded store **bbolt**; tokens per-project; TTL expiry 30d.
- Server scraped by Prometheus (pull); clients push to server (no Pushgateway).
