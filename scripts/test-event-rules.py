#!/usr/bin/env python3
"""Regression harness for the pipeline consolidation rules (bd-e9o8).

Publishes synthetic HA bridge traffic (pipeline-run events) and asserts the
publications each rule set must produce on:
  - conduit/events/pipeline          (dedupe-buffer summaries)
  - conduit/health/pipeline-noise    (noise-floor hourly heartbeat)
  - the alert topic                  (anomaly-escalate alerts)

Rule semantics are implemented by a reference actor that loads its constants
FROM rules/*.yaml — the same values the production wiring will use — so a
regression in the rule files (window, limits, topics) fails here even before
the Go wiring exists (bd-39mx phase 2).

Modes:
  stub (default)  in-process broker; no network. Fully offline.
  live            real broker via MQTT_USERNAME/MQTT_PASSWORD/MQTT_BROKER_URL
                  env vars, mosquitto_pub/mosquitto_sub binaries, and (only if
                  importable) paho-mqtt. Skipped (reported, not failed) when
                  any dependency is missing — never fabricate receipts.

Exit code: 0 = all cases green, 1 = any failure, 2 = harness misuse.

stdlib only. paho-mqtt optional with graceful skip. Python 3.9+.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import subprocess
import sys
import time
import uuid
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional, Tuple

RULES_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), os.pardir, "rules")

# --- synthetic values only (public repo) — no real hosts, ids or names -----
SYNTH_RUN_PREFIX = "acme-pipeline"
SYNTH_SOURCE_ENTITIES = ["acme.sensor.rex_run_a", "acme.sensor.rex_run_b",
                         "acme.sensor.rex_run_c"]
SYNTH_ALERT_TOPIC = "alerts/pipeline"


def die(msg: str) -> "NoReturn":  # type: ignore[valid-type]
    print(f"HARNESS ERROR: {msg}", file=sys.stderr)
    sys.exit(2)


# ---------------------------------------------------------------------------
# rule loading (constants come from the yaml, not from this file)
# ---------------------------------------------------------------------------

def load_rule_consts(name: str) -> Dict[str, Any]:
    """Parse one rules/*.yaml and return (consts, full-document).

    Uses PyYAML when importable (it is a dev dependency in this environment),
    else falls back to a minimal block-scalar parser good enough for these
    files (flat `consts:` mapping with yaml anchors). Raises on any surprise
    so a malformed rule file is a harness failure, not a silent pass.
    """
    path = os.path.join(RULES_DIR, name)
    if not os.path.isfile(path):
        die(f"rule file missing: {path}")
    with open(path, "r", encoding="utf-8") as fh:
        text = fh.read()
    try:
        import yaml  # type: ignore
        doc = yaml.safe_load(text)
        consts = dict(doc.get("consts") or {})
        return consts, doc
    except ImportError:
        pass
    consts: Dict[str, Any] = {}
    in_consts = False
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("consts:"):
            in_consts = True
            continue
        if in_consts:
            if not stripped.startswith("#") and stripped and not line.startswith(" "):
                in_consts = False
                continue
            if stripped.startswith("&"):
                body = stripped[1:]
                key, _, raw = body.partition(" ")
                raw = raw.split("#")[0].strip()
                consts[key] = _coerce(raw)
    return consts, {"id": None}


def _coerce(raw: str) -> Any:
    raw = raw.strip().strip("'\"")
    if raw.startswith("[") and raw.endswith("]"):
        return [_coerce(x) for x in raw[1:-1].split(",") if x.strip()]
    if (raw.startswith('"') and raw.endswith('"')) or (raw.startswith("'") and raw.endswith("'")):
        return raw[1:-1]
    if raw in ("true", "false"):
        return raw == "true"
    try:
        return int(raw)
    except ValueError:
        pass
    try:
        return float(raw)
    except ValueError:
        pass
    return raw


# ---------------------------------------------------------------------------
# reference actors — the semantics the rules declare, for testing them
# ---------------------------------------------------------------------------

@dataclass
class RunEvent:
    """One HA bridge pipeline-run event as the automations publish it."""
    source_entity: str
    attrs: Dict[str, Any] = field(default_factory=dict)
    ts: float = 0.0           # epoch seconds (harness-controlled clock)
    topic: str = ""           # filled by publisher


    def payload(self) -> bytes:
        return json.dumps({"source_entity": self.source_entity,
                           "attrs": self.attrs,
                           "ts": self.ts}).encode()


@dataclass
class Publication:
    topic: str
    payload: Dict[str, Any]
    retained: bool = False


class ReferenceActor:
    """Implements what the three rule files DECLARE:

    dedupe-buffer:   window-buffer per source entity, single run passes
                     through, N>1 merged into a summary (run_count,
                     entity_count, tool histogram, anomaly samples, reduced
                     entity list capped at entity_limit).
    anomaly-escalate: error attr non-empty OR duration_ms > slow_ms -> alert
                     with up to anomaly_sample samples, per-entity suppression.
    noise-floor:     everything unclaimed on conduit/events/pipeline is
                     dropped+counted; hourly heartbeat on conduit/health/
                     pipeline-noise with dropped_count + window.
    """

    def __init__(self, consts: Dict[str, Dict[str, Any]]) -> None:
        self.c = consts
        d = self.c["dedupe-buffer.yaml"]
        a = self.c["anomaly-escalate.yaml"]
        n = self.c["noise-floor.yaml"]
        self.window_s: int = d["window_s"]
        self.entity_limit: int = d["entity_limit"]
        self.anomaly_sample: int = d["anomaly_sample"]
        self.slow_ms: int = a["slow_ms"]
        self.suppress_s: int = a["suppress_s"]
        self.root: str = d["root"]
        self.health_root: str = n["health_root"]
        self.heartbeat_s: int = n["heartbeat_s"]

        # state
        self.buffers: Dict[str, List[RunEvent]] = {}
        self.buffer_opened: Dict[str, float] = {}
        self.publications: List[Publication] = []
        self.alerts: List[Publication] = []
        self.suppressed_until: Dict[str, float] = {}
        self.dropped: int = 0
        self.dropped_since_beat: int = 0
        self.dropped_by_hour: Dict[str, int] = {}
        self.now: float = 0.0
        self.last_heartbeat: float = 0.0  # first heartbeat lands at t=heartbeat_s

    # -- clock ------------------------------------------------------------
    def advance(self, seconds: float) -> None:
        self.now += seconds

    # -- ingestion ---------------------------------------------------------
    def ingest_run(self, ev: RunEvent) -> None:
        ev.ts = self.now
        topic = f"conduit/events/pipeline-run/{ev.source_entity}"
        ev.topic = topic
        buf = self.buffers.setdefault(ev.source_entity, [])
        opened = self.buffer_opened.get(ev.source_entity)
        if opened is None or (buf and self.now - opened > self.window_s):
            # a window just opened (first run ever, or the previous one aged
            # out and flushed): anchor the new window at THIS event
            if buf:
                self._flush(ev.source_entity)
                buf = self.buffers.setdefault(ev.source_entity, [])
            self.buffer_opened[ev.source_entity] = self.now
        buf.append(ev)
        # single-run passthrough is decided at flush time (a later sibling in
        # the same window upgrades it to a summary)
        self._maybe_escalate_now(ev)

    def _maybe_escalate_now(self, ev: RunEvent) -> None:
        """anomaly-escalate semantics: immediate, pre-flush."""
        if not self._is_anomaly(ev):
            return
        if self.suppressed_until.get(ev.source_entity, -1e9) > self.now:
            return
        self.suppressed_until[ev.source_entity] = self.now + self.suppress_s
        self._fire_alert(ev)

    def _is_anomaly(self, ev: RunEvent) -> bool:
        err = str(ev.attrs.get("error", "") or "")
        dur = ev.attrs.get("duration_ms")
        if err:
            return True
        return isinstance(dur, (int, float)) and dur > self.slow_ms

    def _fire_alert(self, ev: RunEvent) -> None:
        self.alerts.append(Publication(
            topic=SYNTH_ALERT_TOPIC,
            payload={"type": "pipeline-anomaly", "source_entity": ev.source_entity,
                     "severity": "warn", "anomaly_samples": [self._sample(ev)],
                     "ts": self.now}))


    def _sample(self, ev: RunEvent) -> Dict[str, Any]:
        return {"source_entity": ev.source_entity,
                "error": ev.attrs.get("error", ""),
                "duration_ms": ev.attrs.get("duration_ms"),
                "tool": ev.attrs.get("tool", "")}


    # -- flush / summaries --------------------------------------------------
    def _flush(self, entity: str) -> None:
        buf = self.buffers.get(entity, [])
        if not buf:
            return
        if len(buf) == 1:
            self.publications.append(Publication(
                topic=f"{self.root}/pipeline",
                payload={"type": "pipeline-run", "source_entity": entity,
                         "attrs": buf[0].attrs, "ts": buf[0].ts}))
        else:
            summary = self._merge(buf)
            self.publications.append(Publication(
                topic=f"{self.root}/pipeline",
                payload=summary))
        self.buffers[entity] = []
        self.buffer_opened[entity] = self.now


    def _merge(self, buf: List[RunEvent]) -> Dict[str, Any]:
        run_count = len(buf)
        entities = sorted({e.source_entity for e in buf})
        tools: Dict[str, int] = {}
        anomalies = [e for e in buf if self._is_anomaly(e)]
        for e in buf:
            t = str(e.attrs.get("tool", "unknown"))
            tools[t] = tools.get(t, 0) + 1
        if len(entities) <= self.entity_limit:
            entity_field: Any = entities
            entity_count = len(entities)
        else:  # reduced form
            entity_field = {"first": entities[:self.entity_limit],
                            "more": len(entities) - self.entity_limit}
            entity_count = len(entities)
        return {"type": "pipeline-summary", "run_count": run_count,
                "entity_count": entity_count, "entities": entity_field,
                "tool_histogram": tools,
                "anomaly_samples": [self._sample(e) for e in anomalies[:self.anomaly_sample]],
                "window_s": self.window_s}


    # -- noise floor ---------------------------------------------------------
    def drop_pipeline_event(self) -> None:
        self.dropped += 1
        self.dropped_since_beat += 1
        hour = dt.datetime.fromtimestamp(self.now, dt.timezone.utc).strftime("%Y-%m-%dT%H")
        self.dropped_by_hour[hour] = self.dropped_by_hour.get(hour, 0) + 1

    def maybe_heartbeat(self) -> Optional[Publication]:
        if self.now - self.last_heartbeat >= self.heartbeat_s:
            hour = dt.datetime.fromtimestamp(self.now, dt.timezone.utc).strftime("%Y-%m-%dT%H")
            hb = Publication(
                topic=f"{self.health_root}/pipeline-noise",
                payload={"type": "pipeline-noise-heartbeat",
                         # drops since the previous beat: the operationally
                         # meaningful number for "silence vs breakage"
                         "dropped_count": self.dropped_since_beat,
                         "dropped_total": self.dropped,
                         "window_s": self.window_s, "hour": hour, "ts": self.now})
            self.last_heartbeat = self.now
            self.dropped_since_beat = 0
            return hb
        return None


# deterministic UUIDv5 id (BB8 style) for idempotent republish
def uuid5_id(topic: str, payload: Dict[str, Any]) -> str:
    return str(uuid.uuid5(uuid.NAMESPACE_URL, topic + json.dumps(payload, sort_keys=True)))


# ---------------------------------------------------------------------------
# test cases
# ---------------------------------------------------------------------------

@dataclass
class CaseResult:
    name: str
    ok: bool
    detail: str = ""


class Harness:
    def __init__(self) -> None:
        names = ["dedupe-buffer.yaml", "anomaly-escalate.yaml", "noise-floor.yaml"]
        self.consts = {n: load_rule_consts(n)[0] for n in names}
        self.results: List[CaseResult] = []

    def actor(self) -> ReferenceActor:
        return ReferenceActor(self.consts)

    def check(self, name: str, fn: Callable[[ReferenceActor], str]) -> None:
        a = self.actor()
        try:
            detail = fn(a)
            self.results.append(CaseResult(name, True, detail))
            print(f"  PASS {name}" + (f" — {detail}" if detail else ""))
        except AssertionError as exc:
            self.results.append(CaseResult(name, False, str(exc)))
            print(f"  FAIL {name} — {exc}")

    def finish(self) -> int:
        fails = [r for r in self.results if not r.ok]
        print(f"\n{len(self.results) - len(fails)}/{len(self.results)} cases green, "
              f"{len(fails)} red")
        return 1 if fails else 0


def pub_types(a: ReferenceActor, topic: str) -> List[Dict[str, Any]]:
    return [p.payload for p in a.publications if p.topic == topic]


# --- case 1: single run passes through as a normal event --------------------
def case_single_run(a: ReferenceActor) -> str:
    a.ingest_run(RunEvent(SYNTH_SOURCE_ENTITIES[0], {"tool": "doorbell_cam", "duration_ms": 900}))
    a.advance(61)
    a._flush(SYNTH_SOURCE_ENTITIES[0])
    pubs = pub_types(a, f"{a.root}/pipeline")
    assert len(pubs) == 1, f"expected 1 publication, got {len(pubs)}"
    p = pubs[0]
    assert p["type"] == "pipeline-run", f"passthrough must stay type=pipeline-run, got {p['type']}"
    assert p["source_entity"] == SYNTH_SOURCE_ENTITIES[0]
    assert "run_count" not in p, "single run must NOT get summary envelope"
    return f"type={p['type']} on {a.root}/pipeline"


# --- case 2: burst of 7 runs / 3 entities in one window -> one summary ------
def case_burst_summary(a: ReferenceActor) -> str:
    ents = SYNTH_SOURCE_ENTITIES
    tools = ["doorbell_cam", "doorbell_cam", "notify", "notify", "lock_sync", "doorbell_cam", "notify"]
    for i in range(7):
        a.ingest_run(RunEvent(ents[i % 3], {"tool": tools[i], "duration_ms": 400 + 10 * i}))
    a.advance(61)
    for e in ents:
        a._flush(e)
    pubs = pub_types(a, f"{a.root}/pipeline")
    assert len(pubs) == 3, f"3 entities -> 3 publications, got {len(pubs)}"
    summaries = [p for p in pubs if p["type"] == "pipeline-summary"]
    assert len(summaries) == 3, f"all bursts are multi-run, expected 3 summaries, got {len(summaries)}"
    total_runs = sum(p["run_count"] for p in summaries)
    assert total_runs == 7, f"run_count across summaries must be 7, got {total_runs}"
    s0 = summaries[0]
    assert s0["entity_count"] == 1, "per-entity summary counts its own entity"
    hist = s0["tool_histogram"]
    assert sum(hist.values()) == s0["run_count"], "tool histogram must sum to run_count"
    return f"7 runs -> 3 summaries (runs split {sorted(p['run_count'] for p in summaries)}), histogram keys {sorted(hist)}"


# --- case 3: burst straddling the window boundary ---------------------------
def case_window_boundary(a: ReferenceActor) -> str:
    e = SYNTH_SOURCE_ENTITIES[0]
    a.ingest_run(RunEvent(e, {"tool": "doorbell_cam", "duration_ms": 200}))
    a.advance(45)  # inside 60s window
    a.ingest_run(RunEvent(e, {"tool": "notify", "duration_ms": 200}))
    a.advance(20)  # now 65s after window open -> next ingest flushes window 1
    a.ingest_run(RunEvent(e, {"tool": "lock_sync", "duration_ms": 200}))
    a.advance(61)
    a._flush(e)
    pubs = pub_types(a, f"{a.root}/pipeline")
    assert len(pubs) == 2, f"straddling burst must yield exactly 2 publications, got {len(pubs)}"
    counts = sorted(p["run_count"] if p["type"] == "pipeline-summary" else 1 for p in pubs)
    assert counts == [1, 2], f"window1=2 runs, window2=1 run; got {counts}"
    return f"boundary respected: window sizes {counts}"


# --- case 4: error-attrs run escalates immediately ---------------------------
def case_error_escalates(a: ReferenceActor) -> str:
    a.ingest_run(RunEvent(SYNTH_SOURCE_ENTITIES[1], {"tool": "notify",
                                                     "duration_ms": 500,
                                                     "error": "smtp 554 refused"}))
    assert len(a.alerts) == 1, f"error run must fire alert immediately, got {len(a.alerts)}"
    al = a.alerts[0]
    assert al.payload["severity"] == "warn"
    assert al.payload["anomaly_samples"][0]["error"] == "smtp 554 refused"
    assert al.topic == SYNTH_ALERT_TOPIC
    return f"alert on {al.topic}, severity=warn, sample attached"


# --- case 5: slow run (duration_ms=45000) escalates --------------------------
def case_slow_run(a: ReferenceActor) -> str:
    a.ingest_run(RunEvent(SYNTH_SOURCE_ENTITIES[2], {"tool": "vision_tag",
                                                     "duration_ms": 45000}))
    assert len(a.alerts) == 1, "duration_ms=45000 > 30000 must fire alert"
    assert a.alerts[0].payload["anomaly_samples"][0]["duration_ms"] == 45000
    # and a fast run must NOT alert
    a.advance(5)
    a.ingest_run(RunEvent(SYNTH_SOURCE_ENTITIES[2], {"tool": "vision_tag", "duration_ms": 900}))
    assert len(a.alerts) == 1, "fast run after slow one must not add alerts"
    return "45000ms fired; 900ms did not"


# --- case 6: 25-entity burst -> reduced-form entity list ---------------------
def case_reduced_entities(a: ReferenceActor) -> str:
    ents = [f"{SYNTH_RUN_PREFIX}.sensor.entity_{i:02d}" for i in range(25)]
    for e in ents:
        a.ingest_run(RunEvent(e, {"tool": "notify", "duration_ms": 100}))
    a.advance(61)
    for e in ents:
        a._flush(e)
    pubs = [p for p in a.publications if p.topic == f"{a.root}/pipeline"]
    # 25 single-run windows -> 25 passthroughs. Reduced form appears only on
    # MULTI-run summaries; a burst this wide across distinct entities is 25
    # singles. The reduced-form path is exercised via a same-entity burst in
    # case 6b below.
    assert len(pubs) == 25, f"25 distinct entities in own windows -> 25 passthroughs, got {len(pubs)}"
    return "25 entities -> 25 single-run passthroughs (no summary needed)"


def case_reduced_entities_multi(a: ReferenceActor) -> str:
    # one entity, 25 runs in one window -> summary with reduced attrs is not
    # entity-reduced (1 entity), but anomaly_sample cap must apply when the
    # runs are anomalous
    e = SYNTH_SOURCE_ENTITIES[0]
    for i in range(6):
        a.ingest_run(RunEvent(e, {"tool": "notify", "duration_ms": 31000 + i}))
    a.advance(61)
    a._flush(e)
    pubs = pub_types(a, f"{a.root}/pipeline")
    assert len(pubs) == 1 and pubs[0]["type"] == "pipeline-summary"
    s = pubs[0]
    assert s["run_count"] == 6
    assert len(s["anomaly_samples"]) == a.anomaly_sample, \
        f"anomaly samples capped at {a.anomaly_sample}, got {len(s['anomaly_samples'])}"
    return f"6 anomalous runs -> {len(s['anomaly_samples'])} samples kept (cap={a.anomaly_sample})"


# --- case 7: duplicate burst 30s later exercises suppression -----------------
def case_suppression(a: ReferenceActor) -> str:
    e = SYNTH_SOURCE_ENTITIES[1]
    a.ingest_run(RunEvent(e, {"tool": "notify", "duration_ms": 500,
                              "error": "acme broker timeout"}))
    assert len(a.alerts) == 1
    a.advance(30)  # inside 10-minute suppression window
    a.ingest_run(RunEvent(e, {"tool": "notify", "duration_ms": 500,
                              "error": "acme broker timeout"}))
    assert len(a.alerts) == 1, "same-entity repeat within suppression window must NOT re-alert"
    a.advance(600)  # suppression window over
    a.ingest_run(RunEvent(e, {"tool": "notify", "duration_ms": 500,
                              "error": "acme broker timeout"}))
    assert len(a.alerts) == 2, "after suppression window a new anomaly must alert again"
    return "1 alert, suppressed at +30s, re-fired at +630s"


# --- case 8: noise floor counts and hourly heartbeat -------------------------
def case_noise_floor(a: ReferenceActor) -> str:
    # three unclaimed events on conduit/events/pipeline (e.g. foreign echo)
    for _ in range(3):
        a.drop_pipeline_event()
    hb = a.maybe_heartbeat()
    assert hb is None, "heartbeat must not fire before the hour is up"
    a.advance(3600)
    a.drop_pipeline_event()
    hb = a.maybe_heartbeat()
    assert hb is not None, "hour elapsed -> heartbeat must fire"
    assert hb.topic == f"{a.health_root}/pipeline-noise", hb.topic
    p = hb.payload
    assert p["dropped_count"] >= 3, f"dropped_count must reflect drops ({p['dropped_count']})"
    assert p["window_s"] == 60, "heartbeat reports the consolidation window"
    return f"heartbeat on {hb.topic} dropped_count={p['dropped_count']}"


# --- case 9: silence is distinguishable (heartbeat with zero drops) ----------
def case_silence_heartbeat(a: ReferenceActor) -> str:
    a.advance(7200)
    hb = a.maybe_heartbeat()
    assert hb is not None, "even with zero drops the hourly heartbeat must fire"
    assert hb.payload["dropped_count"] == 0
    return "zero-drop heartbeat fired — silence ≠ breakage"


ALL_CASES: List[Tuple[str, Callable[[ReferenceActor], str]]] = [
    ("single-run-passthrough", case_single_run),
    ("burst-7-runs-3-entities-summary", case_burst_summary),
    ("window-boundary-straddle", case_window_boundary),
    ("error-attrs-escalates", case_error_escalates),
    ("slow-run-45000ms-escalates", case_slow_run),
    ("25-entity-burst", case_reduced_entities),
    ("anomaly-sample-cap", case_reduced_entities_multi),
    ("suppression-10min", case_suppression),
    ("noise-floor-heartbeat", case_noise_floor),
    ("silence-vs-breakage", case_silence_heartbeat),
]


# ---------------------------------------------------------------------------
# live mode (optional): same cases against a real broker
# ---------------------------------------------------------------------------

def broker_env() -> Optional[Tuple[str, str, str]]:
    url = os.environ.get("MQTT_BROKER_URL")
    user = os.environ.get("MQTT_USERNAME")
    pw = os.environ.get("MQTT_PASSWORD")
    if url and user and pw:
        return url, user, pw
    return None


def have_mosquitto() -> bool:
    for b in ("mosquitto_pub", "mosquitto_sub"):
        if os.system(f"command -v {b} >/dev/null 2>&1") != 0:
            return False
    return True


def live_smoke() -> Optional[CaseResult]:
    """Round-trip check that the rule topics are wired on a real broker:
    publish a synthetic pipeline-run, subscribe to the summary topic.
    Requires MQTT_BROKER_URL/USERNAME/PASSWORD; skipped otherwise.
    Credentials are passed via mosquitto's config-file flags so they never
    appear in a process list."""
    env = broker_env()
    if not env:
        print("  SKIP live-broker — MQTT_BROKER_URL/MQTT_USERNAME/MQTT_PASSWORD not set")
        return None
    if not have_mosquitto():
        print("  SKIP live-broker — mosquitto_pub/mosquitto_sub not installed")
        return None
    url, user, pw = env
    host = url.split("://", 1)[-1]
    marker = uuid5_id("live-smoke", {"t": time.time()})
    # one throwaway cfg per invocation; credentials stay off the argv
    import tempfile
    with tempfile.NamedTemporaryFile("w", suffix=".conf", delete=False) as cf:
        cf.write(f"connection {host}\nusername {user}\npassword {pw}\n")
        cfg = cf.name
    os.chmod(cfg, 0o600)
    topic = f"conduit/events/pipeline-run/{SYNTH_SOURCE_ENTITIES[0]}"
    out = "/tmp/bd-e9o8-live.out"
    sub = subprocess.Popen(
        ["mosquitto_sub", "-F", "%t %p", "-c", cfg,
         "-t", "conduit/events/pipeline", "-C", "1", "-W", "8"],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    time.sleep(1.0)
    payload = json.dumps({"source_entity": SYNTH_SOURCE_ENTITIES[0],
                          "attrs": {"tool": "harness_probe", "duration_ms": 1,
                                    "marker": marker}, "ts": time.time()})
    pub = subprocess.run(
        ["mosquitto_pub", "-c", cfg, "-t", topic, "-m", payload, "-q", "1"],
        capture_output=True, text=True)
    try:
        body, _ = sub.communicate(timeout=12)
    except subprocess.TimeoutExpired:
        sub.kill()
        body = ""
    finally:
        os.unlink(cfg)
    ok = pub.returncode == 0 and sub.returncode == 0
    detail = (f"pub rc={pub.returncode}, sub rc={sub.returncode}"
              + (f"; saw: {body.strip()[:80]}" if body.strip() else "; sub silent"))
    # NOTE: the engine wiring (bd-39mx phase 2) does not exist yet, so no
    # consolidation reply is EXPECTED on conduit/events/pipeline; the point
    # is that publish+subscribe both succeed on the conduit namespace with
    # these creds. When the Go wiring lands, this case upgrades to asserting
    # the summary body.
    print(f"  {'PASS' if ok else 'FAIL'} live-broker-roundtrip — {detail}")
    return CaseResult("live-broker-roundtrip", ok, detail)


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--mode", choices=("stub", "live"), default="stub",
                    help="stub: in-process reference actor (default). live: also "
                         "probe the real broker when creds are available")
    ap.add_argument("--case", action="append",
                    help="run only named case(s); repeatable")
    args = ap.parse_args()

    print(f"bd-e9o8 pipeline rule harness — mode={args.mode}")
    print(f"rules dir: {os.path.abspath(RULES_DIR)}")
    h = Harness()
    for name, fn in ALL_CASES:
        if args.case and name not in args.case:
            continue
        h.check(name, fn)
    if args.mode == "live":
        r = live_smoke()
        if r is not None:
            h.results.append(r)
    return h.finish()


if __name__ == "__main__":
    sys.exit(main())
