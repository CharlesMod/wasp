"""The machine the doctor works on, reached one way: a Patient runs a command and
returns a Result (exit code, output); looks, cures and watchers all go through
``run`` and ``stream``, so the loop never knows how the machine is reached.
``Hive`` is a frame of a Hive, each command a job (`hive run FRAME LINE`); a
product brings its own kinds beside it (DBee's local host, container, ssh)."""
from __future__ import annotations

import subprocess

from .protocol import Result


# PowerShell run without a console wraps its progress and error streams in CLIXML;
# a doctor reads text: progress off, errors as plain lines, wide output not cut
PS_PRELUDE = ("$ProgressPreference='SilentlyContinue'; $ErrorView='NormalView'; "
              "$PSDefaultParameterValues['Out-String:Width']=220; ")


class Patient:
    name = "patient"
    shell = "sh"
    _platform = None

    @property
    def platform(self):
        """What this machine is (Linux, macOS, Windows), asked once and kept."""
        if self._platform is None:
            from waspdoctor.platform import detect
            self._platform = detect(self)
        return self._platform

    def argv(self, cmd: str, user: str = "root", interactive: bool = False) -> list[str]:
        raise NotImplementedError

    def run(self, cmd: str, *, timeout: float = 60, user: str = "root", input: str | None = None) -> Result:
        try:
            p = subprocess.run(self.argv(cmd, user, interactive=input is not None), capture_output=True, text=True,
                               encoding="utf-8", errors="replace",
                               timeout=timeout, input=input,
                               stdin=None if input is not None else subprocess.DEVNULL)
        except subprocess.TimeoutExpired as e:
            out = (e.stdout or b"").decode(errors="replace") if isinstance(e.stdout, bytes) else (e.stdout or "")
            return Result(124, out + f"\n[timed out after {timeout:.0f}s]")
        out = p.stdout + (("\n" + p.stderr) if p.stderr else "")
        return Result(p.returncode, out)

    def stream(self, cmd: str) -> subprocess.Popen:
        """A long-running command whose stdout is read line by line (journalctl -f)."""
        return subprocess.Popen(self.argv(cmd), stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                text=True, encoding="utf-8", errors="replace", bufsize=1, stdin=subprocess.DEVNULL)




class Hive(Patient):
    """A frame of the Hive, reached the Hive's own way: each command is a job
    (`hive run FRAME LINE`), its exit the job's. `shell="powershell"` reaches the
    Windows side of a frame whose drone runs in WSL, through WSL's interop.
    Jobs end, so a Hive patient can be looked at and treated but not streamed:
    its watcher is the Hive's own spine."""

    PS = "/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"
    DONE = __import__("re").compile(r"^\S+: (succeeded|failed|cancelled|timed out)[^\n]*?exit (-?\d+)[^\n]*\(job \w+\)\s*$", __import__("re").M)

    runs_as_drone = True              # `hive run` runs as the frame's drone: its probes read true here

    def __init__(self, frame: str, shell: str = "sh", hive: str = ""):
        import shutil
        self.frame, self.name, self.shell = frame, frame, shell
        self.hive = hive or shutil.which("hive") or "hive"

    def argv(self, cmd, user="root", interactive=False):
        if self.shell == "powershell":
            import base64
            enc = base64.b64encode(PS_PRELUDE.__add__(cmd).encode("utf-16-le")).decode()
            cmd = f"{self.PS} -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand {enc}"
        return [self.hive, "run", self.frame, cmd]

    def run(self, cmd, *, timeout=60, user="root", input=None):
        r = super().run(cmd, timeout=timeout + 30, user=user)
        m = None
        for m in self.DONE.finditer(r.out):
            pass
        if not m:
            return r
        out = (r.out[:m.start()] + r.out[m.end():]).rstrip("\n")
        return Result(int(m.group(2)), out)

    def stream(self, cmd):
        raise RuntimeError("a Hive patient is not streamed: watch it through the Hive's spine")
