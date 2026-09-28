package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/monitoring"
	toolstypes "conduit/internal/tools/types"
)

// Live config reload for the Gateway tool's update_config (conduit-rmho).
//
// Flow: the patch (merge-patch style, see config.FlattenPatch) is applied to
// the raw config.json document — never to the expanded Config struct, which
// holds secrets — and the edited document goes through config.Parse, i.e.
// the exact startup pipeline including Validate. Nothing changes unless it
// passes. Each changed key is then classified:
//
//   - live: applied to the running gateway at once —
//     ai.providers.<name>.* for an existing, non-claude-code provider
//     (instance rebuilt, throttle limits and pricing rebuilt),
//     ai.pricing_overrides (and the deprecated
//     ai.smart_routing.pricing_overrides), ai.call_log.* and
//     ai.subagent_default_model;
//   - requires_restart: everything else. It is saved to the file and takes
//     effect at the next restart; the result says so instead of pretending
//     it applied.
//
// Apply is two-phase: everything that can fail (validation, building the
// new provider instances, writing the file) happens before anything is
// swapped in, so a failed update leaves both the file and the running
// gateway unchanged. Updates are serialized by configReloader.mu.
//
// Approval is NOT done here; the Gateway tool gates every model-originated
// update_config through the human approval primitive (see
// internal/tools/core/gateway_config.go).

// configReloader holds the live-reload state of a Gateway.
type configReloader struct {
	mu   sync.Mutex // serializes plan/apply and guards path
	path string     // config file the gateway was loaded from; "" = reload unavailable
	// live is the effective config after live updates; nil = g.config.
	live atomic.Pointer[config.Config]
	// metrics counts plans and applies by outcome (conduit-2qes).
	metrics monitoring.ConfigUpdateMetrics
	// beforePersist, if set, runs just before the file is replaced (tests).
	beforePersist func()
}

// configRejectedError marks an update rejected because the patch itself is
// invalid (malformed, literal secret, fails validation), as opposed to an
// I/O or build failure. It only classifies; the message is unchanged.
type configRejectedError struct{ err error }

func (e *configRejectedError) Error() string { return e.err.Error() }
func (e *configRejectedError) Unwrap() error { return e.err }

func rejectInvalid(err error) error { return &configRejectedError{err: err} }

// configUpdateOutcome classifies a failed plan or apply for the metrics.
func configUpdateOutcome(err error) monitoring.ConfigUpdateOutcome {
	var rej *configRejectedError
	switch {
	case errors.As(err, &rej):
		return monitoring.ConfigUpdateRejectedInvalid
	case errors.Is(err, config.ErrConfigChangedOnDisk):
		return monitoring.ConfigUpdateRejectedConflict
	default:
		return monitoring.ConfigUpdateFailed
	}
}

// appliedOutcome classifies a successful apply for the metrics.
func appliedOutcome(res *toolstypes.ConfigUpdateResult) monitoring.ConfigUpdateOutcome {
	switch {
	case len(res.Changes) == 0:
		return monitoring.ConfigUpdateUnchanged
	case len(res.RestartKeys()) > 0:
		return monitoring.ConfigUpdateAppliedRestartRequired
	default:
		return monitoring.ConfigUpdateAppliedLive
	}
}

// ConfigUpdateMetrics returns the update_config counters (conduit-2qes).
func (g *Gateway) ConfigUpdateMetrics() monitoring.ConfigUpdateSnapshot {
	return g.reload.metrics.Snapshot()
}

// recordConfigApproval counts a denied or expired update_config approval.
// Approved ones are counted by the apply they trigger.
func (g *Gateway) recordConfigApproval(r approval.Resolution) {
	if r.Action.Kind != toolstypes.ConfigUpdateApprovalKind {
		return
	}
	switch r.Decision {
	case approval.DecisionDenied:
		g.reload.metrics.Record(monitoring.ConfigUpdateApprovalDenied)
	case approval.DecisionExpired:
		g.reload.metrics.Record(monitoring.ConfigUpdateApprovalExpired)
	}
}

// errConfigReloadUnavailable is returned when the gateway does not know its
// config file (tests, embedded use).
var errConfigReloadUnavailable = errors.New("config update unavailable: the gateway was not started from a config file")

// SetConfigPath records the config file the gateway was loaded from; it
// enables update_config (conduit-rmho).
func (g *Gateway) SetConfigPath(path string) {
	g.reload.mu.Lock()
	defer g.reload.mu.Unlock()
	g.reload.path = path
}

// currentConfig returns the effective configuration: the startup config
// with every live update applied. Callers must treat it as read-only; an
// update swaps in a new *Config instead of mutating the current one.
func (g *Gateway) currentConfig() *config.Config {
	if c := g.reload.live.Load(); c != nil {
		return c
	}
	return g.config
}

// liveKind records which live subsystems a plan touches.
type liveKind struct {
	providers     map[string]bool
	pricing       bool
	callLog       bool
	subagentModel bool
}

func (k liveKind) any() bool {
	return len(k.providers) > 0 || k.pricing || k.callLog || k.subagentModel
}

// configPlan is a validated, not yet applied update.
type configPlan struct {
	result    *toolstypes.ConfigUpdateResult
	path      string
	oldData   []byte
	newData   []byte
	candidate *config.Config
	live      liveKind
}

// PlanConfigUpdate validates patch and classifies its changes without
// changing anything (types.ConfigUpdater).
func (g *Gateway) PlanConfigUpdate(ctx context.Context, patch map[string]interface{}) (*toolstypes.ConfigUpdateResult, error) {
	g.reload.mu.Lock()
	defer g.reload.mu.Unlock()
	plan, err := g.planConfigUpdate(patch)
	if err != nil {
		g.reload.metrics.Record(configUpdateOutcome(err))
		return nil, err
	}
	if len(plan.result.Changes) == 0 {
		g.reload.metrics.Record(monitoring.ConfigUpdateUnchanged)
	} else {
		g.reload.metrics.Record(monitoring.ConfigUpdatePlanned)
	}
	o, _ := approval.OriginFrom(ctx)
	g.log().Info("config update planned", "component", "config_reload",
		"live_keys", plan.result.LiveKeys(), "restart_keys", plan.result.RestartKeys(),
		"security_keys", plan.result.SecurityKeys(), "session", o.SessionKey, "source", o.Source)
	return plan.result, nil
}

// UpdateConfiguration applies patch (types.GatewayService). It performs no
// approval; the Gateway tool uses PlanConfigUpdate + human approval +
// ApplyConfigUpdate instead.
func (g *Gateway) UpdateConfiguration(ctx context.Context, patch map[string]interface{}) error {
	_, err := g.ApplyConfigUpdate(ctx, patch)
	return err
}

// ApplyConfigUpdate validates patch, saves it to the config file and applies
// its live part (types.ConfigUpdater). On error nothing changed. Every call
// counts exactly one outcome in the config update metrics.
func (g *Gateway) ApplyConfigUpdate(ctx context.Context, patch map[string]interface{}) (*toolstypes.ConfigUpdateResult, error) {
	g.reload.mu.Lock()
	defer g.reload.mu.Unlock()

	res, rebuilt, err := g.applyConfigUpdate(ctx, patch)
	if err != nil {
		g.reload.metrics.Record(configUpdateOutcome(err))
		return nil, err
	}
	if o := appliedOutcome(res); o == monitoring.ConfigUpdateUnchanged {
		g.reload.metrics.Record(o)
	} else {
		g.reload.metrics.RecordApplied(o, rebuilt, time.Now())
	}
	return res, nil
}

// applyConfigUpdate does the work of ApplyConfigUpdate and also returns the
// number of provider instances rebuilt. Caller holds g.reload.mu.
func (g *Gateway) applyConfigUpdate(ctx context.Context, patch map[string]interface{}) (*toolstypes.ConfigUpdateResult, int, error) {
	plan, err := g.planConfigUpdate(patch)
	if err != nil {
		return nil, 0, err
	}
	res := plan.result
	if len(res.Changes) == 0 {
		return res, 0, nil
	}

	// Phase 1: build everything that can fail.
	running := g.currentConfig()
	next := *running
	next.AI.Providers = append([]config.ProviderConfig(nil), running.AI.Providers...)
	var prep *ai.ProviderReload
	if len(plan.live.providers) > 0 {
		if g.ai == nil {
			return nil, 0, errors.New("config update: no AI router to reload providers on")
		}
		for i, p := range next.AI.Providers {
			if plan.live.providers[p.Name] {
				next.AI.Providers[i] = *findProvider(plan.candidate.AI.Providers, p.Name)
			}
		}
		if prep, err = g.ai.PrepareProviderReload(next.AI); err != nil {
			return nil, 0, fmt.Errorf("config update: %w", err)
		}
	}
	if plan.live.pricing {
		next.AI.PricingOverrides = plan.candidate.AI.PricingOverrides
		next.AI.SmartRouting = plan.candidate.AI.SmartRouting
	}
	var newCallLog *ai.CallLog
	if plan.live.callLog {
		next.AI.CallLog = plan.candidate.AI.CallLog
		if newCallLog, err = ai.OpenCallLog(next.AI.CallLog, running.DataDir); err != nil {
			return nil, 0, fmt.Errorf("config update: %w", err)
		}
	}
	if plan.live.subagentModel {
		next.AI.SubagentDefaultModel = plan.candidate.AI.SubagentDefaultModel
	}

	// Phase 2: persist. The file must still hold what the plan was built on.
	if g.reload.beforePersist != nil {
		g.reload.beforePersist()
	}
	backup, err := config.ReplaceFile(plan.path, plan.newData, plan.oldData)
	if err != nil {
		if newCallLog != nil {
			_ = newCallLog.Close(ctx)
		}
		return nil, 0, fmt.Errorf("config update: save %s: %w", plan.path, err)
	}

	// Phase 3: commit (cannot fail).
	if prep != nil {
		prep.Commit()
	}
	if g.ai != nil && (plan.live.pricing || len(plan.live.providers) > 0) {
		pr := ai.NewPricingResolverFromConfig(next.AI)
		g.ai.SetPricingResolver(pr)
		ai.SetDefaultPricingResolver(pr)
	}
	if plan.live.callLog && g.ai != nil {
		old := g.ai.CallLog()
		g.ai.SetCallLog(newCallLog)
		if old != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := old.Close(closeCtx); err != nil {
				g.log().Warn("config update: closing previous call log", "error", err)
			}
			cancel()
		}
	}
	if plan.live.any() {
		g.reload.live.Store(&next)
	}

	res.Applied = true
	res.ConfigPath = plan.path
	res.BackupPath = backup
	// Audit: key paths and modes only, never values.
	g.log().Info("config update applied", "component", "config_reload",
		"live_keys", res.LiveKeys(), "restart_keys", res.RestartKeys(),
		"security_keys", res.SecurityKeys(), "providers_rebuilt", prep.Changed(),
		"config_path", plan.path, "backup_path", backup)
	return res, len(prep.Changed()), nil
}

func (g *Gateway) log() *slog.Logger {
	if g.logger != nil {
		return g.logger
	}
	return slog.Default()
}

// planConfigUpdate builds a validated plan. Caller holds g.reload.mu.
func (g *Gateway) planConfigUpdate(patch map[string]interface{}) (*configPlan, error) {
	path := g.reload.path
	if path == "" {
		return nil, errConfigReloadUnavailable
	}
	ops, err := config.FlattenPatch(patch)
	if err != nil {
		return nil, rejectInvalid(err)
	}
	for _, op := range ops {
		if err := config.CheckPatchSecrets(op); err != nil {
			return nil, rejectInvalid(err)
		}
	}
	oldData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	doc, err := config.ParseDocument(oldData)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	type change struct {
		path []string
		op   config.PatchOp
	}
	var changes []change
	res := &toolstypes.ConfigUpdateResult{Changes: []toolstypes.ConfigChange{}}
	for _, op := range ops {
		canonical, changed, err := doc.Set(op.Path, op.Value, op.Delete)
		if err != nil {
			return nil, rejectInvalid(err)
		}
		if !changed {
			res.Unchanged = append(res.Unchanged, config.JoinPath(canonical))
			continue
		}
		changes = append(changes, change{path: canonical, op: op})
	}
	plan := &configPlan{result: res, path: path, oldData: oldData}
	if len(changes) == 0 {
		return plan, nil
	}

	plan.newData = doc.Bytes()
	candidate, err := config.Parse(plan.newData)
	if err != nil {
		return nil, rejectInvalid(fmt.Errorf("rejected, nothing changed: %w", err))
	}
	plan.candidate = candidate

	running := g.currentConfig()
	res.Readback = map[string]interface{}{}
	for _, c := range changes {
		mode := toolstypes.ConfigChangeRestart
		if classifyLive(c.path, running, candidate, &plan.live) {
			mode = toolstypes.ConfigChangeLive
		}
		key := config.JoinPath(c.path)
		shown := "(removed)"
		if !c.op.Delete {
			shown = displayValue(c.path, c.op.Value)
		}
		res.Changes = append(res.Changes, toolstypes.ConfigChange{
			Path:              key,
			Value:             shown,
			Mode:              mode,
			SecuritySensitive: securitySensitive(c.path),
		})
		if v, _, found, err := doc.Lookup(c.path); err == nil && found {
			res.Readback[key] = redactedAt(c.path, v)
		}
	}
	return plan, nil
}

// classifyLive reports whether the change at path can be applied to the
// running gateway, recording which subsystem it touches in k.
func classifyLive(path []string, running, candidate *config.Config, k *liveKind) bool {
	if len(path) < 2 || path[0] != "ai" {
		return false
	}
	switch path[1] {
	case "providers":
		if len(path) < 4 {
			return false // the list or a whole entry: add/remove/replace
		}
		switch path[3] {
		case "name", "type", "claude_code":
			return false
		}
		rp := findProvider(running.AI.Providers, path[2])
		cp := findProvider(candidate.AI.Providers, path[2])
		if rp == nil || cp == nil || rp.Type != cp.Type || rp.Type == "claude-code" {
			return false
		}
		if len(running.AI.Providers) != len(candidate.AI.Providers) {
			return false
		}
		if k.providers == nil {
			k.providers = map[string]bool{}
		}
		k.providers[path[2]] = true
		return true
	case "pricing_overrides":
		k.pricing = true
		return true
	case "smart_routing":
		if len(path) >= 3 && path[2] == "pricing_overrides" {
			k.pricing = true
			return true
		}
	case "call_log":
		k.callLog = true
		return true
	case "subagent_default_model":
		if len(path) == 2 {
			k.subagentModel = true
			return true
		}
	}
	return false
}

func findProvider(ps []config.ProviderConfig, name string) *config.ProviderConfig {
	for i := range ps {
		if ps[i].Name == name {
			return &ps[i]
		}
	}
	return nil
}

// securityTopLevel are top-level sections whose every key is security-
// sensitive (sandbox, tool policy, auth, remote access, ingress).
var securityTopLevel = map[string]bool{
	"tools": true, "auth": true, "mcp": true, "ssh": true, "remote_ssh": true,
	"kubernetes": true, "allowed_origins": true, "websocket": true, "rateLimiting": true,
	"channels": true, "secrets_file": true, "skills": true, "tui": true,
	"database": true, "data_dir": true,
}

// securitySensitive reports whether a key affects sandboxing, credentials,
// provider endpoints, authentication, approvals or remote access. Every
// update is approval-gated anyway; this marks the keys in the approval
// prompt and the audit log.
func securitySensitive(path []string) bool {
	if securityTopLevel[path[0]] {
		return true
	}
	for _, s := range path {
		ls := strings.ToLower(s)
		if config.IsSecretKey(ls) || strings.Contains(ls, "approval") || strings.Contains(ls, "sandbox") ||
			strings.Contains(ls, "denylist") || strings.Contains(ls, "require_auth") || ls == "auth" ||
			ls == "base_url" || strings.Contains(ls, "allowed_") {
			return true
		}
	}
	return false
}

// redactedAt redacts v as the value of the last key of path.
func redactedAt(path []string, v interface{}) interface{} {
	key := path[len(path)-1]
	r, err := config.Redacted(map[string]interface{}{key: v})
	if err != nil {
		return config.RedactedValue
	}
	if m, ok := r.(map[string]interface{}); ok {
		return m[key]
	}
	return config.RedactedValue
}

// maxShownValue bounds a rendered value in results and approval prompts.
const maxShownValue = 300

// displayValue renders a new value for the plan (redacted, bounded).
func displayValue(path []string, v interface{}) string {
	b, err := json.Marshal(redactedAt(path, v))
	if err != nil {
		return config.RedactedValue
	}
	s := string(b)
	if r := []rune(s); len(r) > maxShownValue {
		s = string(r[:maxShownValue]) + "..."
	}
	return s
}
