"""A product that brings its own wake brings its own word on it: `reread` says
whether what woke the doctor still reads red (the Hive re-runs the probes that
opened its case), and `vet` refuses a written cure by the product's own laws
(the Hive's: never crown a court, never touch the Queen's keeps). A refused
cure never runs; a close the product's re-read calls red never lands."""
from waspdoctor.doctor import Doctor
from waspdoctor.protocol import Wake
from test_cases_on_disk import HAND, READ, _Patient, _Script

CURE = ("cure", {"command": "chmod +x /usr/sbin/cron", "undo": "chmod -x /usr/sbin/cron",
                 "verify": "test -x /usr/sbin/cron", "why": "restore the execute bit"})
CLOSE = ("close", {"cause": "execute bit lost", "cause_removed": "yes", "finding": "chmod +x restored it"})


class _Ran(_Patient):
    def __init__(self):
        self.ran = []

    def run(self, cmd, **kw):
        self.ran.append(cmd)
        return super().run(cmd, **kw)


def test_the_products_reread_decides_the_close(tmp_path):
    asked = []

    def reread(wake):
        asked.append(wake.what)
        return (1, "probe drone-up reads exit 3") if len(asked) < 99 else None
    case = Doctor(_Patient(), _Script(READ, CURE, CLOSE, HAND), home=tmp_path, reread=reread).treat(
        Wake("unit_failed", "cron.service"), case_id="p-1")
    assert case.end == "handed" and asked and set(asked) == {"cron.service"}
    green = Doctor(_Patient(), _Script(READ, CURE, CLOSE, HAND), home=tmp_path,
                   reread=lambda wake: (0, "every probe reads green")).treat(
        Wake("unit_failed", "cron.service"), case_id="p-2")
    assert green.end == "closed", green.refusals


def test_a_reread_of_none_leaves_the_loops_own(tmp_path):
    case = Doctor(_Patient(), _Script(READ, CURE, CLOSE, HAND), home=tmp_path,
                  reread=lambda wake: None).treat(Wake("unit_failed", "cron.service"), case_id="p-3")
    assert case.end in ("closed", "handed")      # the loop's own re-read answered: no crash on None


def test_the_products_vet_refuses_a_cure_and_it_never_runs(tmp_path):
    p = _Ran()
    case = Doctor(p, _Script(READ, CURE, HAND), home=tmp_path,
                  vet=lambda command, undo: "a written fix never touches the Queen's keeps"
                  if "cron" in command else "").treat(Wake("unit_failed", "cron.service"), case_id="p-4")
    assert not case.cures and "chmod +x /usr/sbin/cron" not in p.ran
    assert any("Queen's keeps" in r["why"] for r in case.refusals if r["kind"] == "cure")
