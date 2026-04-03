# Conduit Critical Code Review — 2026-04-03

Three-angle review: complexity, efficiency, and SRE feature completeness.

---

## 1. Complexity Review

**Verdict: Systematic over-engineering trending toward "framework-as-a-feature."**

### God Objects / God Files

- **gateway.go** (1,980 LOC) — Kitchen-sink orchestrator owning 15+ subsystems: WebSocket, channels, AI routing, scheduling, health monitoring, auth, rate limiting, skills, vector API, MQTT, direct client. Changes require understanding the entire stack.
- **router.go** (862 LOC, 33+ methods) — Embeds 5 routing subsystems: ModelSelector, ComplexityAnalyzer, ContextEngine, PricingResolver, smartRoutingCfg. Impossible to unit-test in isolation.
- **context.go** (1,074 LOC) — Mega-tool called every conversation turn: workspace loading, memory summarization, security policy, access control, token budgeting, context window management.

### Over-Abstraction

- **Single-implementation interfaces** in `types.go`: ChannelSender, GatewayService, SearchService, MQTTService, VectorService — each has exactly one implementation. Premature abstraction adding ~80 LOC of interface boilerplate + adapter code.
- **Tool execution adapter tower** — 5 different wrappers for the same concept:
  1. `Registry.ExecuteTool()`
  2. `execution_adapter.go: ExecutionEngineAdapter`
  3. `planning_execution.go: RegistryToolExecutor`
  4. `core/chain.go: validationToolExecutor`
  5. `sre/tool_executor.go: registryToolExecutor`
  ~150 LOC of adapters that could be eliminated.
- **Skill-tool bridge** in registry.go — exists solely to work around circular imports between skills and tools packages.

### Smart Routing Over-Architecture

6 files, 2,400+ LOC, 18+ types for routing logic:
- `smart_routing.go` (639 LOC)
- `pattern_cluster.go` (805 LOC)
- `cost_optimizer.go` (754 LOC)
- `usage_prediction.go` (633 LOC)
- `router_intelligence.go` (733 LOC)
- Config tracks daily budgets and pricing overrides not used in current MVP.

### Config Complexity

- 46 nested config struct types in `config.go` (988 LOC)
- 63 lines of manual `os.ExpandEnv()` calls — maintenance nightmare
- Adding a new feature requires touching 5+ places

### Package Proliferation

18 tool sub-packages, many too small to justify:
- `args/` (222 LOC) — 2 files, 3 functions
- `errors/` (756 LOC) — error definitions; overkill for a package
- `debuglog/` (328 LOC) — could merge with monitoring
- `communication/` (2,816 LOC) — could merge into core
- `schema/` (724 LOC) — used by ~3 tools; inline would suffice
- `validation/` (2,062 LOC) — 68% test code; over-engineered for sparse use

### Estimated Reducible Complexity

| Area | Before | After | Reduction |
|------|--------|-------|-----------|
| Gateway struct | 1,980 LOC | 500 LOC | 74% |
| Tool execution adapters | 150 LOC | 0 LOC | 100% |
| Single-impl interfaces | 100 LOC | 0 LOC | 100% |
| AI Router | 862 LOC | 400 LOC | 54% |
| Config struct | 988 LOC | 650 LOC | 34% |
| Schema + Validation | 2,786 LOC | 400 LOC | 86% |
| Small tool packages | 3,500 LOC | 2,500 LOC | 29% |
| **Total** | **~10,000 LOC** | **~4,500 LOC** | **~55%** |

---

## 2. Efficiency Review

**Verdict: Works fine at low scale; several time bombs at 100+ concurrent sessions.**

### Critical Bottlenecks

1. **SQLite write lock contention** — `database/retry.go:21-37`: `RetryOnBusy(2)` with 150ms max backoff is insufficient. WAL allows only 1 writer; retry storms cascade under load. Fix: increase to 5-7 retries, 500ms-1s cap.

2. **Unbounded goroutine spawn** — `gateway.go:1339, 1427, 1472`: each message creates a goroutine with no backpressure. At 1000 connections with rapid traffic: 10k+ goroutines. Fix: worker pool (50-100 workers).

3. **Unbounded message history** — `sessions/store.go:371-435`: `GetMessages()` loads all rows then filters in-memory. No cleanup/archival. Fix: hard limit on retrieval, implement archival.

### High Priority

4. **Insufficient shutdown drainage** — `gateway.go:1040-1051`: 10s timeout for 1000 WebSocket connections. SSH server not explicitly shut down. Fix: increase to 30s, add connection draining.

5. **Session state memory leak** — `sessions/state.go`: in-memory map with no bounds or TTL eviction.

### Medium Priority

6. **JSON unmarshaling in hot paths** — `sessions/store.go:403-427`: every message row unmarshals metadata JSON.

7. **O(n²) bubble sort** — `mqtt/event_buffer.go:245-251`: called on every `Recent()` query. Fix: `sort.Slice`.

8. **Unbuffered channel leak** — `gateway.go:1471`: `typingDone := make(chan struct{})` risks goroutine leak. Fix: buffer to 1.

9. **No slice capacity hints** — `ai/messages.go:155, 162`: append loops without pre-allocation.

10. **Missing WAL checkpoint tuning** — `database/migrations.go:289-296`: no `wal_autocheckpoint`, WAL grows unbounded.

11. **Full workspace re-scan every 5 min** — `fts/indexer.go:36-83`: walks entire workspace + SHA256 even if nothing changed. Fix: use `fsnotify`.

12. **Heavy K8s dependencies** — `go.mod`: ~100+ transitive deps from k8s.io packages, even when unused.

### Scale Limits

| Metric | Current Limit | Problem |
|--------|---------------|---------|
| Concurrent WebSocket | ~1000 | SQLite write lock saturates first |
| Message history | Unbounded | Memory leak over weeks |
| Goroutines | Unbounded | 10k+ under sustained load |
| Session state tracker | Unbounded | No eviction |
| WAL file size | Unbounded | Checkpoint latency spikes |

---

## 3. SRE Feature Completeness

**Verdict: 55/100 — strong foundation, critical integration gaps.**

### Scorecard

| Category | Score | Notes |
|----------|-------|-------|
| Core Tools (31 total) | 8/10 | Good breadth; missing cloud APIs |
| Internal Observability | 8/10 | Excellent self-monitoring |
| Auth (tokens) | 8/10 | Solid token system |
| PagerDuty Incidents | 7/10 | Full lifecycle; missing auto-notification |
| K8s + SSH | 6/10 | Strong; no manifest creation, no CRDs |
| Runbook Automation | 5/10 | Skills + Chain work; no approval gates |
| Alerting Framework | 4/10 | Framework solid; channels not wired |
| External Observability | 3/10 | Datadog-only; no Prometheus/Grafana/CloudWatch/ELK |
| Deployment | 3/10 | Systemd only; no containers/Helm/HA |
| Multi-Tenancy/RBAC | 2/10 | All users equal; no granular permissions |
| HA/Scaling | 1/10 | Single instance only |

### Tier 1 — Showstoppers for Team Use

1. **Slack integration** — SRE teams live in Slack; no Slack = no incident notifications. Already have MessageTool; need Slack webhook + channel mapping.
2. **Cloud provider APIs** (AWS/GCP/Azure) — Most production teams run on cloud. Cannot query EC2, RDS, Lambda, GCP Compute, etc.
3. **RBAC system** — Cannot safely share gateway between teams or roles. All authenticated users have equal access to all tools.

### Tier 2 — Important (Workarounds Exist)

4. **Jira/Linear ticket creation** — Incidents need artifacts for follow-up.
5. **Prometheus integration** — Many teams use Prometheus; must proxy through Datadog today.
6. **Grafana alert webhooks** — No push mechanism for alerts from Grafana.
7. **Email notifications** — Alerting framework supports it but no SMTP implementation.

### Tier 3 — Nice-to-Have

8. **Runbook approval gates** — All execution auto-approved; dangerous for infra changes.
9. **Container/K8s deployment** — Dockerfile + Helm chart for cloud-native teams.
10. **Audit trail & alert history** — Compliance and post-mortems need historical records.

### Suitability by Scenario

| Use Case | Ready? |
|----------|--------|
| Single operator, on-prem, Datadog | 80% |
| Home lab / hobby SRE | 90% |
| Team of 5, AWS + Datadog | 40% |
| On-prem K8s + Prometheus + Slack | 50% |
| Enterprise, multi-cloud | 20% |
