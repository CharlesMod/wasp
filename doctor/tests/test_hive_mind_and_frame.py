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
