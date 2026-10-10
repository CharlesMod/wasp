"""waspdoctor.casebook — the doctor's memory of what cured a fault before.

A fault's signature is the sorted set of its red probes as ``name=exit``: what
the machine read, not what anyone said. Every cure that ran is one row:

    {"ts", "case", "patient", "sig": [...], "cause", "cure": {"name", "command", "undo", "verify"},
     "won": bool, "finding"}

``won`` is the verify's word, never a mind's. Tallied on read: a cure that lost
since it last won loses its place; a cure that keeps winning on one patient
treats a symptom, and the doctor is told so.
"""
from __future__ import annotations

import json
import time
from collections import defaultdict
from pathlib import Path

RECURS = 2


class Casebook:
    def __init__(self, path: Path):
        self.path = path
        self.rows: list[dict] = []
        if path.exists():
            for line in path.read_text().splitlines():
                if line.strip():
                    try:
                        self.rows.append(json.loads(line))
                    except ValueError:
                        pass

    def record(self, *, case: str, patient: str, sig: list[str], cause: str, cure: dict, won: bool, finding: str = "") -> None:
        row = {"ts": time.time(), "case": case, "patient": patient, "sig": sorted(sig), "cause": cause,
               "cure": cure, "won": won, "finding": finding}
        self.rows.append(row)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        with self.path.open("a") as fh:
            fh.write(json.dumps(row) + "\n")

    def precedents(self, sig: list[str], limit: int = 3) -> list[dict]:
        """Cures seen on this signature (exact first, then partial), each with its tally."""
        want = set(sig)
        tally: dict[tuple, dict] = {}
        for r in self.rows:
            have = set(r.get("sig") or [])
            overlap = len(want & have)
            if not overlap:
                continue
            key = (r["cure"].get("name"), r["cure"].get("command"))
            t = tally.setdefault(key, {"cure": r["cure"], "won": 0, "lost": 0, "exact": 0, "last": 0, "last_won": True, "patients": defaultdict(int)})
            t["won" if r["won"] else "lost"] += 1
            t["exact"] += int(have == want)
            if r["ts"] >= t["last"]:
                t["last"], t["last_won"] = r["ts"], r["won"]
            if r["won"]:
                t["patients"][r.get("patient", "")] += 1
        out = []
        for t in tally.values():
            score = (t["exact"] * 2 + t["won"] - 2 * t["lost"]) * (1 if t["last_won"] else 0.25)
            symptom = any(n >= RECURS for n in t["patients"].values())
            out.append({"cure": t["cure"], "won": t["won"], "lost": t["lost"], "exact": t["exact"],
                        "score": score, "treats_a_symptom": symptom})
        out.sort(key=lambda x: -x["score"])
        return out[:limit]
