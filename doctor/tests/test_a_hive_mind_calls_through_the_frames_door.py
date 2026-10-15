"""A Hive's mind is reached through its frame's door (the drone's loopback
door), never the court: the door says where a call of the product's kind
would go and that model's card, and the call goes under a path that asks for
that model, sampling by the card. Nothing is granted and nothing said done.
A door that says no slot holds a call this long is TooLong."""
import base64
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest
from waspdoctor.minds import HiveMind
from waspdoctor.protocol import TooLong


def _door(too_long=False):
    seen = {"reads": [], "calls": [], "gets": []}

    class H(BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def _send(self, code, body):
            raw = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_GET(self):
            seen["gets"].append(self.path)
            self._send(404, {})

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
            if (body.get("hive") or {}).get("read"):
                seen["reads"].append(body["hive"])
                if too_long:
                    return self._send(413, {"error": "too_long", "why": "no slot holds it"})
                return self._send(200, {"model": "the-seated-one", "node": "f1",
                                        "sampler_params": {"temperature": 0.5, "top_k": 9, "source": "maker"}})
            path = self.path.split("/")
            hive = json.loads(base64.urlsafe_b64decode(path[2] + "=" * (-len(path[2]) % 4)))
            seen["calls"].append((hive, body))
            self._send(200, {"choices": [{"message": {"role": "assistant", "content": "ok"}}], "usage": {}})

    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_port}", seen


def test_a_mind_calls_through_the_frames_door_by_its_card():
    srv, url, seen = _door()
    try:
        r = HiveMind(url, kind="diagnose", urgency=5, quality=0.85, wait_s=5).chat(
            [{"role": "user", "content": "x"}])
    finally:
        srv.shutdown()
    assert r.text == "ok", r
    assert seen["reads"] and seen["reads"][0]["kind"] == "diagnose" and seen["reads"][0]["class"] == "background", seen
    hive, body = seen["calls"][0]
    assert hive["model"] == "the-seated-one" and hive["kind"] == "diagnose", hive
    assert body["model"] == "the-seated-one" and body["temperature"] == 0.5 and body["top_k"] == 9, body
    assert "source" not in body and "id_slot" not in body, body
    assert not seen["gets"], f"the court's router was asked: {seen['gets']}"


def test_a_door_that_no_slot_holds_the_call_on_is_too_long():
    srv, url, _seen = _door(too_long=True)
    try:
        with pytest.raises(TooLong):
            HiveMind(url, "m", wait_s=5).chat([{"role": "user", "content": "x"}])
    finally:
        srv.shutdown()
