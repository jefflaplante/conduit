package config

// MCPConfig controls authentication for Conduit's MCP server (the endpoint
// exposed to Claude Code on 127.0.0.1:<claude_code.mcp_port>). conduit-31jg.8.
type MCPConfig struct {
	// RequireAuth selects the bearer-token policy:
	//   unset (default) — warn-only transition mode: requests without a token
	//                     are served but logged; a wrong token is rejected.
	//   true            — enforce: requests without a valid token get 401.
	//   false           — auth disabled (not recommended; logged at startup).
	// The unset default will become enforce in a future release.
	RequireAuth *bool `json:"require_auth,omitempty"`

	// TokenFile is the bearer-token file (0600). Default:
	// {data_dir}/auth/mcp_token, generated on first start.
	TokenFile string `json:"token_file,omitempty" cfg:"env,path"`
}
