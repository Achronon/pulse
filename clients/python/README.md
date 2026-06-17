# pulse Python client

Wrap scheduled work with heartbeat check-ins. Fail-open: a monitoring error never
affects your job.

```python
import pulse  # configured from env: PULSE_URL, PULSE_TOKEN, PULSE_PROJECT

@pulse.pulse("ingest-nightly", schedule="0 3 * * *", grace="10m", max_runtime="1h")
def run_ingest():
    ...
```

Or as a context manager:

```python
with pulse.monitor("ingest-nightly", schedule="0 3 * * *", grace="10m"):
    do_work()
```

Both ping `start`, run the body, then `ok` (with duration) or `fail`. The next expected
run time is computed locally from `schedule` (or `interval`) and pushed to the server —
the server does no cron parsing. Durations accept seconds (`300`) or strings (`"10m"`).

Announce a monitor at startup so a job that never fires is still detectable:

```python
pulse.register("ingest-nightly", schedule="0 3 * * *", grace="10m")
```

Explicit client:

```python
c = pulse.Client(base_url="https://pulse.example", token="…", project="empera")
with c.monitor("slug", interval=300):
    ...
```
