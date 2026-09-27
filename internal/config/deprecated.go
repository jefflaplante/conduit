package config

import (
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
	if c.AgentHeartbeat.AlertQueuePath != "" {
		warnDeprecatedOnce("agent_heartbeat.alert_queue_path",
			"agent_heartbeat.alert_queue_path is deprecated: the gateway no longer processes that queue "+
				"(it belongs to alert-flush.sh and the HEARTBEAT.md prompt). Its directory is still used for "+
				"deferred.json; remove the key to use the default memory/alerts/ (conduit-31jg.59)")
	}
}
