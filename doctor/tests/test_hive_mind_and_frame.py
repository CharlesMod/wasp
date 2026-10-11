"""The Hive's mind and frame, carried by the loop so every product reaches a
Hive one way: the router's 413 too_long is TooLong (the loop trims, never
waits), and a frame's command is a `hive run` job whose closing line gives
the exit."""
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from waspdoctor.minds import HiveMind
from waspdoctor.patient import Hive
from waspdoctor.protocol import TooLong


def test_the_routers_too_long_is_too_long():
    class H(BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(413); self.send_header("Content-Type", "application/json"); self.end_headers()
            self.wfile.write(json.dumps({"error": "too_long", "detail": "no engine's slot holds it"}).encode())

        def log_message(self, *a):
            pass
    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    try:
        with pytest.raises(TooLong):
            HiveMind(f"http://127.0.0.1:{srv.server_port}", "m", wait_s=5).chat([{"role": "user", "content": "x"}])
    finally:
        srv.shutdown()


def test_a_frames_command_is_a_job_and_its_exit_the_jobs(tmp_path):
    fake = tmp_path / "hive"
    fake.write_text("#!/bin/sh\necho \"$2 :: $3\"\necho 'frame-a: failed in 0.2s, exit 3 (job ab12)'\n")
    fake.chmod(0o755)
    r = Hive("frame-a", hive=str(fake)).run("systemctl is-active drone")
    assert r.code == 3 and r.out == "frame-a :: systemctl is-active drone"


def test_a_mind_asks_the_router_for_its_kind_naming_no_model():
    """A product that names the kind of work, not a model, asks the router for
    that kind at its urgency and quality floor, and the call names the model
    the router seated it on."""
    asked, sent = [], []

    class H(BaseHTTPRequestHandler):
        def do_GET(self):
            from urllib.parse import parse_qsl, urlparse
            asked.append(dict(parse_qsl(urlparse(self.path).query)))
            body = {"url": f"http://127.0.0.1:{srv.server_port}", "model": "the-seated-one", "slot": 0}
            self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
            self.wfile.write(json.dumps(body).encode())

        def do_POST(self):
            n = int(self.headers.get("Content-Length") or 0)
            body = json.loads(self.rfile.read(n) or b"{}")
            if self.path.endswith("/chat/completions"):
                sent.append(body)
                out = {"choices": [{"message": {"role": "assistant", "content": "ok"}}], "usage": {}}
            else:
                out = {"ok": True}
            self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
            self.wfile.write(json.dumps(out).encode())

        def log_message(self, *a):
            pass
    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    try:
        mind = HiveMind(f"http://127.0.0.1:{srv.server_port}", "", kind="diagnose", urgency=5, quality=0.85,
                        cls="batch", wait_s=5)
        mind.chat([{"role": "user", "content": "x"}])
    finally:
        srv.shutdown()
    q = asked[0]
    assert q.get("kind") == "diagnose" and float(q["urgency"]) == 5 and float(q["quality"]) == 0.85, q
    assert "model" not in q, q
    assert sent and sent[0].get("model") == "the-seated-one", sent
