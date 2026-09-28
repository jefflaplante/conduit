package config

import (
	"fmt"
	"log"
	"sync"
)

// Deprecated config keys are still accepted so existing configs keep
// loading; each logs one warning per process (config may be loaded more
// than once, e.g. by CLI subcommands or a reload).

var deprecationOnce sync.Map // key -> *sync.Once

func warnDeprecatedOnce(key, msg string) {
	o, _ := deprecationOnce.LoadOrStore(key, &sync.Once{})
	o.(*sync.Once).Do(func() { log.Printf("[Config] %s", msg) })
}

// warnDeprecatedKeys logs a one-time warning for each deprecated key present
// in the loaded config. It never fails the load.
func (c *Config) warnDeprecatedKeys() {
	if c.AI.SmartRouting != nil {
		warnDeprecatedOnce("ai.smart_routing",
			"ai.smart_routing is deprecated and ignored: smart routing was removed (it was never wired, so "+
				"behaviour is unchanged). Remove the block; move any pricing_overrides to ai.pricing_overrides (conduit-2avx)")
	}
	if c.AgentHeartbeat.AlertQueuePath != "" {
		warnDeprecatedOnce("agent_heartbeat.alert_queue_path",
			"agent_heartbeat.alert_queue_path is deprecated: the gateway no longer processes that queue "+
				"(it belongs to alert-flush.sh and the HEARTBEAT.md prompt). Its directory is still used for "+
				"deferred.json; remove the key to use the default memory/alerts/ (conduit-31jg.59)")
	}
	for _, t := range c.AgentHeartbeat.AlertTargets {
		if removedAlertTargetTypes[t.Type] {
			warnDeprecatedOnce("agent_heartbeat.alert_targets.type="+t.Type,
				fmt.Sprintf("agent_heartbeat.alert_targets: type %q (target %q) is no longer supported and is ignored: "+
					"no %s deliverer exists (email/SMTP is tracked in conduit-115f). Remove the target or use type "+
					"\"telegram\" (conduit-40qj)", t.Type, t.Name, t.Type))
		}
	}
	if unroutedAlertTargets(c.AgentHeartbeat.AlertTargets) {
		warnDeprecatedOnce("agent_heartbeat.alert_targets",
			"agent_heartbeat.alert_targets: only the first target's telegram chat_id is used (as the heartbeat "+
				"job's delivery target); further targets, non-telegram types and severity filters are reserved "+
				"and do not route alerts (conduit-31jg.73)")
	}
}

// unroutedAlertTargets reports whether alert_targets configures anything
// beyond what the gateway honours today: alert_targets[0] of type telegram
// with config.chat_id.
func unroutedAlertTargets(targets []AlertTarget) bool {
	if len(targets) == 0 {
		return false
	}
	if len(targets) > 1 || targets[0].Type != "telegram" || len(targets[0].Severity) > 0 {
		return true
	}
	return false
}
