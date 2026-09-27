package gateway

import (
	"log/slog"
	"os"

	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/datadir"
	"conduit/internal/mcp"
)

// MCPTokenFilename is the MCP bearer-token file inside {data_dir}/auth/.
const MCPTokenFilename = "mcp_token"

// resolveMCPTokenPath returns mcp.token_file or {data_dir}/auth/mcp_token.
func resolveMCPTokenPath(cfg *config.Config) (string, error) {
	if cfg.MCP.TokenFile != "" {
		return cfg.MCP.TokenFile, nil
	}
	dd, err := datadir.New(cfg.DataDir)
	if err != nil {
		return "", err
	}
	return dd.AuthFilePath(MCPTokenFilename), nil
}

// resolveMCPAuth resolves the MCP auth mode and bearer token (conduit-31jg.8).
// The token file is created (0600) on first start. The token is exported as
// CONDUIT_MCP_TOKEN so the claude-code provider's `claude -p` subprocess —
// whose .mcp.json references ${CONDUIT_MCP_TOKEN} — inherits it. ok=false
// means MCP must not start (enforce mode without a usable token: fail closed).
func resolveMCPAuth(cfg *config.Config, logger *slog.Logger) (mode mcp.AuthMode, token string, ok bool) {
	mode = mcp.ResolveAuthMode(cfg.MCP.RequireAuth)
	if mode == mcp.AuthDisabled {
		logger.Warn("MCP auth DISABLED (mcp.require_auth=false): any local process can call conduit tools over MCP")
		return mode, "", true
	}

	path, err := resolveMCPTokenPath(cfg)
	if err == nil {
		var generated bool
		token, generated, err = auth.LoadOrCreateSecretFile(path)
		if err == nil && generated {
			logger.Info("generated MCP bearer token", "token_file", path)
		}
	}
	if err != nil {
		if mode == mcp.AuthEnforce {
			logger.Error("MCP auth: token file unusable; MCP server not started (fail closed)", "token_file", path, "error", err)
			return mode, "", false
		}
		logger.Error("MCP auth: token file unusable; warn mode will reject every presented token", "token_file", path, "error", err)
		return mode, "", true
	}

	if err := os.Setenv(mcp.TokenEnvVar, token); err != nil {
		logger.Warn("MCP auth: could not export token env var", "var", mcp.TokenEnvVar, "error", err)
	}

	if mode == mcp.AuthWarn {
		logger.Warn("MCP auth in WARN-ONLY mode (mcp.require_auth unset): unauthenticated MCP requests are still served. "+
			"Point clients at the token, then set mcp.require_auth=true",
			"token_file", path, "client_header", "Authorization: Bearer ${"+mcp.TokenEnvVar+"}")
	} else {
		logger.Info("MCP auth enforced", "token_file", path)
	}
	return mode, token, true
}
