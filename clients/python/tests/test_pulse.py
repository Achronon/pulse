import json

import httpx
import pulse


class Clock:
    """Monotonic test clock advancing 1s per call."""

    def __init__(self, start: float):
        self.t = start

    def __call__(self) -> float:
        v = self.t
        self.t += 1
        return v


def make_client(captured, *, fail=False, now=None):
    def handler(request: httpx.Request) -> httpx.Response:
        if fail:
            raise httpx.ConnectError("boom", request=request)
        captured.append(
            {
                "path": request.url.path,
                "body": json.loads(request.content),
                "auth": request.headers.get("authorization"),
            }
        )
        return httpx.Response(204)

    http = httpx.Client(transport=httpx.MockTransport(handler))
    return pulse.Client(
        base_url="https://pulse.test",
        token="tok",
        project="ops",
        http=http,
        now=now or (lambda: 1_700_000_000.0),
    )


def test_to_seconds():
    assert pulse._to_seconds("10m") == 600
    assert pulse._to_seconds("90s") == 90
    assert pulse._to_seconds("2h") == 7200
    assert pulse._to_seconds("1d") == 86400
    assert pulse._to_seconds(120) == 120
    assert pulse._to_seconds(None) == 0
    assert pulse._to_seconds("bogus") == 0


def test_monitor_ok():
    cap = []
    c = make_client(cap)
    with c.monitor("job", interval=300, grace=60):
        pass
    assert [r["body"]["status"] for r in cap] == ["start", "ok"]
    ok = cap[1]["body"]
    assert ok["project"] == "ops"
    assert ok["interval_seconds"] == 300
    assert ok["grace_seconds"] == 60
    assert ok["next_expected_at"] == 1_700_000_000 + 300
    assert cap[1]["auth"] == "Bearer tok"


def test_monitor_fail_propagates():
    cap = []
    c = make_client(cap)
    try:
        with c.monitor("job", interval=60):
            raise ValueError("kaboom")
    except ValueError:
        pass
    else:
        raise AssertionError("exception should propagate")
    assert cap[-1]["body"]["status"] == "fail"


def test_duration_recorded():
    cap = []
    c = make_client(cap, now=Clock(1_000_000.0))
    with c.monitor("job", interval=60):
        pass
    assert cap[1]["body"].get("duration_seconds", 0) > 0


def test_decorator():
    cap = []
    c = make_client(cap)

    @c.decorate("job", interval=60)
    def work(x):
        return x * 2

    assert work(21) == 42
    assert [r["body"]["status"] for r in cap] == ["start", "ok"]


def test_fail_open():
    cap = []
    c = make_client(cap, fail=True)
    ran = False
    with c.monitor("job", interval=60):
        ran = True
    assert ran  # body executed despite check-in transport errors


def test_register():
    cap = []
    c = make_client(cap)
    c.register("job", schedule="0 * * * *", grace="2m")
    assert len(cap) == 1
    assert cap[0]["body"]["status"] == "register"
    assert cap[0]["body"]["next_expected_at"] > 0
    assert cap[0]["body"]["grace_seconds"] == 120


def test_next_expected_from_schedule():
    c = make_client([])
    nxt = c._next_expected("*/5 * * * *", 0)
    assert nxt > 1_700_000_000
    assert nxt % 300 == 0  # aligned to 5-minute boundary
