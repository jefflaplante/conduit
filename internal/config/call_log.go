package config

// CallLogConfig configures the persistent structured LLM call log
// (conduit-2lzv): one JSON line per provider call, metadata only — never
// prompt text, response text or tool arguments.
//
//	"ai": {"call_log": {"enabled": true, "path": "", "max_size_mb": 20, "max_files": 5}}
type CallLogConfig struct {
	// Enabled turns the log on or off. nil (unset) = on: the log is
	// metadata only and is the primary record for LLM-layer incident
	// diagnosis, so it should exist before the incident does.
	Enabled *bool `json:"enabled,omitempty"`
	// Path of the active log file. "" = {data_dir}/logs/llm-calls.jsonl.
	Path string `json:"path,omitempty" cfg:"env,path"`
	// MaxSizeMB rotates the active file once it would exceed this size.
	// 0 = default (20).
	MaxSizeMB int `json:"max_size_mb,omitempty"`
	// MaxFiles is the total number of files kept, the active one included
	// (llm-calls.jsonl, .1 ... .N-1). 0 = default (5).
	MaxFiles int `json:"max_files,omitempty"`
}

// CallLogEnabled reports whether the call log is on (default true).
func (c CallLogConfig) CallLogEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}
