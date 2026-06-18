# pulse — active

_Last updated: 2026-06-18_

## What this is

Heartbeat / cron-liveness monitoring primitive that replaced 14 Sentry cron monitors
(all `empera-*`). Dumb Go exporter + Nest/Go/Python clients; alerting via the Ops
Control Plane. Design: `kb/plans/active/0001-pulse-heartbeat-monitoring-primitive.md`.
Onboarding: `docs/onboarding.md`.

## Status (2026-06-18) — epic HLM-486 DONE, fully live

- **Server** LIVE at `pulse.helhe.im` (hardened over 6 codex rounds). Nest/Go/Python
  clients merged; deploy live (k8s-gitops #292).
- **Ops Control Plane (HLM-489) DONE** — pulse onboarded as a generated-source service:
  PromRule (`PulseMonitorLate`/`Hung`/`Failed`), Grafana dashboard, Alertmanager routes,
  delivery + image-digest + deploy-status + spend lane (k8s-gitops #293 & #294). All
  verified live in-cluster.
- **empera cutover DONE** — `v1.3.23`, 13 crons on `@Pulse`, all 14 Sentry monitors deleted.
- **npm publish (HLM-497) DONE** — `@achrononlimited/pulse-nest@0.1.0` public on npm.
- **Open:** EMP-928 — swap empera's vendored client for the published package (own prod
  deploy; empera healthy on the vendored client meanwhile). Tracked in Linear.

## Decisions

- Codename `pulse`. Store **bbolt**. Auth: per-project bearer tokens (`PULSE_TOKENS`),
  TTL auto-expiry 30d. `next_expected_at` computed client-side; Prometheus pull.
- **npm scope is `@achrononlimited`** (the `achronon` scope was unavailable). The npm
  account `achronon` owns the `achrononlimited` org.
- **Grafana dashboards are operator-gated**: each generated dashboard needs its own
  ArgoCD app (`ops-control-plane-generated-<svc>-dashboard`, mirror the IWT one) +
  a config-only entry in the ops coverage registry. Not auto-rolled-out.

## Gotchas (this session)

- **npm publish + 2FA:** granular tokens CANNOT bypass per-write 2FA (CI hits `EOTP`);
  classic Automation tokens aren't offered on newer accounts. For CI publish, set the
  publishing account's 2FA to "Authorization only". v0.1.0 went out via a *local* passkey
  publish (`npm publish --provenance=false` — local has no OIDC, so no provenance).
- **Hiding a contributor:** the npm/GHCR "Contributors" widget mirrors the repo commit
  graph. **Squash-merge stamps the commit with the PR author** → re-adds them. Use a local
  `git merge --ff-only` + direct push (preserves Achronon authorship) instead. Re-author
  stragglers + push a fresh commit to nudge GitHub's contributor cache (async, can lag).

## Links

- Repo: https://github.com/Achronon/pulse (public)
- npm: https://www.npmjs.com/package/@achrononlimited/pulse-nest
- Linear: epic HLM-486 (DONE) · HLM-497 (publish, DONE) · EMP-928 (empera cutover)
