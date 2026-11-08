# Pipeline-Run Consolidation (bd-e9o8)

Pre-build for **bd-39mx phase 2** (HA → MQTT → Conduit bridges). The 5 Tier-1
bridge automations each chain several small HA automations; every hop emits a
noisy `pipeline-run` event. Without consolidation, wiring the bridges would
flood `conduit/events/#` and every rule subscribed to it. This rule set
consolidates the noise, escalates the failures, and keeps a heartbeat so
silence stays distinguishable from breakage.

**Status: specification + reference semantics + regression harness.** The
Conduit event-rule engine (`internal/mqttevents`, bd-39mx v1) today supports
`state_change` / `threshold` conditions and `spawn` / `queue` / `log` actions.
The three rule files under `rules/` declare the window-buffer / publish /
alert / drop-and-count semantics phase 2 needs; they are **not yet loadable by
the engine**. `scripts/test-event-rules.py` implements the same semantics as a
reference actor whose constants are loaded **from the rule YAML files**, so
the consolidation contract is executable today and the Go wiring has a
regression target to converge on.

## Topic layout

| Topic | Direction | Content |
|---|---|---|
| `conduit/events/pipeline-run/<source_entity>` | HA → Conduit | one event per automation hop (`attrs`: run_id, tool, model, duration_ms, error, …) |
| `conduit/events/pipeline` | Conduit (publish) | consolidated output: single-run passthrough or merged summary |
| `conduit/health/pipeline-noise` | Conduit (publish) | hourly noise-floor heartbeat (`dropped_count`, `window`) |
| `alerts/pipeline` | Conduit (alert path) | anomaly escalations, `severity=warn` |

Publish style is **deterministic UUIDv5** ids (BB8 convention: unsolicited
publisher, never expects or reads replies). Re-publishing the same window
content produces the same id — consumers can dedupe republishes idempotently.

## Rule matrix

### 1. `rules/dedupe-buffer.yaml` — `pipeline-dedupe-buffer`

| | |
|---|---|
| Trigger | `conduit/events/pipeline-run/#` |
| Condition | `window_buffer` keyed on `source_entity` |
| Action | `publish` to `conduit/events/pipeline` |

Behavior:

- Events buffer **per source entity** for `window_s` (60 s). A window opens
  at the entity's first run; runs arriving after `window_s` from the anchor
  belong to the next window (tumbling, not sliding — an anchor never moves
  because a later run arrived).
- **Flush with 1 run** → passthrough: a normal `pipeline-run` event, no
  summary envelope.
- **Flush with N>1 runs** → one `pipeline-summary`:
  `run_count`, `entity_count`, `tool_histogram` (tool name → count),
  `anomaly_samples` (up to 3 error/slow runs quoted), `window_s`.
- **Entity explosion:** a summary covering more than `entity_limit` (20)
  entities switches to reduced form — first 20 entity ids plus a count of the
  rest. Per-entity summaries list their own entity only, so the reduced form
  fires only when a single window spans many entities (fan-out automations).
- `window_timeout_ms` (5000) bounds how long the HA automation may take to
  append its runs to a window before the flush anyway.

### 2. `rules/anomaly-escalate.yaml` — `pipeline-anomaly-escalate`

| | |
|---|---|
| Trigger | `conduit/events/#` (runs and summaries) |
| Condition | `attrs_any`: `attrs.error` non-empty **or** `attrs.duration_ms > 30000` |
| Action | `alert` on `alerts/pipeline`, `severity=warn`, anomaly samples attached (max 3) |

Behavior:

- Escalation is **immediate** on the raw run — it does not wait for the
  consolidation flush. A failing hop is alert-worthy while its siblings are
  still arriving.
- **Suppression:** repeat fires for the same `source_entity` within
  `suppress_s` (600 s) are dropped. The first alert already carries samples;
  a flapping automation must not page every hop.
- Runs and merged summaries are both eligible (a summary whose samples show
  errors escalates too, if not already suppressed).

### 3. `rules/noise-floor.yaml` — `pipeline-noise-floor`

| | |
|---|---|
| Trigger | `conduit/events/pipeline` |
| Condition | `catch_all` (everything neither rule above claimed) |
| Action | `drop_and_count` + hourly `heartbeat` publish |

Behavior:

- Unclaimed events on `conduit/events/pipeline` are dropped **silently** but
  counted per hour bucket.
- Once per hour (`heartbeat_s` 3600) a heartbeat publishes to
  `conduit/health/pipeline-noise`: `dropped_count` (since previous beat),
  `dropped_total`, `window_s`. A missing heartbeat = pipeline broken; a
  heartbeat with `dropped_count=0` = healthy silence.

## Config knobs

All knobs live once in each rule's `consts:` block and are referenced by YAML
anchor elsewhere in the same file — **edit the value, never the logic**. The
harness loads the same files, so editing a knob without updating the docs
table fails nothing silently: the harness re-reads the YAML every run.

| Knob | Default | File | Meaning |
|---|---|---|---|
| `window_s` | 60 | dedupe-buffer | consolidation window per source entity |
| `window_timeout_ms` | 5000 | dedupe-buffer | max time for an automation to append runs |
| `entity_limit` | 20 | dedupe-buffer | entities listed in full before reduced form |
| `anomaly_sample` | 3 | dedupe-buffer, anomaly-escalate | samples quoted per summary/alert |
| `root` | `conduit/events` | dedupe-buffer | publish namespace root |
| `slow_ms` | 30000 | anomaly-escalate | duration threshold for a slow run |
| `suppress_s` | 600 | anomaly-escalate | per-entity alert suppression window |
| `health_root` | `conduit/health` | noise-floor | heartbeat topic root |
| `heartbeat_s` | 3600 | noise-floor | noise heartbeat cadence |

## Harness usage

```
python3 scripts/test-event-rules.py                # stub mode (default, offline)
python3 scripts/test-event-rules.py --mode live    # also probe real broker (see below)
python3 scripts/test-event-rules.py --case suppression-10min
```

- **Stub mode** (default): in-process reference actor, no network, stdlib
  only. Constants are read from `rules/*.yaml` — a knob edit changes behavior
  everywhere consistently, and a malformed rule file fails the harness.
- **Live mode**: additionally round-trips one synthetic publish/subscribe on
  the real broker via `mosquitto_pub`/`mosquitto_sub`, credentials via env
  (`MQTT_BROKER_URL`, `MQTT_USERNAME`, `MQTT_PASSWORD`) passed through a
  mode-0600 temp config file so they never hit a process list. Skipped with
  an explicit `SKIP` line (never a failure) when env or binaries are absent.
  Until the phase-2 engine wiring lands, live mode asserts connectivity
  only — no consolidation reply is expected yet.

Cases (10): single-run passthrough; 7-run/3-entity burst summary; window
boundary straddle; error-attrs escalation; slow-run (45 s) escalation; 25-
entity burst; anomaly-sample cap; 10-minute suppression; noise-floor
heartbeat; zero-drop (silence vs breakage) heartbeat. Exit code 0 = all
green, 1 = any failure, 2 = harness misuse.

## Known edge cases

1. **Tumbling, not sliding.** A burst straddling a window boundary yields two
   publications (sizes N−k and k). This is intended: a sliding window would
   double-count runs across flushes.
2. **Wide fan-out = many passthroughs, not one summary.** 25 entities each
   with one run produce 25 passthrough events (one per entity window), not a
   reduced-form summary — the reduced form exists for multi-entity *summary*
   payloads only. If bridge traffic shows wide single-run fan-outs being
   noisy, lower `window_s` or add a coarser per-automation key.
3. **Anomaly first.** Escalation fires on the raw run, pre-flush; the later
   summary still carries the run in its samples. One failure = one alert +
   one sampled summary, never two alerts.
4. **Suppression is per entity, not per error.** Two different errors on the
   same entity inside 10 minutes produce one alert (with one sample). The
   samples-first policy keeps alerts small; if distinct errors must page
   separately, key suppression on `source_entity + error`.
5. **Heartbeat counts since previous beat**, not calendar-hour buckets — the
   first beat after deploy reports drops since startup.
6. **Clock = event timestamps.** The reference actor uses a harness clock;
   the Go wiring must use event `ts` fields, not broker receive time, or a
   burst replayed/bridged late would split windows.
7. **Rules don't parse arbitrary payloads.** `attrs.error`/`duration_ms` are
   the contract. An automation publishing a different shape lands in the
   noise floor (counted, not alerted) — visible in heartbeat `dropped_count`,
   not silent data loss.

## Deviation from in-repo conventions (documented)

- The repo's live event rules live in the **deployed gateway config**
  (`mqtt.events.rules` JSON), not in-repo; there is no existing `rules/`
  directory or YAML rule convention to mirror. Deviation: rule files are
  standalone YAML under `rules/` with a `consts:` block (constants by anchor,
  referenced by name) so the values are single-sourced and harness-checkable.
  When phase 2 wires these into the engine, either (a) the engine grows
  native support for these condition/action types and the YAML translates
  into `mqtt.events.rules` JSON entries, or (b) the YAML becomes the source
  of truth loaded by a config generator. Choice is phase 2's; the harness
  follows whichever lands.
- Harness is plain `python3` stdlib (task requirement); the repo has no
  existing Python MQTT/event-rule test pattern to follow (checked
  `scripts/`, `test/` — Go and shell only). PyYAML is used when importable
  with a fallback mini-parser, because the repo itself vendors YAML tooling
  for Go, not Python.
