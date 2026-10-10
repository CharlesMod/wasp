"""A case whose record outgrows every seat the mind has is sent to the mind with its
older looks trimmed, its record on disk kept whole; one that outgrows them even so
is handed over, not left waiting for a seat that will never hold it."""
from waspdoctor.doctor import CUT_MAX, Doctor
from waspdoctor.protocol import Reply, TooLong, Wake
from test_cases_on_disk import HAND, _Patient

LONG = "x" * 3000


class _Fits:
    """Refuses any call longer than limit characters, as a router whose seats hold
    no more; answers the steps in turn."""
    name = "fake"

    def __init__(self, limit, *steps):
        self.limit, self.steps, self.n, self.sizes = limit, list(steps), 0, []

    def chat(self, msgs, tools=None, max_tokens=0, effort=""):
        size = sum(len(str(m.get("content") or "")) for m in msgs)
        self.sizes.append(size)
        if size > self.limit:
            raise TooLong(f"no engine's slot holds it ({size})")
        name, args = self.steps[min(self.n, len(self.steps) - 1)]
        self.n += 1
        return Reply(text="", tool_calls=[{"id": f"t{self.n}", "name": name, "arguments": args}])


class _Wordy(_Patient):
    def run(self, cmd, *, timeout=60, user="root", input=None):
        if cmd.startswith("cat /var/log/big"):
            from waspdoctor.protocol import Result
            return Result(0, LONG)
        return super().run(cmd, timeout=timeout, user=user, input=input)


def _looks(n):
    return [("look", {"cmd": f"cat /var/log/big{i}"}) for i in range(n)]


def test_a_long_case_is_trimmed_to_fit_and_its_record_kept_whole(tmp_path):
    steps = _looks(6) + [HAND]
    whole = None
    mind = _Fits(10**9, *steps)
    whole_case = Doctor(_Wordy(), mind, home=tmp_path / "a").treat(Wake("unit_failed", "cron.service"), case_id="w")
    whole = max(mind.sizes)
    mind = _Fits(whole - 4000, *steps)                       # the last call no longer fits
    case = Doctor(_Wordy(), mind, home=tmp_path / "b").treat(Wake("unit_failed", "cron.service"), case_id="t")
    assert case.end == "handed" and case.hand["step"] == "x"  # it went on to its own end
    assert case.cut >= 1 and max(mind.sizes[-1:]) <= whole - 4000
    kept = [m["content"] for m in case.transcript if m.get("role") == "tool"]
    assert all(LONG[:2000] in k for k in kept[:6])            # the record holds every look whole
    assert len(case.transcript) == len(whole_case.transcript)


def test_a_case_that_outgrows_every_seat_even_trimmed_is_handed_over(tmp_path):
    mind = _Fits(10, *_looks(3), HAND)                       # nothing fits
    case = Doctor(_Wordy(), mind, home=tmp_path).treat(Wake("unit_failed", "cron.service"), case_id="n")
    assert case.end == "handed" and case.cut == CUT_MAX + 1
    assert "outgrew every seat" in case.hand["step"]
