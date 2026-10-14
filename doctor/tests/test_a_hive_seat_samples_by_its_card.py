"""A seat on the Hive samples by the seated model's card, as every Hive call
does (the Hive's ops/SAMPLERS.md): HiveMind sent temperature 0.0 to whatever
model the router seated it on, a path around the card. The card is the
court's (GET /v1/models, its sampler_params), read where the seat is asked;
the reply says what it sent."""
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from waspdoctor.minds import HiveMind

CARD = {"temperature": 1.0, "top_p": 0.95, "top_k": 64, "min_p": 0, "source": "the maker"}


def _hive():
    sent = []

    class H(BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def _json(self, body, code=200):
            raw = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_GET(self):
            if self.path.startswith("/v1/route"):
                self._json({"url": f"http://127.0.0.1:{self.server.server_address[1]}", "model": "g26",
                            "node": "n", "slot": 0})
            elif self.path.startswith("/v1/models"):
                self._json({"models": [{"name": "g26", "sampler_params": CARD}]})
            else:
                self._json({}, 404)

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
            if self.path == "/v1/chat/completions":
                sent.append(body)
                self._json({"choices": [{"message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
                            "usage": {"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}})
            else:
                self._json({})
    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_address[1]}", sent


def test_a_hive_seat_sends_the_seated_models_card():
    srv, url, sent = _hive()
    try:
        r = HiveMind(url, "g26", wait_s=5).chat([{"role": "user", "content": "hi"}])
    finally:
        srv.shutdown()
    knobs = {k: v for k, v in CARD.items() if k != "source"}
    assert {k: sent[-1].get(k) for k in knobs} == knobs, f"RED: the seat sampled off the card: {sent[-1]}"
    assert r.served.get("sampled") == knobs, f"the reply does not say what it sampled: {r.served}"
