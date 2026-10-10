"""A Hive frame held on the court while it is mended, and returned: the doctor's
triage on the Hive (the court's POST /v1/drones/{frame}/hold and /return), faked
at its HTTP boundary."""
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from waspdoctor.court import Court
from waspdoctor.doctor import Doctor
from waspdoctor.protocol import Wake
from waspdoctor.protocol import Result
from test_cases_on_disk import HAND, READ, _Patient, _Script


class _Mended(_Patient):
    """The unit reads active once the cure has run."""
    def run(self, cmd, *, timeout=60, user="root", input=None):
        if cmd.startswith("systemctl show -p ActiveState"):
            return Result(0, "ActiveState=active\nResult=success\nType=simple")
        return super().run(cmd, timeout=timeout, user=user, input=input)


@pytest.fixture
def court():
    got, answer = [], {"code": 200}

    class H(BaseHTTPRequestHandler):
        def do_GET(self):
            got.append((self.path, None))
            body = json.dumps({"quarantined": answer.get("quarantined", [])}).encode()
            self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
            self.wfile.write(body)

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
            got.append((self.path, body))
            self.send_response(answer["code"]); self.send_header("Content-Type", "application/json"); self.end_headers()
            self.wfile.write(b'{"ok": true}' if answer["code"] == 200 else b'{"error": "db"}')

        def log_message(self, *a):
            pass

    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{srv.server_port}", got, answer
    srv.shutdown()


HOLD = ("hold", {"why": "its engine is dead under its pin; work goes round it while it is mended"})
RETURN = ("return", {"why": "the engine answers again"})
CURE = ("cure", {"command": "chmod +x /usr/sbin/cron", "undo": "chmod -x /usr/sbin/cron",
                 "verify": "systemctl is-active cron", "why": "restore the execute bit"})
CLOSE = ("close", {"cause": "execute bit lost", "cause_removed": "yes", "finding": "chmod +x restored it"})


def test_no_court_no_hold_in_the_kit(tmp_path):
    mind = _Script(HAND)
    Doctor(_Patient(), mind, home=tmp_path).treat(Wake("unit_failed", "cron.service"), case_id="h-0")
    assert "hold" not in mind.kits[0] and "return" not in mind.kits[0]


def test_a_held_frame_is_returned_by_the_mind_and_the_court_hears_both_with_the_case(tmp_path, court):
    url, got, _ = court
    mind = _Script(HOLD, RETURN, HAND)
    case = Doctor(_Patient(), mind, home=tmp_path, court=Court(url, "sick")).treat(
        Wake("unit_failed", "cron.service"), case_id="h-1")
    assert "hold" in mind.kits[0] and "return" not in mind.kits[0]
    assert "return" in mind.kits[1] and "hold" not in mind.kits[1]           # held: the kit offers its undo
    assert [p for p, b in got if b is not None] == ["/v1/drones/sick/hold", "/v1/drones/sick/return"]
    assert [b for _, b in got if b is not None][0] == {"case": "h-1", "why": HOLD[1]["why"]}
    assert [a["act"] for a in case.court_acts] == ["hold", "return"] and not case.held


def test_a_close_returns_the_frame_it_held(tmp_path, court):
    url, got, _ = court
    mind = _Script(HOLD, READ, CURE, CLOSE, HAND)
    case = Doctor(_Mended(), mind, home=tmp_path, court=Court(url, "sick")).treat(
        Wake("unit_failed", "cron.service"), case_id="h-2")
    assert case.end == "closed", case.refusals
    assert [p for p, b in got if b is not None] == ["/v1/drones/sick/hold", "/v1/drones/sick/return"]
    assert "mended" in [b for _, b in got if b is not None][1]["why"] and not case.held


def test_a_hand_leaves_the_frame_held_and_says_so(tmp_path, court):
    url, got, _ = court
    mind = _Script(HOLD, HAND)
    case = Doctor(_Patient(), mind, home=tmp_path, court=Court(url, "sick")).treat(
        Wake("unit_failed", "cron.service"), case_id="h-3")
    assert [p for p, b in got if b is not None] == ["/v1/drones/sick/hold"] and case.held
    assert "held" in case.hand["step"] and "/v1/drones/sick/return" in case.hand["step"]


def test_a_court_that_refuses_leaves_the_frame_unheld(tmp_path, court):
    url, got, answer = court
    answer["code"] = 500
    mind = _Script(HOLD, HAND)
    case = Doctor(_Patient(), mind, home=tmp_path, court=Court(url, "sick")).treat(
        Wake("unit_failed", "cron.service"), case_id="h-4")
    assert not case.held and "hold" in mind.kits[1]
    said = [m for m in case.transcript if m.get("role") == "tool"][0]["content"]
    assert said.startswith("refused") and "500" in said


def test_a_held_case_exports_the_court_tools_it_was_offered(tmp_path, court):
    url, _, _ = court
    Doctor(_Patient(), _Script(HOLD, RETURN, HAND), home=tmp_path, court=Court(url, "sick")).treat(
        Wake("unit_failed", "cron.service"), case_id="h-5")
    rec = json.loads(_export(tmp_path))
    assert {"hold", "return"} <= {t["function"]["name"] for t in rec["tools"]}
    assert [a["act"] for a in rec["meta"]["court_acts"]] == ["hold", "return"]


def _export(home):
    import io
    from waspdoctor.doctor import export_cases
    out = io.StringIO()
    export_cases(home, out)
    return out.getvalue()


QROWS = [{"key": "k-engine", "job": "J1", "reason": "3 crashes in 10 min", "node": "Sick", "class": "fix",
          "cmd": ["sh", "-c", "restart engine"]},
         {"key": "k-other", "job": "J2", "reason": "3 crashes", "node": "elsewhere", "class": "fix", "cmd": ["true"]}]


QREAD = ("diagnose", {"cause": "the engine restart crashed three times and was quarantined",
                      "evidence": "k-engine: job J1 (fix) `sh -c restart engine`: 3 crashes in 10 min"})


def test_the_frames_quarantine_is_in_view_and_only_its_keys_may_be_released(tmp_path, court):
    url, got, answer = court
    answer["quarantined"] = QROWS
    release = ("release", {"key": "k-engine", "why": "the engine's pin is fixed; the restart may run again"})
    mind = _Script(QREAD, ("release", {"key": "k-other", "why": "not this frame's action at all"}), release, HAND)
    case = Doctor(_Patient(), mind, home=tmp_path, court=Court(url, "sick")).treat(
        Wake("quarantined", "sick"), case_id="h-6")
    opening = case.transcript[1]["content"]
    assert "k-engine" in opening and "3 crashes in 10 min" in opening and "k-other" not in opening
    assert "release" not in mind.kits[0]                                   # triage: name the mechanism first
    kit = [t for t in case.turn_log[1]["tools"]]
    assert "release" in kit
    posts = [(p, b) for p, b in got if b is not None]
    assert posts == [("/v1/quarantine/release", {"key": "k-engine", "case": "h-6", "why": release[1]["why"]})]
    assert [a["act"] for a in case.court_acts] == ["release"]
    said = [m["content"] for m in case.transcript if m.get("role") == "tool"]
    assert said[1].startswith("refused") and "k-engine" in said[1]


def test_the_release_tool_names_only_the_frames_keys(tmp_path, court):
    url, _, answer = court
    answer["quarantined"] = QROWS
    seen = {}

    class _Spy(_Script):
        def chat(self, msgs, tools=None, max_tokens=0, effort=""):
            for t in tools or []:
                if t["function"]["name"] == "release":
                    seen["enum"] = t["function"]["parameters"]["properties"]["key"]["enum"]
            return super().chat(msgs, tools, max_tokens, effort)
    Doctor(_Patient(), _Spy(QREAD, HAND), home=tmp_path, court=Court(url, "sick")).treat(
        Wake("quarantined", "sick"), case_id="h-7")
    assert seen["enum"] == ["k-engine"]


def test_no_quarantine_no_release(tmp_path, court):
    url, _, _ = court
    mind = _Script(READ, HAND)
    Doctor(_Patient(), mind, home=tmp_path, court=Court(url, "sick")).treat(Wake("unit_failed", "cron.service"), case_id="h-8")
    assert all("release" not in k for k in mind.kits)
