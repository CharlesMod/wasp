"""waspdoctor.looks — the doctor's eyes: what it may read, and nothing it may write.

The shape is held in code, not asked of the mind: a look is one command from a
read-only family, with at most three filters after it. No redirection, no
command substitution, no `;`/`&&`/`||`, no flag the family lists as a write.
Output is cut to its tail so a mind reads what matters and the record stays
small. Ported from the hive's apiary.looks, without the hive.
"""
from __future__ import annotations

import re
import shlex

OUT_LINES = 80
OUT_CHARS = 6000

# family -> flags it may not say (the write-shaped ones)
FAMILIES: dict[str, tuple[str, ...]] = {
    "ps": (), "ss": (), "df": (), "free": (), "du": (), "id": (), "uptime": (), "uname": (),
    "lsblk": (), "findmnt": (), "hostname": (), "date": (), "nproc": (), "env": (), "last": (),
    "cat": (), "grep": (), "ls": (), "stat": (), "head": (), "tail": (), "wc": (), "file": (),
    "readlink": (), "realpath": (), "getent": (), "which": (), "find": ("-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls"),
    "journalctl": ("--vacuum-size", "--vacuum-time", "--vacuum-files", "--rotate", "--flush", "--sync", "--relinquish-var"),
    "systemctl": ("start", "stop", "restart", "reload", "enable", "disable", "mask", "unmask", "kill",
                  "reset-failed", "set-property", "edit", "daemon-reload", "isolate", "poweroff", "reboot", "halt"),
    "ip": ("add", "del", "set", "flush", "change", "replace"), "lsof": (), "dmesg": ("-c", "-C", "--clear"),
    "ping": (), "curl": ("-X", "--request", "-d", "--data", "--data-binary", "-F", "--form", "-T", "--upload-file", "-o", "--output", "-O"),
    "loginctl": ("enable-linger", "disable-linger", "terminate-user", "kill-user", "lock-session", "terminate-session"),
    "pgrep": (), "fuser": ("-k", "-K", "--kill"), "lsusb": (), "lspci": (), "sysctl": ("-w", "--write"),
    "crontab": ("-e", "-r"), "mount": ("-o", "-a", "--bind", "--move"), "nginx": ("-s",), "sshd": (), "python3": ("-c", "-m"),
    "ulimit": (), "lscpu": (), "vmstat": (), "iostat": (), "top": ("-d",), "numfmt": (), "sort": (), "uniq": (), "cut": (), "awk": (), "sed": ("-i", "--in-place", "w", "e"),
    "test": (), "true": (), "echo": (), "printf": (), "stat": (), "md5sum": (), "sha256sum": (), "openssl": ("req", "genrsa", "genpkey", "rand", "-out", "-keyout", "ca", "enc", "dgst", "-sign"),
    "timedatectl": ("set-time", "set-timezone", "set-ntp", "set-local-rtc"), "hostnamectl": ("set-hostname", "set-icon-name", "set-chassis"),
    "hive": (), "nslookup": (), "dig": (), "host": (), "netstat": (), "cmp": (), "diff": ("-o", "--output"), "strings": (), "od": (), "xxd": ("-r",), "hexdump": (), "cksum": (), "readelf": (), "nm": (), "sha1sum": (), "base64": (), "[": (), "whoami": (), "groups": (), "w": (), "who": (), "getfacl": (), "lsattr": (), "namei": (), "dpkg-query": (), "apt-cache": (), "systemd-analyze": (), "less": (), "column": (), "tr": (), "jq": (), "resolvectl": ("flush-caches", "reset-statistics", "revert", "dns", "domain"),
}
# the Hive's own reads: `hive VERB [SUB]` where SUB (the first word that is not a
# flag) is one of these; "*" takes any, "" is the verb alone. Anything else (a
# write, a job launched, a stream that never ends) is not a look.
HIVE_READS: dict[str, set[str]] = {
    "": {""}, "journal": {"*"}, "fleet": {"*"}, "frames": {"*"}, "logs": {"*"}, "jobs": {""},
    "doctor": {"", "case", "pages"}, "needs": {"", "show", "plan"}, "roles": {""}, "scout": {""},
    "marks": {"*"}, "status": {""}, "succession": {""}, "bees": {""}, "brain": {""}, "core": {""},
    "queen": {"", "show"}, "deploy": {"status"}, "job": {"status", "logs"}, "todo": {"list", "arc"},
    "snapshot": {"list"}, "ray": {"status", "logs"}, "engines": {"list"}, "material": {"list"},
    "measured": {"list", "show"}, "trust": {"show", "check"}, "pin": {"status"}, "settings": {"", "path"},
}
HIVE_STREAMS = {"--follow", "-f"}


def hive_read(args: list[str]) -> str:
    """Why `hive ARGS` is not a look, or '' when it only reads."""
    words = [a for a in args if not a.startswith("-")]
    verb = words[0] if words else ""
    sub = words[1] if len(words) > 1 else ""
    allowed = HIVE_READS.get(verb)
    if allowed is None:
        return f"`hive {verb}` is not one of the Hive's reads; read it with hive journal, fleet, doctor case, needs show, job logs"
    if "*" not in allowed and sub not in allowed:
        return f"`hive {verb} {sub}` acts; a look only reads (" + ", ".join(f"hive {verb} {x}".rstrip() for x in sorted(allowed)) + ")"
    if any(a in HIVE_STREAMS for a in args):
        return "a look ends; `--follow` never does"
    return ""


FILTERS = {"grep", "tail", "head", "wc", "sort", "uniq", "cut", "awk", "sed", "tr", "jq", "numfmt", "column"}
SECRET = re.compile(r"(?i)(api[_-]?key|token|password|(?<![/\w])passwd(?!\b/)|secret|authkey|private[_-]?key|\.ssh/|id_(rsa|ed25519|ecdsa|dsa)\b|/etc/shadow|\.pem\b|\.key\b|tailscaled\.state|\.gnupg/|\.netrc|credentials)")


HARMLESS_REDIR = re.compile(r"(?<![\w/])(?:[12]?>&[12]|&?[12]?>\s*/dev/null|<\s*/dev/null)(?![\w/])")
OPS = {";", "&&", "||", "|", "&", ">", ">>", "<", "<<", "(", ")", ">&", "<&", "&>", "|&"}


def segments(cmd: str) -> list[tuple[str, list[str]]]:
    """Split a shell line into (operator-before, tokens) by the shell's own
    quoting: a `;` or `|` inside quotes is part of a word, not an operator.
    Raises ValueError on unbalanced quotes."""
    lx = shlex.shlex(cmd, posix=True, punctuation_chars=";&|<>()")
    lx.whitespace_split = True
    out, cur, op = [], [], ""
    for tok in lx:
        if tok in OPS or (tok and set(tok) <= set(";&|<>()")):
            out.append((op, cur))
            cur, op = [], tok
        else:
            cur.append(tok)
    out.append((op, cur))
    return [(o, t) for o, t in out if t or o]


REDIRS = {">", ">>", "<", "<<", ">&", "<&", "&>", "|&", "&", "(", ")"}
JOINS = {"", "|", "&&", "||", ";"}


def check(cmd: str, families: dict | None = None) -> str:
    """Why this look may not run, or '' when it may (on a POSIX shell; `families` is
    the platform's read-only verbs, Linux's by default). A look is judged by its effect:
    any chain of commands (`|`, `&&`, `||`, `;`) is allowed when every command in it
    only reads. Redirection and background are refused, except the harmless kinds
    (merging or discarding output, no input)."""
    fams = families if families is not None else FAMILIES
    if not cmd.strip():
        return "empty"
    if "$(" in cmd or "`" in cmd:
        return "no command substitution in a look"
    cmd = HARMLESS_REDIR.sub(" ", cmd)        # merging or discarding output writes nothing
    try:
        segs = segments(cmd)
    except ValueError as e:
        return f"unparseable: {e}"
    for op, _ in segs:
        if op in REDIRS:
            return f"redirection or background (`{op}`) writes or detaches; a look only reads"
        if op not in JOINS:
            return f"`{op}` is not a way to join commands in a look"
    if len(segs) > 8:
        return "a look is at most eight commands"
    for op, toks in segs:
        if not toks:
            return "an empty command in the chain"
        verb = toks[0].rsplit("/", 1)[-1]
        if verb == "sudo":
            return "the doctor already runs as the patient's root; no sudo"
        if op == "|" and verb in FILTERS:
            if verb == "sed" and any(x in toks[1:] for x in ("-i", "--in-place")):
                return "sed -i writes"
            if verb in ("awk", "sed") and any("system(" in t or ">" in t for t in toks[1:]):
                return "no shell-outs or writes inside awk/sed"
            continue
        if verb not in fams:
            return f"`{verb}` is not on the doctor's read-only list; read it another way"
        args = toks[1:]
        if verb == "hive" and (why := hive_read(args)):
            return why
        for bad in fams[verb]:
            for i, t in enumerate(args):
                if t != bad:
                    continue
                if bad in ("-o", "--output") and i + 1 < len(args) and args[i + 1] == "/dev/null":
                    continue                    # output thrown away writes nothing
                return f"`{verb} {bad}` writes; a look only reads"
        if verb in ("awk", "sed") and any("system(" in t or ">" in t for t in args):
            return "no shell-outs or writes inside awk/sed"
        if verb in ("cat", "grep", "head", "tail", "find", "ls", "stat", "less", "strings", "od", "xxd", "hexdump", "base64"):
            if any(SECRET.search(t) for t in args):
                return "a look does not read a secret's file by name"
    return ""


def cut(out: str) -> str:
    lines = out.splitlines()
    if len(lines) > OUT_LINES:
        lines = [f"[{len(lines) - OUT_LINES} earlier lines cut]"] + lines[-OUT_LINES:]
    s = "\n".join(lines)
    if len(s) > OUT_CHARS:
        s = f"[{len(s) - OUT_CHARS} chars cut]\n" + s[-OUT_CHARS:]
    return SECRET_VALUE.sub(r"\1=<masked>", s)


SECRET_VALUE = re.compile(r"(?i)\b((?:api[_-]?key|token|password|passwd|secret|authkey)[a-z_]*)\s*[=:]\s*\S+")


def look(patient, cmd: str, *, timeout: float = 30, trusted: bool = False) -> tuple[int, str]:
    """Run one look on the patient, checked by the patient's platform. A refused
    look returns (126, why). `trusted` is the harness's own look (unchecked)."""
    why = "" if trusted else patient.platform.check_look(cmd)
    if why:
        return 126, f"refused: {why}"
    r = patient.run(cmd, timeout=timeout)
    return r.code, cut(r.out)
