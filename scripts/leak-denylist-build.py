#!/usr/bin/env python3
"""Build the private denylist used by scripts/leak-guard.py.

Collects literal values that must never appear in this public repo from the
live deployment's private files and writes them, one per line, to
~/.config/conduit/leak-denylist (mode 0600, outside the repo). Re-run after
rotating a credential or changing private config.

Sources (all optional; missing files are skipped):
  --env FILE    KEY=VALUE / export lines: every value >= 8 chars, plus the
                hostname of any *_URL value
  --json FILE   JSON config: string values under secret-like keys (literal,
                not ${ENV}), every e-mail address, chat_id/user_id numbers
  --file FILE   whole-file secret (e.g. a token file)
  --db FILE     gateway.db: Telegram pairing user/chat IDs (read-only)
  --extra FILE  hand-maintained values, one per line: plain lines are
                substrings; `word:VALUE` lines are whole-word, case-
                insensitive (use for names, pets, places, employer)

Defaults match the single-owner deployment under ~/ocgo; the script itself
contains no private values and is safe to publish.
"""

import argparse
import glob
import json
import os
import re
import sqlite3
import sys
from urllib.parse import urlparse

HOME = os.path.expanduser("~")
OUT = os.environ.get("CONDUIT_LEAK_DENYLIST") or os.path.join(HOME, ".config/conduit/leak-denylist")
SECRET_KEY = re.compile(r"secret|token|key|pass|cred|auth|client_id|client_secret|identity", re.I)
ID_KEY = re.compile(r"(^|_)(chat|user|owner|admin)_?ids?$", re.I)
EMAIL = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}")
PUBLIC_EMAIL_DOMAINS = re.compile(r"@(example\.(com|org|net)|users\.noreply\.github\.com)$", re.I)


def keep(v):
    v = v.strip().strip("'\"")
    if len(v) < 6 or v.startswith("${") or v.lower() in ("true", "false", "null", "none"):
        return None
    if v.startswith(("/", "~")) and " " not in v:  # file paths are not secrets
        return None
    return v


def from_env(path, out):
    for line in open(path, encoding="utf-8", errors="replace"):
        m = re.match(r"\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)", line)
        if not m:
            continue
        key, val = m.group(1), m.group(2).strip().strip("'\"")
        if key.endswith("URL") and "://" in val:
            host = urlparse(val).hostname
            if host:
                out.add(host)
        v = keep(val)
        if v and len(v) >= 8:
            out.add(v)


def from_json(path, out):
    def walk(o, k=""):
        if isinstance(o, dict):
            for kk, vv in o.items():
                walk(vv, kk)
        elif isinstance(o, list):
            for vv in o:
                walk(vv, k)
        elif isinstance(o, (int, str)):
            s = str(o)
            if ID_KEY.search(k) and re.fullmatch(r"-?\d{6,}", s):
                out.add(s)
            elif isinstance(o, str):
                for e in EMAIL.findall(s):
                    if not PUBLIC_EMAIL_DOMAINS.search(e):
                        out.add(e)
                if SECRET_KEY.search(k):
                    v = keep(s)
                    if v and len(v) >= 16 and " " not in v:
                        out.add(v)
    walk(json.load(open(path, encoding="utf-8")))


def from_db(path, out):
    db = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    try:
        cols = [r[1] for r in db.execute("PRAGMA table_info(telegram_pairings)")]
        idcols = [c for c in cols if re.search(r"(user|chat)_?id", c, re.I)]
        for c in idcols:
            for (v,) in db.execute(f"SELECT DISTINCT {c} FROM telegram_pairings"):
                if v is not None and re.fullmatch(r"-?\d{6,}", str(v)):
                    out.add(str(v))
    finally:
        db.close()


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--env", action="append", default=None)
    ap.add_argument("--json", action="append", default=None)
    ap.add_argument("--file", action="append", default=None)
    ap.add_argument("--db", action="append", default=None)
    ap.add_argument("--extra", default=os.path.join(HOME, ".config/conduit/leak-denylist.extra"))
    ap.add_argument("--out", default=OUT)
    a = ap.parse_args()

    envs = a.env or [os.path.join(HOME, "ocgo/.ocgo-secrets.env"), os.path.join(HOME, "ocgo/.secrets")]
    jsons = a.json or [os.path.join(HOME, "ocgo/config.json")] + glob.glob(os.path.join(HOME, ".config/gogcli/*.json"))
    files = a.file or [os.path.join(HOME, ".conduit/auth/mcp_token")]
    dbs = a.db or [os.path.join(HOME, "ocgo/gateway.db")]

    out, used = set(), []
    for fn, paths in ((from_env, envs), (from_json, jsons), (from_db, dbs)):
        for p in paths:
            if os.path.isfile(p):
                try:
                    fn(p, out)
                    used.append(p)
                except Exception as e:  # keep going; report which source failed
                    print(f"skip {p}: {e.__class__.__name__}", file=sys.stderr)
    for p in files:
        if os.path.isfile(p):
            v = keep(open(p, encoding="utf-8", errors="replace").read())
            if v:
                out.add(v)
                used.append(p)
    if os.path.isfile(a.extra):
        for line in open(a.extra, encoding="utf-8"):
            v = line.strip()
            if v and not v.startswith("#") and len(v) >= 6:
                out.add(v)
        used.append(a.extra)

    os.makedirs(os.path.dirname(a.out), mode=0o700, exist_ok=True)
    tmp = a.out + ".tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write("# leak-guard private denylist - generated by scripts/leak-denylist-build.py; do not commit\n")
        for v in sorted(out):
            f.write(v + "\n")
    os.replace(tmp, a.out)
    os.chmod(a.out, 0o600)
    print(f"wrote {len(out)} values to {a.out} from {len(used)} sources")
    for p in used:
        print("  " + p)


if __name__ == "__main__":
    sys.exit(main())
