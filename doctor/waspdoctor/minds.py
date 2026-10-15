"""Minds the loop can ask, in its one call shape (`chat(messages, tools=None) -> Reply`):

* `HiveMind` calls a Hive through its frame's door: the door says where a
  call would go and that model's card, and holds the call until an engine
  can start it; nothing is granted or said done.
* `openai_body` and `openai_reply` are the call and the answer any
  OpenAI-compatible server speaks, for a product's own minds beside it.

Every reply carries its usage, so a case counts tokens and seconds."""
from __future__ import annotations

import base64
import http.client
import json
import os
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


# The sampler knobs a model's card names (the registry's sampler_params), as
# the hive's ladder sends them (dance.rungs.CARD_KEYS; this package imports
# nothing of the hive's): a seat on the hive samples by its model's card.
CARD_KEYS = ("temperature", "top_p", "top_k", "min_p", "presence_penalty", "repeat_penalty")

# Silence, not length, ends a call: a slow machine reads a long prompt for
# minutes, and llama-server says how far it is (return_progress) while it does.
SILENCE_S = 120.0

# The frame's door when neither the caller nor HIVE_DOOR names one: the
# drone's loopback door on every frame of a Hive.
DOOR = "http://127.0.0.1:4409"


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


def openai_body(model: str, messages, tools, max_tokens: int, temperature: float | None, effort: str = "") -> dict:
    """A chat call as any OpenAI-compatible server takes it; `n_predict` is
    llama.cpp's own cap (a server's default can otherwise override max_tokens),
    `chat_template_kwargs` its per-request reasoning effort, and the top-level
    `reasoning_effort` is the same for vLLM, Ollama and OpenAI; each ignores the other's."""
    body = {"model": model, "messages": messages, "max_tokens": max_tokens,
            "n_predict": max_tokens}
    if temperature is not None:
        body["temperature"] = temperature
    if effort:
        body["chat_template_kwargs"] = {"reasoning_effort": effort}
        body["reasoning_effort"] = effort
    if tools:
        body["tools"] = tools
    return body


def openai_reply(out: dict, seconds: float, name: str) -> Reply:
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
    """A call on a Hive through its frame's door (the drone's loopback door:
    `door`, else HIVE_DOOR, else this frame's :4409), by model name or by the
    kind of work (`kind`, at the product's `urgency` and quality floor). The
    door says where a call would go and that model's card; the call goes
    under a path that asks for that model, and the door holds it until an
    engine can start it. Nothing is granted and nothing said done."""

    def __init__(self, door: str = "", model: str = "", *, cls: str = "background", wait_s: float = 120,
                 kind: str = "", urgency: float | None = None, quality: float | None = None):
        self.door = (door or os.environ.get("HIVE_DOOR") or DOOR).rstrip("/")
        self.model = model
        self.cls = cls
        self.wait_s = wait_s
        self.ask = {k: v for k, v in (("model", model), ("kind", kind), ("urgency", urgency),
                                      ("quality", quality)) if v not in ("", None)}
        self.name = f"hive:{model or kind}"
        self.say = print

    def _where(self, hive: dict, messages, tools) -> tuple[int, dict]:
        """Where the door would put the call now, granting nothing; its refusal said."""
        body = {"hive": dict(hive, read=True), "messages": messages, **({"tools": tools} if tools else {})}
        req = urllib.request.Request(f"{self.door}/v1/chat/completions", data=json.dumps(body).encode(),
                                     headers={"Content-Type": "application/json"}, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                return r.status, json.loads(r.read() or b"{}")
        except urllib.error.HTTPError as e:
            try:
                return e.code, json.loads(e.read() or b"{}")
            except ValueError:
                return e.code, {}

    def chat(self, messages: list[dict], tools: list[dict] | None = None,
             *, max_tokens: int = 1024, effort: str = "") -> Reply:
        """One call; an engine that drops mid-call (a restart, a reload) is
        backed off from while it is down and asked for again, until wait_s."""
        deadline = time.time() + self.wait_s
        delay = 2.0
        while True:
            try:
                return self._chat_once(messages, tools, max_tokens=max_tokens, effort=effort)
            except (urllib.error.URLError, ConnectionError, TimeoutError, http.client.HTTPException) as e:
                if isinstance(e, urllib.error.HTTPError) and e.code < 500:
                    raise
                if time.time() + delay > deadline:
                    raise
                time.sleep(delay)          # backoff while the other side is down
                delay = min(delay * 2, 30.0)

    def _chat_once(self, messages, tools, *, max_tokens, effort="") -> Reply:
        hive = {"class": self.cls, **self.ask,
                "tokens": sum(len(json.dumps(m)) for m in messages) // 3 + max_tokens}
        code, where = self._where(hive, messages, tools)
        if code == 413 or where.get("error") == "too_long":
            # no slot holds a call this long: waiting will not change it (the loop trims it)
            raise TooLong(f"{self.name}: {where.get('why') or where}")
        model = str(where.get("model") or self.model) if code == 200 else self.model
        if not model:
            raise NoSeat(f"no seat for {self.name}: {where.get('why') or where}")
        sampled = {k: v for k, v in (where.get("sampler_params") or {}).items()
                   if k in CARD_KEYS and isinstance(v, (int, float))}
        body = dict(openai_body(model, messages, tools, max_tokens, None, effort), **sampled)
        raw = json.dumps(dict(hive, model=model), separators=(",", ":")).encode()
        path = base64.urlsafe_b64encode(raw).rstrip(b"=").decode()
        t0 = time.time()
        out = _post_stream(f"{self.door}/as/{path}/v1/chat/completions", body)
        r = openai_reply(out, time.time() - t0, self.name)
        r.served = {"node": str(where.get("node") or ""), "url": self.door, "sampled": sampled}
        return r

