"""Minds the loop can ask, in its one call shape (`chat(messages, tools=None) -> Reply`):

* `HiveMind` asks a Hive's router for a seat (``GET /v1/route``, the court's own
  long poll), calls the engine's OpenAI-style endpoint on the seat granted, and
  gives the seat back (``POST /v1/route/done``).
* `openai_body` and `openai_reply` are the call and the answer any
  OpenAI-compatible server speaks, for a product's own minds beside it.

Every reply carries its usage, so a case counts tokens and seconds."""
from __future__ import annotations

import http.client
import json
import time
import urllib.error
import urllib.parse
import urllib.request

from .protocol import NoSeat, Reply, TooLong


def _post(url: str, body: dict, headers: dict | None = None, timeout: float = 300) -> dict:
    req = urllib.request.Request(url, data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json", **(headers or {})},
                                 method="POST")
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read())


# Silence, not length, ends a call: a slow machine reads a long prompt for
# minutes, and llama-server says how far it is (return_progress) while it does.
SILENCE_S = 120.0


def _post_stream(url: str, body: dict, headers: dict | None = None, silence_s: float | None = None) -> dict:
    """A chat call streamed, assembled into the reply a plain call returns. The
    socket timeout is a watchdog for silence: each progress or token chunk
    resets it. A server that ignores `stream` and answers JSON is read as is."""
    body = {**body, "stream": True, "stream_options": {"include_usage": True}, "return_progress": True}
    req = urllib.request.Request(url, data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json", **(headers or {})},
                                 method="POST")
    with urllib.request.urlopen(req, timeout=silence_s or SILENCE_S) as r:
        if "text/event-stream" not in (r.headers.get("Content-Type") or ""):
            return json.loads(r.read())
        return _assemble(r)


def _assemble(lines) -> dict:
    """OpenAI stream chunks (`data: {...}` lines) into one chat completion."""
    text, think, calls, usage, finish = [], [], {}, {}, None
    for raw in lines:
        line = raw.decode("utf-8", "replace").strip() if isinstance(raw, bytes) else raw.strip()
        if not line.startswith("data:"):
            continue
        data = line[5:].strip()
        if data == "[DONE]":
            break
        try:
            ch = json.loads(data)
        except ValueError:
            continue
        if ch.get("usage"):
            usage = ch["usage"]
        for c in ch.get("choices") or []:
            d = c.get("delta") or {}
            if d.get("content"):
                text.append(d["content"])
            if d.get("reasoning_content"):
                think.append(d["reasoning_content"])
            for tc in d.get("tool_calls") or []:
                slot = calls.setdefault(tc.get("index", len(calls)), {"id": "", "type": "function",
                                                                       "function": {"name": "", "arguments": ""}})
                if tc.get("id"):
                    slot["id"] = tc["id"]
                fn = tc.get("function") or {}
                slot["function"]["name"] += fn.get("name") or ""
                slot["function"]["arguments"] += fn.get("arguments") or ""
            finish = c.get("finish_reason") or finish
    msg = {"role": "assistant", "content": "".join(text)}
    if think:
        msg["reasoning_content"] = "".join(think)
    if calls:
        msg["tool_calls"] = [calls[k] for k in sorted(calls)]
    return {"choices": [{"message": msg, "finish_reason": finish}], "usage": usage}


def _get(url: str, timeout: float = 30) -> tuple[int, dict]:
    try:
        with urllib.request.urlopen(url, timeout=timeout) as r:
            return r.status, json.loads(r.read() or b"{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read() or b"{}")
        except ValueError:
            return e.code, {}


def openai_body(model: str, messages, tools, max_tokens: int, temperature: float, effort: str = "") -> dict:
    """A chat call as any OpenAI-compatible server takes it; `n_predict` is
    llama.cpp's own cap (a server's default can otherwise override max_tokens),
    `chat_template_kwargs` its per-request reasoning effort, and the top-level
    `reasoning_effort` is the same for vLLM, Ollama and OpenAI; each ignores the other's."""
    body = {"model": model, "messages": messages, "max_tokens": max_tokens,
            "n_predict": max_tokens, "temperature": temperature}
    if effort:
        body["chat_template_kwargs"] = {"reasoning_effort": effort}
        body["reasoning_effort"] = effort
    if tools:
        body["tools"] = tools
    return body


def openai_reply(out: dict, seconds: float, name: str) -> "Reply":
    msg = (out.get("choices") or [{}])[0].get("message") or {}
    usage = out.get("usage") or {}
    calls = []
    for tc in msg.get("tool_calls") or []:
        fn = tc.get("function") or {}
        try:
            args = json.loads(fn.get("arguments") or "{}")
        except ValueError:
            args = {"_raw": fn.get("arguments")}
        calls.append({"id": tc.get("id", ""), "name": fn.get("name", ""), "arguments": args})
    return Reply(text=msg.get("content") or "", tool_calls=calls, reasoning=msg.get("reasoning_content") or "",
                 tokens_in=int(usage.get("prompt_tokens") or 0),
                 tokens_out=int(usage.get("completion_tokens") or 0),
                 seconds=seconds, mind=name, raw=out)


class HiveMind:
    """A seat on the hive for one call at a time: by model name, or by the
    kind of work (`kind`, at the product's `urgency` and quality floor), the
    router choosing the mind and the call naming the one it seated."""

    def __init__(self, court: str, model: str = "", *, cls: str = "background", wait_s: float = 120,
                 kind: str = "", urgency: float | None = None, quality: float | None = None):
        self.court = court.rstrip("/")
        self.model = model
        self.cls = cls
        self.wait_s = wait_s
        self.ask = {k: v for k, v in (("model", model), ("kind", kind), ("urgency", urgency),
                                      ("quality", quality)) if v not in ("", None)}
        self.name = f"hive:{model or kind}"
        self.say = print

    def seats(self) -> int | None:
        """How many seats the Hive holds for this model now (the router's own count),
        or None when the court does not say."""
        try:
            code, body = _get(f"{self.court}/v1/route/demand", timeout=10)
        except OSError:
            return None
        n = (body.get("seats") or {}).get(self.model) if code == 200 else None
        return int(n) if isinstance(n, (int, float)) else None

    def _grant(self, tokens: int) -> dict:
        """Ask until a seat is granted or ``wait_s`` has passed. The court holds a
        refusal only ``hold`` seconds a call (its `wait`), and says when to ask
        again (`retry_after_s`); a seat another caller holds frees at its done."""
        hold = min(10.0, self.wait_s)
        q = urllib.parse.urlencode({"class": self.cls, **self.ask, "tokens": tokens, "wait": hold})
        deadline = time.time() + self.wait_s
        last = {}
        while True:
            code, body = _get(f"{self.court}/v1/route?{q}", timeout=hold + 20)
            if code == 200 and body.get("url"):
                return body
            if code == 413 or body.get("error") == "too_long":
                # no seat holds a call this long: waiting will not change it (the loop trims it)
                raise TooLong(f"{self.name}: {body.get('detail') or body.get('why') or body}")
            last = body
            if time.time() > deadline:
                break
            if self.say and time.time() - getattr(self, "_said", 0) > 30:
                self._said = time.time()
                self.say(f"   waiting for a seat on {self.name}: {(body.get('detail') or body.get('why') or '')[:120]}")
            time.sleep(min(5.0, float(body.get("retry_after_s") or body.get("retry_s") or 2)))
        raise NoSeat(f"no seat for {self.name} in {self.wait_s:.0f}s: {last.get('detail') or last.get('why') or last}")

    def chat(self, messages: list[dict], tools: list[dict] | None = None,
             *, max_tokens: int = 1024, temperature: float = 0.0, effort: str = "") -> Reply:
        """One call; an engine that drops mid-call (a restart, a reload) is
        backed off from and asked for again through the router, until wait_s."""
        deadline = time.time() + self.wait_s
        delay = 2.0
        while True:
            try:
                return self._chat_once(messages, tools, max_tokens=max_tokens, temperature=temperature, effort=effort)
            except (urllib.error.URLError, ConnectionError, TimeoutError, http.client.HTTPException) as e:
                if isinstance(e, urllib.error.HTTPError) and e.code < 500:
                    raise
                if time.time() + delay > deadline:
                    raise
                time.sleep(delay)
                delay = min(delay * 2, 30.0)

    def _chat_once(self, messages, tools, *, max_tokens, temperature, effort="") -> Reply:
        est = sum(len(json.dumps(m)) for m in messages) // 3 + max_tokens
        grant = self._grant(est)
        url = grant["url"].rstrip("/")
        # n_predict beside max_tokens: the engine's own cap, which a pin's default
        # otherwise overrides (seen: 12000 on a call that asked 4096)
        body = openai_body(grant.get("model") or self.model, messages, tools, max_tokens, temperature, effort)
        if grant.get("slot") is not None:
            # the seat the court granted, not one llama-server picks: an unpinned call
            # lands by LRU on another caller's slot and reads that conversation's state
            body["id_slot"] = int(grant["slot"])
        t0 = time.time()
        usage, out = {}, {}
        try:
            out = _post_stream(f"{url}/v1/chat/completions", body)
        finally:
            usage = out.get("usage") or {}
            try:
                _post(f"{self.court}/v1/route/done",
                      {"node": grant.get("node", ""), "url": grant.get("url", ""),
                       "slot": int(grant.get("slot", 0) or 0),
                       "tokens": int(usage.get("total_tokens") or 0),
                       **({"timings": out["timings"]} if out.get("timings") else {})},
                      timeout=5)
            except Exception:  # noqa: BLE001 — the engine's idle reading frees the seat then
                pass
        r = openai_reply(out, time.time() - t0, self.name)
        r.served = {"node": grant.get("node", ""), "url": grant.get("url", ""), "slot": int(grant.get("slot", 0) or 0)}
        return r
