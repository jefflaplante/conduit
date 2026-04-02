# Conduit Critical Review: SRE Perspective & Agent Harness Assessment

## Context

Jeff requested a comprehensive review of Conduit's current state with focus on:
1. **SRE utility** — How well does Conduit support an AI agent in daily SRE work?
2. **Agent harness quality** — How effectively does the system keep an agent focused, on-task, and genuinely helpful?

---

## Executive Summary

**Conduit is a solid foundation** — clean Go architecture, comprehensive tooling for K8s/SSH, thoughtful prompt engineering with priority-based section management. However, it's **not yet a complete SRE platform**.

| Area | Grade | Summary |
|------|-------|---------|
| K8s Operations | A | Production-ready with security tiers, audit logging, port-forward, exec |
| SSH Fleet Management | A | Excellent: inventory integration, fan-out, audit trail, security classification |
| Incident Management | F | PagerDuty/Datadog stubs exist but **no tool exposure** |
| Metrics Visibility | F | Cannot query Datadog, no K8s resource metrics (`top` not implemented) |
| Agent Focus Keeping | B+ | Strong fabrication prevention, depth limits, but weak mid-task refocusing |
| Observability | C | Metrics exist but no structured logging, no distributed tracing |
| Architecture | B+ | Clean separation, good patterns, but large files and hardcoded timeouts |

---

## Part 1: SRE Tooling Capabilities

### What Works Well Today

**Kubernetes** (`internal/tools/k8s/`)
- Full CRUD on common resources (pods, deployments, services, configmaps, secrets)
- Pod exec for remote debugging
- Port forwarding (10 concurrent tunnels)
- Rolling restarts, scale operations
- Event monitoring and bounded watches (30-120s)
- 4-tier security classification (read/modify/dangerous/blocked)

**SSH** (`internal/tools/ssh/`)
- One-shot and fan-out execution across hosts
- Persistent sessions (5 concurrent, 10min idle timeout)
- Ansible inventory integration (INI/YAML/dynamic scripts)
- SCP file transfer
- Comprehensive JSONL audit logging with secret redaction
- Command classification and blocking

**MQTT** (`internal/tools/mqtt/`)
- Device discovery, status, topic enumeration
- Event history and pattern-matched querying
- Good for IoT/home automation SRE

### Critical Gaps for SRE Work

**1. Incident Management Integration — THE BIG MISS**

The codebase has `internal/tools/datadog/` and `internal/tools/pagerduty/` directories with HTTP clients configured, but **no tool implementations exposed**. An SRE cannot:
- List/ack/resolve PagerDuty incidents
- Query Datadog metrics or logs
- Correlate alerts to infrastructure state
- Close the loop: alert → investigation → resolution

**Impact**: Conduit can investigate (K8s, SSH) but cannot initiate from or resolve to the incident system. The agent is deaf to the alerting ecosystem.

**2. Kubernetes Metrics — Blind Spot**

The K8s tool has a `top` action that explicitly returns "not yet implemented - requires metrics API". An SRE cannot:
- See pod CPU/memory usage
- Identify resource-constrained workloads
- Answer "why is this pod slow?"

**3. No Dry-Run/Preview for Dangerous Operations**

K8s changes cannot be validated before execution. No `kubectl apply --dry-run` equivalent. Agent must either commit to action or refuse.

**4. RBAC Visibility Missing**

No way to check what permissions the agent has before attempting an operation. Leads to permission-denied failures mid-investigation.

### Beads Backlog Alignment

The open beads confirm these gaps are known and prioritized:
- `conduit-1fu` (P2 epic): Datadog integration
- `conduit-dy7` (P2 epic): PagerDuty integration
- `conduit-3qy` (P2 feature): SRE Incident Correlation Engine

**Recommendation**: These are the right priorities. Completing Datadog + PagerDuty tools transforms Conduit from "investigation helper" to "incident lifecycle manager."

---

## Part 2: Agent Harness Assessment

### Strengths — What Keeps the Agent Focused

**1. Multi-Layer Prompt Architecture** (`internal/agent/prompt_builder.go`)
- 20+ sections with P1-P4 priority
- Token-budget-aware inclusion (small models shed P3-P4 gracefully)
- P1 sections (identity, safety, tool integrity) **never dropped**

**2. Fabrication Prevention — Excellent**
- Explicit "never fabricate tool results" rules in multiple places
- Active detection of spawn claims without actual `SessionsSpawn` calls
- System injects corrections when agent claims actions it didn't take

**3. Tool Chaining Limits**
- Max depth 25 (configurable)
- Prevents runaway loops
- Thinking indicator feedback at each depth level

**4. Context Window Management** (`internal/ai/messages.go`)
- Token-aware history retrieval, not fixed message count
- Smart trimming preserves system prompt + current user message
- Per-session budgets prevent cross-contamination

**5. Heartbeat/Silent Token System**
- `HEARTBEAT_OK` and `SILENT_REPLY_TOKEN` prevent noise
- Heuristic detection suppresses "nothing to report" chatter

### Weaknesses — Where Agents Drift

**1. No Mid-Task Refocusing**

After a 15-step tool chain, the agent has no checkpoint reminding it of the original goal. It relies entirely on initial system prompt stability. A periodic "Are you still solving X?" injection would help.

**2. No Circular Pattern Detection**

Depth limits prevent infinite recursion but not A→B→A→B patterns that stay under the limit.

**3. Weak Failure Strategy Reset**

When a tool fails repeatedly, there's error handling but no explicit "try a different approach" prompt. Agent keeps hammering the same wall.

**4. Tool Result Truncation Risk**

Results truncated to 8KB default. Critical error signals or diagnostic output could be lost, causing misdiagnosis.

**5. Task State Not Tracked**

`SessionStateManager` tracks processing/waiting/idle but not **what task** is being worked. No mechanism to confirm mid-chain that the agent is still solving the correct problem.

### Prompt Engineering In Progress

The `prompt_prd.md` draft shows good direction:
- Restructuring section order (data before instructions)
- Consolidating duplicated rules (~800 chars reclaimed)
- Adding explicit stopping conditions
- Reframing negatives as positives

**Recommendation**: Continue this work. The stopping conditions (Change 5) and role statement (Change 2) address harness weaknesses directly.

---

## Part 3: Architecture & Technical Debt

### Strengths

- Clean package boundaries and dependency injection via `ToolServices`
- Smart SQLite usage with WAL mode and connection pooling
- Graceful degradation (optional services don't crash gateway)
- Comprehensive test coverage (192 files, 72K+ lines)

### Weaknesses

**1. No Structured Logging**
All logging via `log.Printf()`. No levels, no request IDs, no correlation. Makes production debugging difficult.

**2. Large Files**
`gateway.go` is 1,940 lines. Should split into focused files.

**3. Hardcoded Timeouts**
HTTP server timeouts not configurable (30s read, 60s write, 120s idle).

**4. Missing Distributed Tracing**
No OpenTelemetry integration. Tool execution traces exist only in debug ring buffer.

**5. Incomplete Test Coverage**
- Heartbeat integration tests disabled
- No E2E gateway lifecycle tests
- No stress tests for WebSocket connections

---

## Recommendations: What to Work on Next

### Tier 1: Complete the SRE Story (Highest Impact)

1. **Datadog Tool Implementation** — Query metrics, search logs, check monitors
   - Enables: "Any Datadog alerts firing?" "Show me error rate for service X"
   - Beads: `conduit-1me`, `conduit-1u8`, `conduit-32h`

2. **PagerDuty Tool Implementation** — List/ack/resolve incidents, on-call lookup
   - Enables: "What incidents are open?" "Ack the P1 for me"
   - Beads: `conduit-1a3`, `conduit-5pv`

3. **Incident Correlation Engine** — The capstone
   - Once DD + PD tools exist, build the orchestration layer
   - Bead: `conduit-3qy`

### Tier 2: Harden the Agent Harness

4. **Add Mid-Chain Refocusing**
   - After every N tool calls (configurable, default 10), inject a reminder of the original goal
   - Prevents drift on complex multi-step operations

5. **Implement Prompt PRD Changes**
   - Stopping conditions (Change 5)
   - Role statement enrichment (Change 2)
   - Duplicate consolidation (Change 3)

6. **Circular Pattern Detection**
   - Track recent tool call sequence
   - Detect A→B→A→B patterns and break with strategy reset prompt

### Tier 3: Operational Excellence

7. **Structured Logging**
   - Replace `log.Printf()` with `slog`
   - Add request ID correlation
   - Define log levels (info/warn/error/debug)

8. **K8s Resource Metrics**
   - Implement the `top` action using metrics API
   - Enables resource utilization visibility

9. **Re-enable Integration Tests**
   - Fix and un-disable heartbeat tests
   - Add E2E gateway lifecycle tests

### Tier 4: Polish (Lower Priority)

10. **Configurable HTTP Timeouts**
11. **OpenTelemetry Integration**
12. **Gateway.go File Split**

---

## Conclusion

Conduit has strong bones. The K8s and SSH tools are production-grade. The agent harness prevents the most dangerous failure modes (fabrication, runaway loops). The architecture is clean.

**The gap is the incident lifecycle.** Completing PagerDuty and Datadog integration transforms Conduit from "a capable investigation assistant" to "an SRE co-pilot that can manage incidents end-to-end."

The agent harness refinements (mid-chain refocusing, stopping conditions) are important but secondary — they improve quality of operation, while the incident tools enable entirely new workflows.

**Suggested next move**: Start with PagerDuty incident management (`conduit-1a3`) since the client exists and the API is simpler than Datadog. This unblocks the correlation engine.

---

# Part 4: Wave Execution Plan

## Overview

Execute all 19 open tickets using cascading waves of parallel sub-agents with a fully autonomous orchestrator for integration.

**Execution Model:**
- Sub-agents work in isolated git worktrees
- Each agent owns one ticket end-to-end (design → implement → test)
- Orchestrator has full authority: architectural decisions, refactoring, follow-up ticket creation
- Waves proceed sequentially; agents within a wave run in parallel

**Excluded from this plan:** `conduit-23a` (MQTT Unified Inbox) — separate PRD pending.

---

## Wave 0: Dependency Unblock

**Orchestrator action:** The `conduit-10a` (Agent Harness Improvements) epic currently blocks its subtasks. This is backwards — subtasks should contribute to epic completion, not be blocked by it.

**Fix:** Remove blocking dependencies OR mark epic in_progress so subtasks become ready.

```bash
br dep remove conduit-3ci conduit-10a
br dep remove conduit-3rk3 conduit-10a
br dep remove conduit-lo2b conduit-10a
br dep remove conduit-1j1y conduit-10a
br dep remove conduit-1wet conduit-10a
br dep remove conduit-1meo conduit-10a
```

**Duration:** 1 minute

---

## Wave 1: Foundation Swarm

**8 parallel agents** — No code interdependencies. Each works in isolated worktree.

| Agent ID | Ticket | Description | Key Files |
|----------|--------|-------------|-----------|
| `pd-incidents` | conduit-1a3 | PagerDuty incident CRUD | `internal/tools/pagerduty/tool.go` (new) |
| `pd-oncall` | conduit-5pv | PagerDuty schedules/escalation | `internal/tools/pagerduty/oncall.go` (new) |
| `dd-metrics` | conduit-1u8 | Datadog metrics query | `internal/tools/datadog/metrics.go` (new) |
| `dd-logs` | conduit-1me | Datadog log search | `internal/tools/datadog/logs.go` (new) |
| `dd-monitors` | conduit-32h | Datadog monitor management | `internal/tools/datadog/monitors.go` (new) |
| `k8s-top` | conduit-1v50 | K8s resource metrics | `internal/tools/k8s/top.go` (new) |
| `tui-cd` | conduit-1h0 | TUI shell cd tracking | `internal/tui/shell.go` |
| `slog` | conduit-1l0r | Structured logging | `internal/logging/` (new package) |

**Agent Instructions Template:**
```
You are implementing ticket {TICKET_ID}: {TITLE}

Context:
- Working in isolated git worktree
- Follow existing patterns in internal/tools/{domain}/
- Use existing HTTP client from internal/tools/{domain}/client.go
- Register tool in internal/tools/registry.go
- Write tests in {file}_test.go
- Security tier classification required (read/modify/dangerous/blocked)

Deliverables:
1. Tool implementation with all actions from ticket description
2. Unit tests with >80% coverage
3. Integration test if external API involved (mock or skip with TODO)
4. Update CONFIG.md if new config options added

Do NOT modify files outside your scope without orchestrator approval.
```

**Estimated duration:** 2-4 hours per agent (parallel)

**Wave 1 Exit Criteria:**
- All 8 agents report completion
- `make test` passes in each worktree
- Orchestrator reviews and merges to integration branch

---

## Wave 2: Dependent Tasks

**3 parallel agents** — Depend on Wave 1 patterns/outputs.

| Agent ID | Ticket | Depends On | Key Files |
|----------|--------|------------|-----------|
| `dd-apm` | conduit-k9w | dd-metrics (pattern) | `internal/tools/datadog/apm.go` (new) |
| `tui-env` | conduit-1vo | tui-cd | `internal/tui/shell.go` |
| `tui-interactive` | conduit-398 | tui-cd | `internal/tui/shell.go` |

**Conflict Risk:** All 3 TUI agents touch `shell.go`. Orchestrator sequences TUI merges: `tui-cd` → `tui-env` → `tui-interactive`.

**Wave 2 Exit Criteria:**
- All 3 agents report completion
- TUI agents merged in dependency order
- `make test` passes on integration branch

---

## Wave 3: Agent Harness + Feature

**7 parallel agents** — Core harness improvements plus one independent feature.

| Agent ID | Ticket | Key Files | Conflict Zone |
|----------|--------|-----------|---------------|
| `prompt-prd` | conduit-1j1y | `prompt_builder.go`, `prompt_sections.go`, `constants.go` | Exclusive |
| `refocus` | conduit-3ci | `internal/tools/execution.go` | Shared |
| `circular` | conduit-3rk3 | `internal/tools/execution.go` | Shared |
| `failure-reset` | conduit-lo2b | `internal/tools/execution.go` | Shared |
| `truncation` | conduit-1wet | `internal/tools/execution.go` | Shared |
| `task-state` | conduit-1meo | `internal/agent/interface.go`, `internal/sessions/state.go` | Exclusive |
| `media-protocol` | conduit-11g | `internal/channels/telegram/` | Exclusive |

**HIGH RISK:** 4 agents modify `execution.go`. Orchestrator strategy:

1. Define interface contracts before agents start:
   - `refocus`: Add `RefocusCheck(depth int, originalGoal string)` hook
   - `circular`: Add `PatternTracker` with `RecordCall()` and `DetectCircular()` 
   - `failure-reset`: Add `FailureTracker` with `RecordFailure()` and `ShouldPivot()`
   - `truncation`: Modify `truncateResult()` function only

2. Agents implement in isolated sections
3. Orchestrator merges in order: truncation → refocus → circular → failure-reset

**Wave 3 Exit Criteria:**
- All 7 agents report completion
- Orchestrator resolves execution.go conflicts
- Agent harness integration test passes
- `make test` passes on integration branch

---

## Wave 4: Capstone Integration

**1 agent** — Depends on all PagerDuty and Datadog tools from Wave 1.

| Agent ID | Ticket | Integrates |
|----------|--------|------------|
| `sre-correlation` | conduit-3qy | PD incidents + DD metrics/logs + K8s + SSH |

**Agent Instructions:**
```
You are implementing the SRE Incident Correlation Engine.

Available tools (from Wave 1):
- PagerDuty: list_incidents, get_incident, acknowledge, resolve, snooze, add_note
- PagerDuty: get_oncall, list_schedules, get_escalation_policy
- Datadog: query_metrics, list_metrics
- Datadog: search_logs, get_log
- Datadog: list_monitors, get_monitor, mute_monitor
- K8s: All existing actions + top (new)
- SSH: All existing actions

Build an orchestration layer that:
1. Accepts "triage incident X" command
2. Pulls PD incident details
3. Extracts service/host context
4. Queries relevant DD metrics and logs
5. Suggests K8s/SSH investigation commands
6. Formats unified incident summary

Implementation location: internal/tools/sre/ (new package)
```

**Wave 4 Exit Criteria:**
- Correlation engine functional
- Demo workflow: PD incident → DD context → investigation suggestions
- `make test` passes

---

## Wave 5: Orchestrator Final Pass

**Orchestrator responsibilities:**

1. **Integration Testing**
   - Run full `make test` on merged codebase
   - Run integration tests for new tools (may require API mocks)
   - Verify no regressions in existing K8s/SSH tools

2. **Conflict Resolution**
   - Merge all worktree branches to main integration branch
   - Resolve any remaining conflicts
   - Ensure import consistency

3. **Code Review**
   - Invoke `superpowers:code-reviewer` agent on complete diff
   - Address any critical findings

4. **Follow-up Tickets**
   - Create beads for any issues discovered during integration
   - Document technical debt introduced

5. **Final PR**
   - Create single PR with all changes OR
   - Create sequenced PRs per wave (user preference)
   - Include comprehensive PR description with all tickets addressed

6. **Beads Cleanup**
   - Close all completed tickets
   - Update epic status
   - `br sync --flush-only && git add .beads/ && git commit`

---

## Orchestrator Agent Specification

```
You are the Orchestrator for the Conduit wave execution plan.

Authority:
- Full autonomy for architectural decisions
- May refactor shared code to resolve conflicts
- May create follow-up tickets for discovered issues
- May reject agent work that doesn't meet quality bar

Responsibilities:
1. Wave 0: Unblock dependencies
2. Between waves: Merge agent worktrees, resolve conflicts, run tests
3. Wave 3: Define execution.go interface contracts BEFORE dispatching agents
4. Wave 5: Final integration, review, PR creation

Communication:
- Report wave completion status after each wave
- Flag any blocking issues immediately
- Document all architectural decisions made

Quality Gates:
- No wave proceeds until previous wave's tests pass
- No merge without unit tests
- Integration tests required for external API tools
```

---

## Execution Timeline (Estimated)

| Phase | Duration | Parallelism |
|-------|----------|-------------|
| Wave 0 | 5 min | 1 (orchestrator) |
| Wave 1 | 3-4 hours | 8 agents |
| Wave 1 merge | 30 min | 1 (orchestrator) |
| Wave 2 | 2-3 hours | 3 agents |
| Wave 2 merge | 20 min | 1 (orchestrator) |
| Wave 3 | 3-4 hours | 7 agents |
| Wave 3 merge | 45 min | 1 (orchestrator) |
| Wave 4 | 2-3 hours | 1 agent |
| Wave 5 | 1-2 hours | 1 (orchestrator) |
| **Total** | **~12-16 hours** | Max 8 concurrent |

---

## Risk Mitigation

| Risk | Mitigation |
|------|------------|
| execution.go merge conflicts | Pre-defined interface contracts, ordered merging |
| External API rate limits | Mock-based integration tests |
| Agent scope creep | Explicit file ownership in instructions |
| Test failures cascade | Wave gates prevent forward progress |
| Orchestrator bottleneck | Parallelize review within waves |

---

## Verification

After Wave 5 completion:

1. **Functional verification:**
   - PagerDuty: List incidents, ack one, add note
   - Datadog: Query metrics, search logs, check monitors
   - K8s: Run `top pods`, verify resource data
   - SRE Correlation: "Triage incident X" end-to-end
   - Agent harness: 20+ tool chain without drift

2. **Regression verification:**
   - Existing K8s tests pass
   - Existing SSH tests pass
   - Existing session tests pass
   - Heartbeat loop functional

3. **Documentation:**
   - CONFIG.md updated for new tools
   - Tool descriptions in registry complete
   - CHANGELOG entry for release
