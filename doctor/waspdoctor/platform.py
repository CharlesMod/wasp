"""waspdoctor.platform — what differs between Linux, macOS and Windows, in one place.

A patient's platform is detected once (`detect`) and kept on it as a
capability; nothing else in DBee asks "which OS" again. A Platform says:

* how the mind's looks are checked (`check_look`) and its cures (`check_cure`),
* what the harness reads first when a service wakes it (`first_looks`),
* whether a service is healthy again (`service_state`): the re-read that decides a close,
* how a service is found and watched (`resolve`, `watch_cmd`, `parse_event`),
* what a critical line looks like there by default (`critical`).

Linux and macOS share the POSIX shell checker (`looks.check`) with their own
read-only families; Windows has a PowerShell checker here. The harness's own
first looks are trusted commands and run unchecked; only the mind's are held
to the shape.
"""
from __future__ import annotations

import json
import re
import shlex
from dataclasses import dataclass, field

from . import looks

# ---------------------------------------------------------------- detection


def detect(patient) -> "Platform":
    """Ask the machine once. A POSIX shell answers `uname -s`; a Windows
    patient (PowerShell) answers its own way."""
    if getattr(patient, "shell", "") == "powershell":
        return WINDOWS
    r = patient.run("uname -s 2>/dev/null || echo unknown", timeout=15)
    out = r.out.strip().lower()
    if "darwin" in out:
        return MACOS
    if "linux" in out:
        return LINUX
    r = patient.run("$PSVersionTable.PSVersion.Major", timeout=15)
    if r.code == 0 and r.out.strip().isdigit():
        return WINDOWS
    return LINUX


# ---------------------------------------------------------------- the shape


@dataclass
class Platform:
    name: str
    shell: str                                   # "sh" | "powershell"
    families: dict | None = None  # read-only verbs → their write-shaped arguments; None is looks.FAMILIES
    never: list = field(default_factory=list)    # (regex, why): never a cure, however worded
    cure_verbs: set = field(default_factory=set)
    critical: str = ""                           # a critical line, by default

    # -- checks -------------------------------------------------------------
    def check_look(self, cmd: str) -> str:
        return looks.check(cmd, families=self.families)

    def check_cure(self, cmd: str) -> str:
        from .cures import check_cure
        return check_cure(cmd, never=self.never, verbs=self.cure_verbs, families=self.families)

    # -- a service ----------------------------------------------------------
    def resolve(self, patient, service: str) -> dict:
        return {"service": service}

    def first_looks(self, service: str) -> list[str]:
        return []

    def service_state(self, patient, service: str) -> tuple[bool, str]:
        return False, "unknown"

    def watch_cmd(self, service: str = "") -> str:
        return ""

    # whether watch_cmd prints {"ready":true} once it is subscribed; a stream that
    # does not is taken as ready when it starts (journalctl -f and tail -F read from now)
    says_ready = False

    def down_at_start(self, patient, service: str) -> str:
        """The service is already down, badly, as DBee starts to watch it: no event
        will come for a fault that happened before the subscription. A clean stop
        (result success, exit code 0, a Windows service never started) is not."""
        ok, said = self.service_state(patient, service)
        if ok or re.search(r"result success|exit code (0|1077)\b", said):
            return ""
        return said

    def parse_event(self, line: str, service: str = "") -> dict | None:
        """One line of the watcher's stream as {kind, what, evidence}, or None."""
        return None

    def what_changed(self) -> str:
        return ""


# ---------------------------------------------------------------- Linux

LINUX_CRITICAL = (r"(?i)\b(emerg|alert|crit(ical)?|fatal|panic|segfault|core dumped|Traceback|"
                  r"Out of memory|oom-kill|No space left|Permission denied|Input/output error|"
                  r"Address already in use|certificate has expired)\b")
_FAILED = re.compile(r"(?:Failed to start|entered failed state|Failed with result|Main process exited, code=(?:exited|killed), status=[1-9])")
_UNIT = re.compile(r"\b([\w@.-]+\.service)\b")
_OOM = re.compile(r"(?:Out of memory: Killed process|oom-kill:|OOM killed)")


class _Linux(Platform):
    def resolve(self, patient, service):
        unit = service if "." in service else service + ".service"
        return {"service": unit, "manager": "systemd", "log": f"journalctl -u {unit}"}

    def first_looks(self, unit):
        return [f"systemctl status {unit} --no-pager -l -n 0",
                f"journalctl -u {unit} --no-pager -n 25 -o short-iso",
                f"systemctl cat {unit} --no-pager"]

    def service_state(self, patient, unit):
        r = patient.run(f"systemctl show -p ActiveState,Result,Type {unit}", timeout=20)
        st = dict(l.split("=", 1) for l in r.out.splitlines() if "=" in l)
        state, result, typ = st.get("ActiveState", ""), st.get("Result", ""), st.get("Type", "")
        ok = state == "active" or (typ == "oneshot" and state == "inactive" and result == "success")
        return ok, f"{state} (result {result}{', oneshot' if typ == 'oneshot' else ''})"

    def watch_cmd(self, service=""):
        return "journalctl -f -o json --no-pager -n 0 2>/dev/null"

    def parse_event(self, line, service=""):
        try:
            e = json.loads(line)
        except ValueError:
            return None
        msg = e.get("MESSAGE") or ""
        if isinstance(msg, list):
            msg = bytes(msg).decode(errors="replace")
        unit = e.get("UNIT") or e.get("_SYSTEMD_UNIT") or ""
        if _OOM.search(msg):
            m = _UNIT.search(msg)
            return {"kind": "oom", "what": m.group(1) if m else unit or "kernel", "evidence": msg}
        if _FAILED.search(msg):
            m = _UNIT.search(msg) or _UNIT.search(unit)
            return {"kind": "unit_failed", "what": m.group(1) if m else unit or "?", "evidence": msg}
        if service and unit == service and re.search(self.critical, msg):
            return {"kind": "line", "what": service, "evidence": msg}
        return None

    def what_changed(self):
        return ("find /etc /opt /usr/local /srv /run/systemd/transient -xdev -type f -mmin -60 "
                "-printf '%TY-%Tm-%Td %TH:%TM  %u:%g %m  %p\\n' 2>/dev/null | sort -r | head -25")


# ---------------------------------------------------------------- macOS

MACOS_FAMILIES = {
    "log": ("erase", "config", "collect"), "launchctl": ("bootout", "bootstrap", "unload", "load", "kickstart", "kill",
            "enable", "disable", "remove", "stop", "start", "submit", "setenv", "unsetenv", "reboot", "config"),
    "vm_stat": (), "diskutil": ("erase", "eraseDisk", "eraseVolume", "zeroDisk", "secureErase", "partitionDisk", "unmount",
            "unmountDisk", "mount", "repairVolume", "resizeVolume", "apfs", "rename", "enableJournal", "disableJournal"),
    "scutil": ("--set",), "security": ("delete-certificate", "delete-identity", "delete-keychain", "add-certificates",
            "add-trusted-cert", "remove-trusted-cert", "import", "export", "unlock-keychain", "set-key-partition-list",
            "find-generic-password", "find-internet-password", "dump-keychain"),
    "sw_vers": (), "pmset": ("-a", "-b", "-c", "-u", "sleepnow", "restoredefaults", "schedule", "repeat", "force"),
    "mdls": (), "plutil": ("-convert", "-replace", "-insert", "-remove", "-o"), "defaults": ("write", "delete", "import", "rename"),
    "dscacheutil": ("-flushcache",), "networksetup": ("-set", "-setdnsservers", "-setmanual", "-setdhcp", "-setairportpower"),
    "system_profiler": (), "ioreg": (), "csrutil": ("enable", "disable", "clear"), "spctl": ("--master-disable", "--add", "--remove", "--enable", "--disable"),
    "brew": ("install", "uninstall", "upgrade", "update", "remove", "rm", "cleanup", "link", "unlink", "services"),
    "codesign": ("-s", "--sign", "--remove-signature"), "stat": (), "fs_usage": (), "lsof": (), "netstat": (), "route": ("add", "delete", "change", "flush"),
}
MACOS_NEVER = [
    (re.compile(r"\bdiskutil\s+(erase\w*|zeroDisk|secureErase|partitionDisk|apfs\s+delete\w*)"), "a disk or volume erase"),
    (re.compile(r"\b(csrutil|nvram)\b"), "the machine's protection or firmware settings"),
    (re.compile(r"\bspctl\s+--master-disable\b"), "turning off Gatekeeper"),
    (re.compile(r"\brm\s+-[a-zA-Z]*r[a-zA-Z]*\s+(/System|/Library|/Users|/Applications|/private|/usr|/bin|/sbin)(\s|/|$)"), "a recursive delete of a system tree"),
    (re.compile(r"\btmutil\s+(delete|disable)"), "removing backups"),
    (re.compile(r"\bdscl\s+\S+\s+-(delete|passwd|change)"), "a user or password change"),
    (re.compile(r"\bsecurity\s+(delete-|add-trusted|remove-trusted|set-key)"), "the keychain's trust"),
    (re.compile(r"\b(shutdown|halt|reboot)\b"), "a reboot or power change"),
    (re.compile(r"\bfdesetup\b"), "disk encryption"),
]


class _MacOS(Platform):
    def resolve(self, patient, service):
        """A launchd label (com.example.thing) or a process name; the system domain first."""
        label = service
        r = patient.run(f"launchctl print system/{shlex.quote(label)} 2>/dev/null | head -3", timeout=15)
        domain = "system"
        if r.code != 0 or not r.out.strip():
            r2 = patient.run("id -u", timeout=10)
            domain = f"gui/{r2.out.strip() or '501'}"
        return {"service": label, "manager": "launchd", "domain": domain,
                "log": f"log show --last 10m --predicate 'process == \"{label.rsplit('.', 1)[-1]}\"'"}

    def first_looks(self, label):
        proc = label.rsplit(".", 1)[-1]
        return [f"{{ launchctl print system/{label} 2>/dev/null || launchctl print gui/$(id -u)/{label}; }} | head -60",
                f"log show --last 15m --style compact --predicate 'process == \"{proc}\" OR eventMessage CONTAINS \"{label}\"' | tail -40",
                "df -h", "vm_stat | head -12", "uptime"]

    def service_state(self, patient, label):
        r = patient.run(f"launchctl print system/{label} 2>/dev/null || launchctl print gui/$(id -u)/{label}", timeout=15)
        return mac_state(r.out)

    def watch_cmd(self, service=""):
        """launchd's own log for exits (the unified log does not carry them on
        macOS 26: `log stream --process launchd` is silent while an agent
        crash-loops), followed by kqueue through tail -F; the unified log for the
        service's errors and faults. Both end with this shell."""
        proc = service.rsplit(".", 1)[-1] if service else ""
        pred = ('(messageType == error OR messageType == fault)'
                + (f' AND (process == "{proc}" OR eventMessage CONTAINS "{service}")' if service else ""))
        stream = f"log stream --style ndjson --level info --predicate {shlex.quote(pred)}"
        return (f"trap 'kill $a $b 2>/dev/null' EXIT INT TERM; a=; "
                f"if [ -r {LAUNCHD_LOG} ]; then tail -F -n 0 {LAUNCHD_LOG} & a=$!; fi; "
                f"{stream} & b=$!; wait")

    def parse_event(self, line, service=""):
        if line.lstrip().startswith("{"):
            return mac_event(line, service, self.critical)
        return launchd_log_event(line, service)

    def what_changed(self):
        return ("find /etc /Library/LaunchDaemons /Library/LaunchAgents /usr/local/etc /opt/homebrew/etc "
                "-type f -mmin -60 2>/dev/null | head -25")


def mac_state(out: str) -> tuple[bool, str]:
    """`launchctl print` → healthy?: running, or a job that ran and exited 0 (a oneshot)."""
    state = re.search(r"^\s*state = (\S+)", out, re.M)
    last = re.search(r"last exit code = ([^\n]+)", out)
    pid = re.search(r"^\s*pid = (\d+)", out, re.M)
    s = state.group(1) if state else "unknown"
    code = last.group(1).strip() if last else "?"
    ok = s == "running" or (not pid and code == "0")
    return ok, f"{s} (last exit code {code})"


LAUNCHD_LOG = "/var/log/com.apple.xpc.launchd/launchd.log"
# 2026-10-09 16:23:20.062499 (gui/501/com.example.web [62472]) <Notice>: exited due to exit(2), ran for 45ms
_LAUNCHD_LINE = re.compile(r"\((?:[\w.-]+/)*([\w.-]+) \[\d+\]\) <\w+>: (.*)$")


def launchd_log_event(line: str, service: str) -> dict | None:
    """A line of launchd's own log: a job that exited badly wakes; a clean exit, or
    a stop launchd itself sent (bootout, kickstart -k), does not."""
    m = _LAUNCHD_LINE.search(line.strip())
    if not m:
        return None
    label, msg = m.groups()
    if not _LAUNCHD_EXIT.search(msg) or re.search(r"due to exit\(0\)|sent by launchd", msg):
        return None
    if service and service not in (label, label.rsplit(".", 1)[-1]):
        return None
    return {"kind": "unit_failed", "what": label, "evidence": line.strip()}


_LAUNCHD_EXIT = re.compile(r"(?:exited due to |exited with exit code[: ]|exited abnormally|Service exited)", re.I)


def mac_event(line: str, service: str, critical: str) -> dict | None:
    try:
        e = json.loads(line)
    except ValueError:
        return None
    msg = e.get("eventMessage") or ""
    proc = (e.get("processImagePath") or "").rsplit("/", 1)[-1]
    mtype = (e.get("messageType") or "").lower()
    if proc == "launchd" and _LAUNCHD_EXIT.search(msg) and (not service or service in msg):
        if re.search(r"exit(ed)? (code[: ]*|due to exit\()0\)?(\s|$)", msg):
            return None                                   # a clean exit is not a fault
        m = re.search(r"\[(?:\d+:)?([\w.-]+)\]|service: ([\w.-]+)|\b(\w+(?:\.\w+)+)\b", msg)
        what = service or next((g for g in (m.groups() if m else ()) if g), "launchd")
        return {"kind": "unit_failed", "what": what, "evidence": msg}
    if mtype in ("fault", "error") and (not service or proc == service.rsplit(".", 1)[-1] or service in msg):
        if mtype == "fault" or re.search(critical, msg):
            return {"kind": "line", "what": service or proc, "evidence": f"{proc}: {msg}"}
    return None


# ---------------------------------------------------------------- Windows

# PowerShell verbs and commands that only read
PS_READ_VERBS = {"get", "test", "select", "where", "sort", "measure", "format", "group", "compare",
                 "resolve", "convertto", "convertfrom", "join", "split", "out-string", "write-output", "write-host",
                 "out-host", "find"}
PS_ALIASES = {"gci", "ls", "dir", "gc", "cat", "type", "sls", "select", "where", "?", "sort", "fl", "ft", "fw",
              "gsv", "gps", "ps", "gcim", "gwmi", "measure", "group", "gi", "gp", "gl", "pwd", "echo", "%"}
# native executables, with the arguments that make them write
WIN_NATIVE = {
    "ipconfig": ("/release", "/renew", "/flushdns", "/registerdns"), "netstat": (), "whoami": (), "hostname": (),
    "systeminfo": (), "tasklist": (), "where.exe": (), "findstr": (), "more": (), "nslookup": (), "ping": (),
    "sc": ("start", "stop", "config", "delete", "create", "pause", "continue", "failure", "sdset", "privs", "control"),
    "sc.exe": ("start", "stop", "config", "delete", "create", "pause", "continue", "failure", "sdset", "privs", "control"),
    "wevtutil": ("cl", "clear-log", "sl", "set-log", "um", "uninstall-manifest", "im", "install-manifest", "epl", "export-log", "al", "archive-log"),
    "icacls": ("/grant", "/deny", "/remove", "/reset", "/setowner", "/restore", "/save", "/setintegritylevel", "/inheritance"),
    "certutil": ("-addstore", "-delstore", "-setreg", "-importpfx", "-repairstore", "-urlcache", "-f", "-decode", "-encode"),
    "netsh": ("set", "add", "delete", "reset", "dump", "exec", "install", "uninstall"),
    "reg": ("add", "delete", "import", "restore", "save", "load", "unload", "copy"),
    "fsutil": ("behavior", "dirty", "file", "hardlink", "objectid", "quota", "repair", "reparsepoint", "resource", "sparse", "usn", "volume"),
    "chkdsk": ("/f", "/r", "/x", "/b"), "schtasks": ("/create", "/delete", "/change", "/run", "/end"),
    "dism": ("/online", "/image", "/cleanup-image"), "w32tm": ("/resync", "/config", "/register", "/unregister"),
}
PS_WRITE_METHODS = re.compile(r"\.(Kill|Delete|Remove|Stop|Start|Set\w*|Write\w*|Create\w*|Move\w*|Copy\w*|Clear|Invoke\w*|Close|Dispose|Add\w*|Put|Change\w*)\s*\(", re.I)
PS_DANGER = re.compile(r"(?i)(\bInvoke-Expression\b|\biex\b|\bInvoke-Command\b|\bicm\b|\bStart-Process\b|\bsaps\b|\bAdd-Type\b|"
                       r"\bNew-Object\b|\[\s*(System\.)?(IO\.File|Diagnostics\.Process|Reflection)|\bInvoke-WebRequest\b|\biwr\b|"
                       r"\bInvoke-RestMethod\b|\birm\b|\bOut-File\b|\bTee-Object\b|\bSet-Content\b|\bAdd-Content\b|-EncodedCommand|"
                       r"\bStart-Job\b|\bRegister-\w+|\bUnregister-\w+|\bEnter-PSSession\b)")
PS_REDIRECT = re.compile(r"(?<![\w$])(?:\*|[1-6])?>>?(?!\s*\$null)|(?<![\w$-])&\s*(?=[\"'$({\w.\\])")
PS_CMD = re.compile(r"(?<![\w$.\\/:-])([A-Za-z]+-[A-Za-z][A-Za-z0-9]*|[A-Za-z][\w.]*\.exe)(?![\w-])")   # a name inside a path is not a command
WIN_SECRET = re.compile(r"(?i)(\\config\\SAM|\\config\\SECURITY|unattend\.xml|\.pfx\b|\.pem\b|\.key\b|ConsoleHost_history|"
                        r"\bGet-Credential\b|ConvertFrom-SecureString|\bdpapi|\\Microsoft\\Credentials|\\Microsoft\\Protect)")


def check_ps(cmd: str) -> str:
    """Why a PowerShell look may not run, or ''. Read by effect: every command in
    the line (in pipes, parentheses or a Where-Object filter) only reads."""
    if not cmd.strip():
        return "empty"
    if PS_DANGER.search(cmd):
        return f"`{PS_DANGER.search(cmd).group(0).strip()}` runs or writes; a look only reads"
    if PS_REDIRECT.search(cmd):
        return "redirection or the call operator writes or runs; a look only reads (2>$null is fine)"
    if PS_WRITE_METHODS.search(cmd):
        return f"`{PS_WRITE_METHODS.search(cmd).group(0)}` changes something; a look only reads"
    if WIN_SECRET.search(cmd):
        return "a look does not read a secret's store by name"
    if re.search(r"(?i)\bForEach-Object\b|(?<![\w$])%\s*\{", cmd):
        # a ForEach block may only format what it was handed
        for body in re.findall(r"\{([^{}]*)\}", cmd):
            if PS_CMD.search(body) and not all(_ps_reads(c) for c in PS_CMD.findall(body)):
                return "a ForEach block in a look may only read"
    for c in PS_CMD.findall(cmd):
        if c.lower() in WIN_NATIVE:
            continue                                       # a native tool: its arguments are checked below
        if not _ps_reads(c):
            return f"`{c}` is not a read-only command; a look only reads"
    # bare words leading a pipeline segment: aliases and native tools
    for seg in re.split(r"[|;]|&&|\|\|", re.sub(r"\{[^{}]*\}|\([^()]*\)|'[^']*'|\"[^\"]*\"", " ", cmd)):
        words = seg.strip().split()
        if not words:
            continue
        w0 = words[0].lower()
        if "-" in w0 and _ps_reads(words[0]):
            continue
        if w0 in PS_ALIASES:
            continue
        native = WIN_NATIVE.get(w0) if w0 in WIN_NATIVE else WIN_NATIVE.get(w0 + ".exe") if not w0.endswith(".exe") else None
        if w0 in WIN_NATIVE or native is not None:
            bad = WIN_NATIVE.get(w0, native) or ()
            hit = next((b for b in bad if b.lower() in (x.lower() for x in words[1:])), None)
            if hit:
                return f"`{w0} {hit}` writes; a look only reads"
            continue
        if w0.startswith("$") or w0[0] in "@[(.'\"0123456789-":
            continue                                       # an expression, a value, a member
        return f"`{words[0]}` is not on the doctor's read-only list; read it another way"
    return ""


def _ps_reads(cmd: str) -> bool:
    c = cmd.lower()
    if c.endswith(".exe"):
        return c in WIN_NATIVE and not WIN_NATIVE[c]  # an exe with write-shaped args is checked in segments
    verb = c.split("-", 1)[0]
    return verb in PS_READ_VERBS or c in PS_READ_VERBS


WIN_NEVER = [
    (re.compile(r"(?i)\b(Format-Volume|Clear-Disk|Initialize-Disk|Remove-Partition|Set-Partition|Remove-PhysicalDisk|diskpart|format\s+[a-z]:)"), "a disk or volume erase"),
    (re.compile(r"(?i)\b(bcdedit|bcdboot|bootrec|reagentc)\b"), "the boot configuration"),
    (re.compile(r"(?i)\b(Restart-Computer|Stop-Computer|shutdown(\.exe)?)\b"), "a reboot or power change"),
    (re.compile(r"(?i)\bvssadmin\b.*\bdelete|\bwbadmin\b.*\bdelete|\bRemove-WBBackupSet"), "removing backups or shadow copies"),
    (re.compile(r"(?i)\bwevtutil\s+(cl|clear-log)\b|\bClear-EventLog\b|\bRemove-EventLog\b"), "clearing the event logs"),
    (re.compile(r"(?i)\bcipher\s+/w"), "wiping free space"),
    (re.compile(r"(?i)\b(Remove-Item|rm|rmdir|rd|del)\b.*-Recurse.*C:\\(Windows|Program Files( \(x86\))?)(\\|\s|$|\*|\"|'|[})\]])"), "a recursive delete inside a system tree"),
    (re.compile(r"(?i)\b(Remove-Item|rm|rmdir|rd|del)\b.*-Recurse.*C:\\(Users|ProgramData)\\?(\s|$|\*|\"|'|[})\]])"), "a recursive delete of a system tree"),
    (re.compile(r"(?i)\b(reg(\.exe)?\s+delete|Remove-Item\b.*HKLM:\\(SYSTEM|SOFTWARE|SAM|SECURITY))"), "deleting the registry"),
    (re.compile(r"(?i)\b(net\s+user|Remove-LocalUser|Set-LocalUser|Disable-LocalUser|net\s+localgroup)\b"), "a user or group change"),
    (re.compile(r"(?i)\b(Set-MpPreference|Add-MpPreference)\b.*-Disable|\bDisable-WindowsOptionalFeature\b|\bSet-ExecutionPolicy\s+(Unrestricted|Bypass)"), "turning off the machine's protection"),
    (re.compile(r"(?i)\b(Disable-NetAdapter|Remove-NetIPAddress|netsh\s+(interface|int)\s+.*\bdisable|tailscale)"), "cutting the network (a doctor never cuts its own way in)"),
    (re.compile(r"(?i)\btakeown\b.*C:\\Windows|\bicacls\b.*C:\\Windows.*/(grant|reset|setowner)"), "the system's own permissions"),
    (re.compile(r"(?i)\bInvoke-Expression\b|\biex\b|-EncodedCommand|\bInvoke-WebRequest\b.*\|\s*iex"), "running text as code"),
]
WIN_CURE_VERBS = {"restart-service", "start-service", "stop-service", "set-service", "move-item", "copy-item", "rename-item",
                  "remove-item", "set-content", "add-content", "clear-content", "new-item", "set-itemproperty",
                  "new-itemproperty", "stop-process", "set-acl", "icacls", "sc", "sc.exe", "netsh", "certutil",
                  "ipconfig", "w32tm", "wevtutil", "schtasks", "import-certificate", "remove-netfirewallrule",
                  "set-netfirewallrule", "enable-scheduledtask", "start-scheduledtask", "set-dnsclientserveraddress",
                  "out-file", "compress-archive", "start-sleep", "write-output", "out-null"}


def check_ps_cure(cmd: str) -> str:
    if not cmd.strip():
        return "empty"
    if "\n" in cmd.strip():
        return "a cure is one line"
    for pat, why in WIN_NEVER:
        if pat.search(cmd):
            return f"never: {why}"
    if re.search(r"(?:^|[\s;|(=])[.&]\s*[{'\"$(]", cmd):
        return "running a scriptblock or a path (`.` or `&`) hides the command; write the command itself"
    # every command, inside a scriptblock, parentheses or an assignment too, is a cure verb
    bare = re.sub(r"\$[\w:]+\s*=(?!=)", ";", re.sub(r"'[^']*'|\"[^\"]*\"", " ", cmd))   # `$x = cmd` runs cmd
    for seg in re.split(r"[|;{}()]|&&|\|\|", bare):
        words = seg.strip().split()
        if not words:
            continue
        w0 = words[0].lower()
        if w0.startswith("$") or w0[0] in "@[.'\"0123456789-":
            continue
        if w0 in WIN_CURE_VERBS or _ps_reads(words[0]) or w0 in PS_ALIASES or w0 in WIN_NATIVE:
            if (w := _ps_runs_anything(w0, cmd)):
                return w
            continue
        return f"`{words[0]}` is not a verb a cure may use"
    return ""


WIN_INTERPRETERS = r"(?i)\b(cmd|powershell|pwsh|wscript|cscript|mshta|rundll32|regsvr32|msiexec|bash|wsl)(\.exe)?\b|\s/c\s"


def _ps_runs_anything(verb: str, cmd: str) -> str:
    """Why this Windows verb would run a program the shape never saw, or ''."""
    low = cmd.lower()
    if verb in ("schtasks", "schtasks.exe") and re.search(r"(?i)/create\b|/tr\b|/xml\b", cmd):
        return "`schtasks /create` (or /tr) installs a program to run; it is not a cure"
    if verb in ("sc", "sc.exe") and (re.search(r"(?i)\bsc(\.exe)?\s+create\b", cmd) or
                                     re.search(r"(?i)\bcommand=", cmd) or
                                     (re.search(r"(?i)binpath=", cmd) and re.search(WIN_INTERPRETERS, cmd))):
        return "`sc` that creates a service or names a program to run (an interpreter in binPath, a failure command) is not a cure"
    if verb in ("set-service", "new-service") and (verb == "new-service" or (re.search(r"(?i)-BinaryPathName", cmd) and re.search(WIN_INTERPRETERS, cmd))):
        return "a service that runs an interpreter is not a cure"
    if verb == "certutil" and not re.search(r"(?i)-(addstore|delstore|repairstore|verifystore)\b", cmd):
        return "`certutil` only adds, removes or repairs a certificate in a store in a cure"
    if verb == "netsh" and re.search(r"(?i)\breset\b", low):
        return "`netsh … reset` resets the machine's network settings; it is not a cure"
    return ""


class _Windows(Platform):
    def check_look(self, cmd):
        return check_ps(cmd)

    def check_cure(self, cmd):
        return check_ps_cure(cmd)

    def resolve(self, patient, service):
        r = patient.run(f"Get-Service -Name '{service}' -ErrorAction SilentlyContinue | "
                        f"Select-Object Name,DisplayName,Status | ConvertTo-Json -Compress", timeout=20)
        try:
            j = json.loads(r.out.strip() or "{}")
        except ValueError:
            j = {}
        return {"service": j.get("Name") or service, "manager": "scm", "display": j.get("DisplayName", ""),
                "log": "Get-WinEvent System/Application"}

    def first_looks(self, service):
        s = service.replace("'", "''")
        return [f"Get-Service -Name '{s}' | Format-List Name,DisplayName,Status,StartType,DependentServices,ServicesDependedOn",
                f"Get-CimInstance Win32_Service -Filter \"Name='{s}'\" | Format-List Name,State,ExitCode,ProcessId,PathName,StartName",
                "$e = Get-WinEvent -FilterHashtable @{LogName='System'; ProviderName='Service Control Manager'; StartTime=(Get-Date).AddMinutes(-30)} -MaxEvents 15 -ErrorAction SilentlyContinue; if ($e) { $e | Format-Table TimeCreated,Id,Message -Wrap | Out-String -Width 220 } else { 'no Service Control Manager events in the last 30 minutes' }",
                "$e = Get-WinEvent -FilterHashtable @{LogName='Application'; Level=1,2,3; StartTime=(Get-Date).AddMinutes(-30)} -MaxEvents 25 -ErrorAction SilentlyContinue; if ($e) { $e | Format-Table TimeCreated,ProviderName,Id,Message -Wrap | Out-String -Width 220 } else { 'no warnings or errors in the Application log in the last 30 minutes' }",
                "Get-Volume | Where-Object DriveLetter | Format-Table DriveLetter,FileSystemLabel,SizeRemaining,Size -AutoSize",
                "Get-CimInstance Win32_OperatingSystem | Format-List LastBootUpTime,FreePhysicalMemory,TotalVisibleMemorySize"]

    def service_state(self, patient, service):
        r = patient.run(f"Get-CimInstance Win32_Service -Filter \"Name='{service}'\" | Select-Object State,ExitCode,StartMode | ConvertTo-Json -Compress", timeout=20)
        return win_state(r.out)

    says_ready = True

    def watch_cmd(self, service=""):
        return WIN_WATCH.replace("__SERVICE__", service.replace("'", "''"))

    def parse_event(self, line, service=""):
        return win_event(line, service, self.critical)

    def what_changed(self):
        return ("Get-ChildItem C:\\ProgramData,C:\\Windows\\System32\\drivers\\etc,C:\\inetpub -Recurse -File -ErrorAction SilentlyContinue | "
                "Where-Object LastWriteTime -gt (Get-Date).AddMinutes(-60) | Sort-Object LastWriteTime -Descending | "
                "Select-Object -First 25 LastWriteTime,FullName | Format-Table -AutoSize")


def win_state(out: str) -> tuple[bool, str]:
    try:
        j = json.loads(out.strip() or "{}")
    except ValueError:
        return False, out.strip()[:120] or "unknown"
    state, code, mode = j.get("State", "?"), j.get("ExitCode", "?"), j.get("StartMode", "?")
    return state == "Running", f"{state} (exit code {code}, start {mode})"


# An EventLogWatcher: the event log's own subscription, so nothing is polled. One
# JSON line per event: the Service Control Manager's crash events (7031, 7034,
# 7023, 7024), and critical/error events naming the service.
WIN_WATCH = r"""
$ErrorActionPreference = 'Stop'
$svc = '__SERVICE__'
# the Service Control Manager's messages name a service by its display name
$dn = if ($svc) { try { (Get-Service -Name $svc -ErrorAction Stop).DisplayName } catch { $svc } } else { '' }
$q = "*[System[(Level=1 or Level=2) or (Provider[@Name='Service Control Manager'] and (EventID=7031 or EventID=7034 or EventID=7023 or EventID=7024 or EventID=7000 or EventID=7009))]]"
$ws = @()
foreach ($log in 'System','Application') {
  $w = [System.Diagnostics.Eventing.Reader.EventLogWatcher]::new([System.Diagnostics.Eventing.Reader.EventLogQuery]::new($log, [System.Diagnostics.Eventing.Reader.PathType]::LogName, $q))
  Register-ObjectEvent -InputObject $w -EventName EventRecordWritten -SourceIdentifier "dbee-$log" | Out-Null
  $w.Enabled = $true; $ws += $w
}
'{"ready":true}'
while ($true) {
  $e = Wait-Event
  $r = $e.SourceEventArgs.EventRecord
  if ($r) {
    # a service installed after the watch began has a display name only now
    if ($svc -and $dn -eq $svc) { $dn = try { (Get-Service -Name $svc -ErrorAction Stop).DisplayName } catch { $svc } }
    $m = try { $r.FormatDescription() } catch { '' }
    [pscustomobject]@{ display = $dn; log = $r.LogName; provider = $r.ProviderName; id = $r.Id; level = $r.Level; msg = $m; props = @($r.Properties | ForEach-Object { "$($_.Value)" }) } | ConvertTo-Json -Compress
  }
  Remove-Event -EventIdentifier $e.EventIdentifier
}
"""


def win_event(line: str, service: str, critical: str) -> dict | None:
    try:
        e = json.loads(line)
    except ValueError:
        return None
    prov, eid, msg = e.get("provider", ""), int(e.get("id") or 0), e.get("msg") or ""
    props = [str(p) for p in e.get("props") or []]
    names = {n.lower() for n in (service, e.get("display") or "") if n}
    if prov == "Service Control Manager" and eid in (7031, 7034, 7023, 7024, 7000, 7009):
        named = props[0] if props else ""
        said = " ".join([msg] + props).lower()
        if names and not any(n in said for n in names):
            return None
        return {"kind": "unit_failed", "what": service or named or "service", "evidence": f"SCM {eid}: {msg}"}
    if int(e.get("level") or 4) <= 2 and (not names or any(n in (prov + " " + msg).lower() for n in names)):
        if int(e.get("level") or 4) == 1 or re.search(critical, msg):
            return {"kind": "line", "what": service or prov, "evidence": f"{prov} {eid}: {msg}"}
    return None


# ---------------------------------------------------------------- the three

LINUX = _Linux(name="linux", shell="sh", critical=LINUX_CRITICAL)
MACOS = _MacOS(name="macos", shell="sh",
               families={**looks.FAMILIES, **MACOS_FAMILIES},
               never=MACOS_NEVER,
               critical=r"(?i)\b(fault|fatal|panic|crash(ed)?|Abort trap|Segmentation fault|exited abnormally|"
                        r"Out of memory|No space left|Permission denied|Operation not permitted|certificate has expired|"
                        r"Address already in use)\b")
WINDOWS = _Windows(name="windows", shell="powershell",
                   critical=r"(?i)\b(fatal|critical|crash(ed)?|terminated unexpectedly|access is denied|"
                            r"not enough (disk )?space|out of memory|faulting application|unhandled exception|"
                            r"certificate .*expired|only one usage of each socket address|"
                            r"service cannot be started|access to the path .{1,260} is denied)\b")
ALL = {"linux": LINUX, "macos": MACOS, "windows": WINDOWS}
