# pulse — active

_Last updated: 2026-06-18_

## What this is

Heartbeat / cron-liveness monitoring primitive that replaced 14 Sentry cron monitors
(all `empera-*`). Dumb Go exporter + Nest/Go/Python clients; alerting via the Ops
Control Plane. Design: `kb/plans/active/0001-pulse-heartbeat-monitoring-primitive.md`.
Onboarding: `docs/onboarding.md`.

## Status (2026-06-18) — LIVE

- **Server** (HLM-487) LIVE at `pulse.helhe.im` — hardened over 6 codex rounds.
- **Clients** (HLM-490/491/492) merged. **Deploy** (HLM-488, k8s-gitops #292) live.
- **Alerts** (HLM-489) live as a service-local PrometheusRule (severity `warning`).
- **empera cutover** DONE — released `v1.3.23`; 13 crons on `@Pulse`, **verified**
  (10 monitors reporting `ok`, 0 fail). **All 14 Sentry cron monitors deleted.**
- **Open follow-ups:** HLM-489 (Grafana dashboard + full ops-control-plane generator
  onboarding + raise severity after soak), HLM-494 (docs — this), HLM-497 (publish
  `@achronon/pulse-nest`, drop empera's vendored client). HLM-493 closed (superseded
  by live empera traffic).

## Decisions

- Codename `pulse` (placeholder, kept). Store: **bbolt** (single file, no CGO).
- Auth: per-project bearer tokens via `PULSE_TOKENS=project:token,...` (+ optional
  wildcard `PULSE_TOKEN`). TTL auto-expiry default 30d.
- `next_expected_at` computed client-side → complex cron schedules work with a dumb
  server. Prometheus pull (no Pushgateway).

## Links

- Repo: https://github.com/Achronon/pulse (public)
- Linear: epic HLM-486 (subs HLM-487..494)
