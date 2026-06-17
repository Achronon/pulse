# pulse — active

_Last updated: 2026-06-17_

## What this is

Heartbeat / cron-liveness monitoring primitive replacing 14 Sentry cron monitors
(all `empera-*`). Dumb Go exporter + Nest/Go/Python clients; alerting via the Ops
Control Plane. Full design: `kb/plans/active/0001-pulse-heartbeat-monitoring-primitive.md`.

## Status (2026-06-17)

Building everything in one session — user opted "go all the way": build + live deploy +
empera cutover + delete Sentry monitors **today**, **no soak** (risk accepted: a fresh
server briefly monitors real payout/booking jobs before bake-in).

- **HLM-487 server** — IN PROGRESS. `server/` Go exporter: store (bbolt) + metrics
  collector + authed check-in API + graceful main + tests + Dockerfile. On branch
  `feature/hlm-487-pulse-server`.
- HLM-490/491/492 clients, HLM-488 deploy, HLM-489 alerting, HLM-493 dogfood,
  HLM-494 docs — pending.
- EMP empera cutover + Sentry monitor deletion — pending (downstream).

## Decisions

- Codename `pulse` (placeholder, kept). Store: **bbolt** (single file, no CGO).
- Auth: per-project bearer tokens via `PULSE_TOKENS=project:token,...` (+ optional
  wildcard `PULSE_TOKEN`). TTL auto-expiry default 30d.
- `next_expected_at` computed client-side → complex cron schedules work with a dumb
  server. Prometheus pull (no Pushgateway).

## Links

- Repo: https://github.com/Achronon/pulse (public)
- Linear: epic HLM-486 (subs HLM-487..494)
