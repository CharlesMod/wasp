"""The shapes the loop speaks in, so any product can bring its own patient,
mind and watchers: a ``Wake`` (what woke the doctor), a ``Result`` (a patient's
answer to a command), a ``Reply`` (a mind's answer to a chat) and ``NoSeat``
(a mind that is busy, not broken)."""
from __future__ import annotations

import time
from dataclasses import dataclass, field


@dataclass
class Wake:
    kind: str                     # unit_failed | oom | health_miss | line | a product's own
    what: str                     # the unit, the url, the line
    at: float = field(default_factory=time.time)
    evidence: str = ""

    @property
    def key(self) -> str:
        return f"{self.kind}:{self.what}"


@dataclass
class Result:
    code: int
    out: str

    @property
    def tail(self) -> str:
        return self.out[-6000:]


@dataclass
class Reply:
    text: str
    tool_calls: list = field(default_factory=list)   # [{"name", "arguments": dict, "id"}]
    tokens_in: int = 0
    tokens_out: int = 0
    seconds: float = 0.0
    mind: str = ""
    raw: dict | None = None
    reasoning: str = ""                              # a thinking model's reasoning, when the server returns it
    served: dict = field(default_factory=dict)       # where it ran: a seat's node, url and slot


class NoSeat(RuntimeError):
    """The mind's server answered, but no seat came within the call's wait: it is
    busy, not broken. A case keeps asking until its own clock runs out."""


class TooLong(RuntimeError):
    """No seat the mind has holds the call's context (the Hive's router answers 413,
    too_long): asking again will not change it; only a shorter call will."""
