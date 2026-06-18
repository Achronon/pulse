# Onboarding a job to pulse

pulse is the self-hosted heartbeat / cron-liveness monitor for the Achronon stack.
A job sends **check-ins**; pulse stores last-known state and exposes Prometheus
metrics. Alertmanager fires if a job goes late / hangs / fails — pulse itself stays
dumb. (Design: [`kb/plans/active/0001-pulse-heartbeat-monitoring-primitive.md`](../kb/plans/active/0001-pulse-heartbeat-monitoring-primitive.md).)

- **Endpoint:** `https://pulse.helhe.im/v1/checkin/<slug>` (bearer-token auth — the
  only path exposed publicly).
- `/metrics` and `/healthz` are in-cluster only.
- **Fail-open:** every client swallows check-in errors so monitoring never breaks
  the job it wraps.

## 1. Pick a slug (namespacing convention)

Slugs are global and **namespaced by project**: `^[a-z0-9][a-z0-9-]{0,63}$`, prefixed
with the project name — `empera-booking-expiry`, `birrday-digest`, `ops-rule-regen`.
A scoped token may only write to monitors in its own project.

## 2. Get a token

Each project gets a bearer token, stored as the `pulse-tokens` SealedSecret
(`PULSE_TOKENS=project:token,...`). To add a project:

```bash
# in k8s-gitops-helheim, re-seal with the new project appended:
kubectl create secret generic pulse-tokens -n pulse \
  --from-literal=PULSE_TOKENS='empera:<tok>,ops:<tok>,birrday:<tok>' \
  --dry-run=client -o yaml \
| kubeseal --cert infrastructure/sealed-secrets/pub-cert.pem --format yaml \
    --scope strict --namespace pulse --name pulse-tokens \
> services/pulse/pulse-tokens.sealedsecret.yaml
```

Give the job `PULSE_URL=https://pulse.helhe.im`, `PULSE_TOKEN=<its project token>`,
`PULSE_PROJECT=<project>` as env.

## 3. Wrap the job

### NestJS / TypeScript — drop-in for `@SentryCron`

```ts
import { Cron } from '@nestjs/schedule';
import { Pulse } from '@achronon/pulse-nest'; // until published: vendored copy

@Cron('*/5 * * * *')
@Pulse('empera-booking-expiry', { schedule: '*/5 * * * *', grace: '2m', maxRuntime: '4m' })
async expireStaleBookings() { /* ... */ }
```

### Go

```go
import pulse "github.com/Achronon/pulse/clients/go"

pulse.Run(ctx, "limesindex-refresh",
  pulse.Opts{Schedule: "0 * * * *", Grace: 2*time.Minute, MaxRuntime: 10*time.Minute},
  func(ctx context.Context) error { return refresh(ctx) })
```

### Python

```python
import pulse  # env: PULSE_URL, PULSE_TOKEN, PULSE_PROJECT

@pulse.pulse("ingest-nightly", schedule="0 3 * * *", grace="10m", max_runtime="1h")
def run_ingest(): ...
```

### Shell / anything (curl)

```bash
T="$PULSE_TOKEN"; U=https://pulse.helhe.im
post(){ curl -fsS -H "Authorization: Bearer $T" -H 'content-type: application/json' \
  -d "$2" "$U/v1/checkin/$1" ; }
post my-cron '{"status":"start","interval_seconds":3600,"grace_seconds":120}'
# ... do work ...
post my-cron '{"status":"ok","interval_seconds":3600}'   # or {"status":"fail"}
```

## 4. Check-in protocol

`POST /v1/checkin/<slug>` with JSON:

| field | meaning |
|---|---|
| `status` | `register` \| `start` \| `ok` \| `fail` |
| `next_expected_at` | unix secs of the next due run (client-computed from cron — full precision) |
| `interval_seconds` | alternative to `next_expected_at`; server uses `now + interval` |
| `grace_seconds` | tolerance after `next_expected` before "late" |
| `max_runtime_seconds` | enables hung detection; omit to opt out |
| `duration_seconds` | on `ok`/`fail` |

`register` is authoritative — send it at process start so a job that never fires is
still detectable, and to clear stale schedule fields. `start` advances `next_expected`
to the following run, so a long-but-punctual run isn't flagged late (that's `hung`).

## 5. What alerts you get

Generic over the `monitor` label (no new rule per job):

- **PulseMonitorLate** — `time() > next_expected + grace` (also the dead-man's-switch).
- **PulseMonitorHung** — started, exceeded `max_runtime`, no terminal status.
- **PulseMonitorFailed** — a `fail` was reported.

Day-1 severity is `warning`; raise to `critical`/paging once a monitor has soaked.

## 6. Decommissioning

Stop sending check-ins. pulse auto-expires a monitor after its TTL (default 30d,
measured from when it was last due), so dead crons stop alerting on their own.
