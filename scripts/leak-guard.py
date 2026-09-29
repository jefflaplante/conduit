#!/usr/bin/env python3
"""leak-guard: block commits/pushes that would publish secrets or personal data.

This repository is public. The guard scans only *added* content and fails
when it finds either:

  * a built-in credential pattern (Anthropic/OpenAI/OpenRouter keys,
    Telegram bot tokens, GitHub tokens, private keys, JWTs), or
  * any value from a private denylist kept OUTSIDE the repo
    (default ~/.config/conduit/leak-denylist, override with
    $CONDUIT_LEAK_DENYLIST). Build it with scripts/leak-denylist-build.py.
    A plain line is a case-sensitive substring (secrets, IDs, e-mails; at
    least 6 chars). A line `word:VALUE` matches VALUE as a whole word,
    ignoring case (personal names, pets, places, employer; at least 3
    chars), so short terms do not fire inside unrelated words.

Findings print as file:line and a rule name; matched values are never printed.

Usage:
  leak-guard.py --staged            # pre-commit: staged changes
  leak-guard.py --range A..B        # pre-push: commits in a range
  leak-guard.py --all               # every tracked file in the work tree

Allow a known-safe line (placeholders, test sentinels) by adding the marker
`leak-guard:allow` on the same line. Denylist values cannot be allowed.
"""

import argparse
import os
import re
import subprocess
import sys

ALLOW_MARKER = "leak-guard:allow"

PATTERNS = [
    ("anthropic-key", re.compile(r"sk-ant-(?:api|oat|admin)\d{2}-[A-Za-z0-9_-]{20,}")),
    ("openai-key", re.compile(r"\bsk-(?:proj-|svcacct-)?[A-Za-z0-9]{32,}")),
    ("openrouter-key", re.compile(r"\bsk-or-v1-[a-f0-9]{32,}")),
    ("telegram-bot-token", re.compile(r"\b\d{8,10}:AA[A-Za-z0-9_-]{33}\b")),
    ("github-token", re.compile(r"\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,})")),
    ("private-key", re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----")),
    ("jwt", re.compile(r"\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}")),
    ("aws-access-key", re.compile(r"\b(?:AKIA|ASIA)[A-Z0-9]{16}\b")),
    ("google-oauth-secret", re.compile(r"\bGOCSPX-[A-Za-z0-9_-]{20,}")),
]

# Placeholders that match a pattern but are documentation, not credentials.
PLACEHOLDER = re.compile(r"(?i)(REDACTED|EXAMPLE|PLACEHOLDER|x{8,}|0{16,}|your[-_]?(api[-_]?)?key)")

# Binary or vendored paths that are never scanned.
SKIP_PATH = re.compile(r"\.(png|jpe?g|gif|ico|pdf|woff2?|ttf|gz|zip|tar|db|sqlite|pyc)$|(^|/)vendor/|\.min\.js$")


def default_denylist_path():
    return os.environ.get("CONDUIT_LEAK_DENYLIST") or os.path.expanduser("~/.config/conduit/leak-denylist")


def load_denylist(path):
    """One value per line; '#' comments. Returns (matchers, warning).

    A matcher is a str (substring) or a compiled pattern (`word:` entry)."""
    if not os.path.exists(path):
        return [], f"leak-guard: denylist {path} not found; only built-in patterns are checked " \
                   f"(run scripts/leak-denylist-build.py)"
    mode = os.stat(path).st_mode & 0o777
    warn = None
    if mode & 0o077:
        warn = f"leak-guard: warning: {path} is mode {oct(mode)}; it should be 0600"
    values, words = [], []
    with open(path, encoding="utf-8") as f:
        for line in f:
            v = line.rstrip("\n")
            if not v or v.startswith("#"):
                continue
            if v.startswith("word:"):
                w = v[len("word:"):].strip()
                if len(w) >= 3:
                    words.append(re.escape(w))
            elif len(v) >= 6:
                values.append(v)
    if words:
        # One alternation keeps history-wide scans fast.
        values.append(re.compile(r"(?<![A-Za-z0-9])(?:" + "|".join(words) + r")(?![A-Za-z0-9])", re.I))
    return values, warn


def git(*args):
    return subprocess.run(["git", *args], capture_output=True, text=True, errors="replace", check=True).stdout


def added_lines_from_diff(diff):
    """Yield (path, lineno, text) for added lines in a unified diff."""
    path, lineno = None, 0
    for raw in diff.splitlines():
        if raw.startswith("+++ "):
            p = raw[4:]
            path = None if p == "/dev/null" else p[2:] if p.startswith("b/") else p
            continue
        if raw.startswith("@@"):
            m = re.search(r"\+(\d+)", raw)
            lineno = int(m.group(1)) if m else 0
            continue
        if path is None or raw.startswith("---"):
            continue
        if raw.startswith("+"):
            yield path, lineno, raw[1:]
            lineno += 1
        elif not raw.startswith("-"):
            lineno += 1


def scan_lines(items, denylist):
    findings = []
    for path, lineno, text in items:
        if SKIP_PATH.search(path):
            continue
        for v in denylist:
            if (v in text) if isinstance(v, str) else v.search(text):
                findings.append((path, lineno, "private-denylist value"))
                break
        if ALLOW_MARKER in text:
            continue
        for name, rx in PATTERNS:
            m = rx.search(text)
            if m and not PLACEHOLDER.search(m.group(0)):
                findings.append((path, lineno, name))
    return findings


def staged_items():
    return added_lines_from_diff(git("diff", "--cached", "-U0", "--no-color", "--no-ext-diff", "--text"))


def range_items(rng):
    # Every added line in every commit of the range, so an earlier commit
    # that adds a secret and a later one that removes it is still caught.
    items = []
    # rng may hold several rev-list arguments, e.g. "<sha> --not --remotes".
    for sha in git("rev-list", "--reverse", *rng.split()).split():
        diff = git("show", "--format=", "-U0", "--no-color", "--no-ext-diff", "--text", "--first-parent", sha)
        items.extend((f"{p} @{sha[:8]}", n, t) for p, n, t in added_lines_from_diff(diff))
        msg = git("log", "-1", "--format=%B", sha)
        items.extend((f"<commit message> @{sha[:8]}", i + 1, t) for i, t in enumerate(msg.splitlines()))
    return items


def all_items():
    for path in git("ls-files", "-z").split("\0"):
        if not path or SKIP_PATH.search(path) or not os.path.isfile(path):
            continue
        try:
            with open(path, encoding="utf-8", errors="replace") as f:
                for i, text in enumerate(f, 1):
                    yield path, i, text.rstrip("\n")
        except OSError:
            continue


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--staged", action="store_true")
    g.add_argument("--range")
    g.add_argument("--all", action="store_true")
    ap.add_argument("--denylist", default=default_denylist_path())
    args = ap.parse_args()

    denylist, warn = load_denylist(args.denylist)
    if warn:
        print(warn, file=sys.stderr)

    if args.staged:
        items = staged_items()
    elif args.range:
        items = range_items(args.range)
    else:
        items = all_items()

    findings = scan_lines(items, denylist)
    if not findings:
        return 0
    print("leak-guard: BLOCKED - possible secret or personal data in new content:", file=sys.stderr)
    for path, lineno, rule in findings:
        print(f"  {path}:{lineno}: {rule}", file=sys.stderr)
    print("Remove the value (use a placeholder such as owner@example.com or ${ENV_VAR}).\n"
          "For a genuine placeholder that trips a pattern, add 'leak-guard:allow' to that line.\n"
          "Private config belongs in ~/ocgo/config.json, never in this public repo.", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
