package mqtt

import "log"

// VerboseLogging controls whether debug-level MQTT messages appear in the journal.
// Set from gateway.go using the config value: mqtt.VerboseLogging = cfg.Debug.VerboseLogging
var VerboseLogging bool

// debugf logs only when VerboseLogging is true.
func debugf(format string, args ...interface{}) {
	if VerboseLogging {
		log.Printf(format, args...)
	}
}
