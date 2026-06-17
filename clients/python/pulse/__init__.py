"""Python client for the pulse heartbeat / cron-liveness monitor.

Wrap scheduled work with check-ins (start -> ok/fail). The next expected run time
is computed locally from the job's cron expression (or interval) and pushed to the
server, so the server needs no cron engine. All check-ins are FAIL-OPEN: a
monitoring error never affects the wrapped work.

Configure via env (PULSE_URL, PULSE_TOKEN, PULSE_PROJECT) or a Client.

    import pulse

    @pulse.pulse("ingest-nightly", schedule="0 3 * * *", grace="10m", max_runtime="1h")
    def run_ingest():
        ...

    # or as a context manager:
    with pulse.monitor("ingest-nightly", schedule="0 3 * * *"):
        ...
"""

from __future__ import annotations

import contextlib
import functools
import logging
import os
import time
from datetime import datetime
from typing import Callable, Optional, Union

import httpx
from croniter import croniter

__all__ = ["Client", "pulse", "monitor", "register", "set_default", "Duration"]

log = logging.getLogger("pulse")

Duration = Union[int, float, str, None]

_UNITS = {"s": 1, "m": 60, "h": 3600, "d": 86400}


def _to_seconds(v: Duration) -> int:
    """Coerce an int/float (seconds) or a string like '10m'/'2h'/'90s' to seconds."""
    if v is None:
        return 0
    if isinstance(v, (int, float)):
        return int(v)
    s = v.strip()
    if not s:
        return 0
    if s[-1] in _UNITS:
        try:
            return int(float(s[:-1]) * _UNITS[s[-1]])
        except ValueError:
            pass
    try:
        return int(float(s))
    except ValueError:
        log.warning("pulse: invalid duration %r; treating as 0", v)
        return 0


class Client:
    """Talks to a pulse server. Reusable and thread-safe for sending check-ins."""

    def __init__(
        self,
        base_url: Optional[str] = None,
        token: Optional[str] = None,
        project: Optional[str] = None,
        http: Optional[httpx.Client] = None,
        now: Optional[Callable[[], float]] = None,
    ) -> None:
        self.base_url = (base_url if base_url is not None else os.getenv("PULSE_URL", "")).rstrip("/")
        self.token = token if token is not None else os.getenv("PULSE_TOKEN", "")
        self.project = project if project is not None else os.getenv("PULSE_PROJECT", "")
        self._http = http or httpx.Client(timeout=10.0)
        self._now = now or time.time

    def _next_expected(self, schedule: Optional[str], interval_s: int) -> int:
        if schedule:
            try:
                base = datetime.fromtimestamp(self._now())  # local tz, matches cron daemons
                return int(croniter(schedule, base).get_next(datetime).timestamp())
            except Exception:  # noqa: BLE001 - invalid expr should not break the job
                log.warning("pulse: invalid schedule %r; falling back to interval", schedule)
        if interval_s:
            return int(self._now()) + interval_s
        return 0

    def _send(
        self,
        slug: str,
        status: str,
        *,
        schedule: Optional[str] = None,
        interval_s: int = 0,
        grace_s: int = 0,
        max_runtime_s: int = 0,
        project: Optional[str] = None,
        duration: float = 0.0,
    ) -> None:
        if not self.base_url:
            return  # not configured; no-op (dev/local)
        body: dict = {"status": status}
        proj = project or self.project
        if proj:
            body["project"] = proj
        nxt = self._next_expected(schedule, interval_s)
        if nxt:
            body["next_expected_at"] = nxt
        if interval_s:
            body["interval_seconds"] = interval_s
        if grace_s:
            body["grace_seconds"] = grace_s
        if max_runtime_s:
            body["max_runtime_seconds"] = max_runtime_s
        if duration:
            body["duration_seconds"] = duration
        headers = {"Authorization": f"Bearer {self.token}"} if self.token else {}
        try:
            resp = self._http.post(f"{self.base_url}/v1/checkin/{slug}", json=body, headers=headers)
            if resp.status_code >= 300:
                log.warning("pulse: check-in %s: status %s", slug, resp.status_code)
        except Exception as exc:  # noqa: BLE001 - fail open
            log.warning("pulse: check-in %s failed: %s", slug, exc)

    def register(
        self,
        slug: str,
        *,
        schedule: Optional[str] = None,
        interval: Duration = None,
        grace: Duration = None,
        max_runtime: Duration = None,
        project: Optional[str] = None,
    ) -> None:
        """Announce a monitor at startup so a job that never fires is detectable."""
        self._send(
            slug,
            "register",
            schedule=schedule,
            interval_s=_to_seconds(interval),
            grace_s=_to_seconds(grace),
            max_runtime_s=_to_seconds(max_runtime),
            project=project,
        )

    @contextlib.contextmanager
    def monitor(
        self,
        slug: str,
        *,
        schedule: Optional[str] = None,
        interval: Duration = None,
        grace: Duration = None,
        max_runtime: Duration = None,
        project: Optional[str] = None,
    ):
        """Context manager: ping start on enter, ok/fail (with duration) on exit."""
        kw = dict(
            schedule=schedule,
            interval_s=_to_seconds(interval),
            grace_s=_to_seconds(grace),
            max_runtime_s=_to_seconds(max_runtime),
            project=project,
        )
        self._send(slug, "start", **kw)
        started = self._now()
        try:
            yield
        except BaseException:
            self._send(slug, "fail", duration=self._now() - started, **kw)
            raise
        else:
            self._send(slug, "ok", duration=self._now() - started, **kw)

    def decorate(self, slug: str, **kwargs):
        """Decorator form of monitor()."""

        def wrap(fn):
            @functools.wraps(fn)
            def inner(*args, **kw):
                with self.monitor(slug, **kwargs):
                    return fn(*args, **kw)

            return inner

        return wrap


# ---- module-level default client (lazy, env-configured) --------------------

_default: Optional[Client] = None


def _std() -> Client:
    global _default
    if _default is None:
        _default = Client()
    return _default


def set_default(client: Client) -> None:
    """Override the module-level default client."""
    global _default
    _default = client


def pulse(slug: str, **kwargs):
    """Decorator using the default client: @pulse.pulse('slug', schedule=...)."""
    return _std().decorate(slug, **kwargs)


def monitor(slug: str, **kwargs):
    """Context manager using the default client."""
    return _std().monitor(slug, **kwargs)


def register(slug: str, **kwargs) -> None:
    """Register a monitor using the default client."""
    _std().register(slug, **kwargs)
