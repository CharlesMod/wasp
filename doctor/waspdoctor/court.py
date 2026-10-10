"""The Hive's court, for the doctor's triage of a Hive frame: the frame held while
it is mended (the fleet's work goes round it; work addressed to it by name, the
doctor's own looks and cures, still lands) and returned once it is, and an action
the court quarantined on the frame released once its cause is mended. A hold is
undone by its return, a release by the next crash; each lands on the court's spine
with the case and why."""
from __future__ import annotations

import json
import urllib.error
import urllib.request


class Court:
    def __init__(self, url: str, frame: str, *, timeout: float = 10):
        self.url, self.frame, self.timeout = url.rstrip("/"), frame, timeout

    def word(self, verb: str, case: str, why: str) -> tuple[bool, str]:
        """POST /v1/drones/{frame}/{hold|return}: (taken, what the court said)."""
        return self._post(f"/v1/drones/{self.frame}/{verb}", {"case": case, "why": why}, verb)

    def release(self, key: str, case: str, why: str) -> tuple[bool, str]:
        """POST /v1/quarantine/release: the action under key may run again."""
        return self._post("/v1/quarantine/release", {"key": key, "case": case, "why": why}, "release")

    def quarantined(self) -> list[dict]:
        """The actions the court quarantined whose last run was on this frame; none
        when the court does not answer (the case goes on without them)."""
        try:
            with urllib.request.urlopen(f"{self.url}/v1/quarantine", timeout=self.timeout) as r:
                rows = json.loads(r.read() or b"{}").get("quarantined") or []
        except (OSError, ValueError):
            return []
        return [q for q in rows if str(q.get("node", "")).lower() == self.frame.lower() and q.get("key")]

    def _post(self, path: str, body: dict, verb: str) -> tuple[bool, str]:
        req = urllib.request.Request(f"{self.url}{path}", method="POST", data=json.dumps(body).encode(),
                                     headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as r:
                return True, r.read().decode(errors="replace")[:300]
        except urllib.error.HTTPError as e:
            return False, f"the court refused the {verb} ({e.code}): {e.read().decode(errors='replace')[:300]}"
        except OSError as e:
            return False, f"the court did not take the {verb}: {e}"
