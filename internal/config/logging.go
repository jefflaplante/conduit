package config

// LoggingConfig holds structured logging configuration
type LoggingConfig struct {
	// Level is the minimum log level: debug, info, warn, error (default: info)
	Level string `json:"level,omitempty"`
	// Format is the output format: text, json (default: text)
	Format string `json:"format,omitempty"`
}

// DefaultLoggingConfig returns sensible defaults for logging
func DefaultLoggingConfig() LoggingConfig {
	return LoggingConfig{
		Level:  "info",
		Format: "text",
	}
}

// GetLevel returns the configured level or default
func (c *LoggingConfig) GetLevel() string {
	if c.Level == "" {
		return "info"
	}
	return c.Level
}

// GetFormat returns the configured format or default
func (c *LoggingConfig) GetFormat() string {
	if c.Format == "" {
		return "text"
	}
	return c.Format
}

// DebugConfig contains debugging and logging settings
type DebugConfig struct {
	LogMessageContent bool `json:"log_message_content,omitempty"` // Enable logging of message content (privacy risk!)
	VerboseLogging    bool `json:"verbose_logging,omitempty"`     // Enable verbose debug logging
}
