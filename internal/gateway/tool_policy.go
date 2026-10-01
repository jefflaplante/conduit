package gateway

import (
	"log/slog"

	"conduit/internal/config"
	"conduit/internal/datadir"
	"conduit/internal/policy"
	"conduit/internal/tools"
)

// initToolPolicy installs the tool action policy on the registry
// (conduit-25lt.2). Phase 1 is shadow mode: decisions are recorded to
// <data_dir>/logs/policy-decisions.jsonl and nothing is blocked. tool_policy
// is read at startup only; the agent cannot change it (update_config
// refuses the block).
func initToolPolicy(cfg *config.Config, reg *tools.Registry, logger *slog.Logger) {
	pc := cfg.ToolPolicy.Resolve(cfg.AgentHeartbeat.AlertTargets)
	if pc.Mode == policy.ModeOff {
		logger.Info("tool policy off", "component", "policy")
		return
	}
	var rec policy.Recorder
	path := ""
	if dd, err := datadir.New(cfg.DataDir); err != nil {
		logger.Warn("tool policy: no data dir; decisions are not recorded", "component", "policy", "error", err)
	} else {
		path = policy.DecisionLogPath(dd.Root())
		if fr, err := policy.NewFileRecorder(path); err != nil {
			logger.Warn("tool policy: cannot open decision log; decisions are not recorded", "component", "policy", "error", err)
		} else {
			rec = fr
		}
	}
	reg.SetPolicyEngine(policy.New(pc, rec))
	if len(pc.OwnerTargets) == 0 {
		logger.Warn("tool policy: no owner targets (tool_policy.owner_targets or a telegram alert target); messages to the owner will read as message.dm",
			"component", "policy")
	}
	logger.Info("tool policy enabled", "component", "policy", "mode", pc.Mode,
		"classes", len(pc.Classes), "owner_targets", len(pc.OwnerTargets), "log", path)
}
