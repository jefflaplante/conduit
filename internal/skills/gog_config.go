package skills

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// GogConfig configures the built-in gog/email command builder
// (skills.gog in config.json). Every field is optional; unset fields fall
// back to the legacy defaults below so existing deployments keep their exact
// behavior (conduit-31jg.40). Approval gating of owner-account sends
// (conduit-31jg.43) keys off OwnerAliases/AgentAliases, so changing them
// changes which identities require human approval.
type GogConfig struct {
	// Binary is the gog executable. Default: "gog" resolved on PATH at
	// startup (absolute path pinned), else bare "gog".
	Binary string `json:"binary,omitempty"`

	// OwnerAccountEnv / AgentAccountEnv name the environment variables that
	// hold the owner's and the agent's gog account. Only these expansions
	// ever follow --account; model-supplied strings never do.
	// Defaults: GOG_ACCOUNT / JULES_ACCOUNT.
	OwnerAccountEnv string `json:"owner_account_env,omitempty"`
	AgentAccountEnv string `json:"agent_account_env,omitempty"`

	// OwnerAliases / AgentAliases are the account/from/inbox values the model
	// may use to select each identity. Anything else is rejected. Sends
	// default to the agent identity; an owner alias requires approval.
	OwnerAliases []string `json:"owner_aliases,omitempty"`
	AgentAliases []string `json:"agent_aliases,omitempty"`

	// EnvFiles are candidate shell env files (KEY=VALUE / export lines)
	// sourced before each gog command; the first that exists wins. "~/" is
	// expanded; relative paths resolve against the workspace directory.
	EnvFiles []string `json:"env_files,omitempty"`

	// CleanupScript is run by the "cleanup" action. Relative paths resolve
	// against the workspace directory.
	// Default: scripts/hygiene-junk-sweep.sh.
	CleanupScript string `json:"cleanup_script,omitempty"`
}

// Legacy defaults: the values that were hardcoded in executor.go before
// conduit-31jg.40. They apply only when skills.gog leaves a field unset so
// the original deployment's approval semantics are unchanged; new
// deployments should set skills.gog explicitly.
var (
	legacyGogOwnerAliases = []string{"jeff", "owner@example.com", "owner-alt@example.com"}
	legacyGogAgentAliases = []string{"jules", "agent@example.com"}
	legacyGogEnvFiles     = []string{"~/ocgo/.ocgo-secrets.env", "~/.conduit-secrets.env"}
)

const (
	defaultGogOwnerAccountEnv = "GOG_ACCOUNT"
	defaultGogAgentAccountEnv = "JULES_ACCOUNT"
	defaultGogCleanupScript   = "scripts/hygiene-junk-sweep.sh"
)

var envVarNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// gogSettings is GogConfig with defaults applied and paths resolved.
type gogSettings struct {
	binary        string
	ownerAccount  string // shell expansion, e.g. "$GOG_ACCOUNT" (double-quoted)
	agentAccount  string
	ownerAliases  map[string]bool
	agentAliases  map[string]bool
	aliasList     []string // for error messages, agent first then owner
	envFiles      []string
	cleanupScript string // "" when unresolvable (no workspace for a relative path)
}

// resolveGogSettings applies defaults to cfg (nil = all defaults).
func resolveGogSettings(cfg *GogConfig, workspaceDir string) (*gogSettings, error) {
	var c GogConfig
	if cfg != nil {
		c = *cfg
	}

	binary := c.Binary
	if binary == "" {
		binary = "gog"
		if p, err := exec.LookPath("gog"); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				binary = abs
			}
		}
	}

	ownerEnv := firstNonEmpty(c.OwnerAccountEnv, defaultGogOwnerAccountEnv)
	agentEnv := firstNonEmpty(c.AgentAccountEnv, defaultGogAgentAccountEnv)
	for _, name := range []string{ownerEnv, agentEnv} {
		if !envVarNameRE.MatchString(name) {
			return nil, fmt.Errorf("skills.gog: invalid environment variable name %q", name)
		}
	}

	ownerAliases := c.OwnerAliases
	if len(ownerAliases) == 0 {
		ownerAliases = legacyGogOwnerAliases
	}
	agentAliases := c.AgentAliases
	if len(agentAliases) == 0 {
		agentAliases = legacyGogAgentAliases
	}
	s := &gogSettings{
		binary:       binary,
		ownerAccount: `"$` + ownerEnv + `"`,
		agentAccount: `"$` + agentEnv + `"`,
		ownerAliases: make(map[string]bool, len(ownerAliases)),
		agentAliases: make(map[string]bool, len(agentAliases)),
	}
	for _, a := range agentAliases {
		if a == "" {
			continue
		}
		s.agentAliases[a] = true
		s.aliasList = append(s.aliasList, a)
	}
	for _, a := range ownerAliases {
		if a == "" {
			continue
		}
		if s.agentAliases[a] {
			return nil, fmt.Errorf("skills.gog: alias %q is listed as both owner and agent", a)
		}
		s.ownerAliases[a] = true
		s.aliasList = append(s.aliasList, a)
	}

	envFiles := c.EnvFiles
	if len(envFiles) == 0 {
		envFiles = legacyGogEnvFiles
	}
	for _, f := range envFiles {
		if p := resolveGogPath(f, workspaceDir); p != "" {
			s.envFiles = append(s.envFiles, p)
		}
	}

	s.cleanupScript = resolveGogPath(firstNonEmpty(c.CleanupScript, defaultGogCleanupScript), workspaceDir)
	return s, nil
}

// resolveGogPath expands "~/" and anchors relative paths at workspaceDir.
// Returns "" for a relative path when no workspace is known.
func resolveGogPath(p, workspaceDir string) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if filepath.IsAbs(p) {
		return p
	}
	if workspaceDir == "" {
		return ""
	}
	return filepath.Join(workspaceDir, p)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *gogSettings) isAgentAlias(v string) bool { return s.agentAliases[v] }
func (s *gogSettings) isOwnerAlias(v string) bool { return s.ownerAliases[v] }

// sendUsesOwner reports whether a gog/email send with these args goes out
// as the owner: any non-agent account, or an owner-alias from. Single source
// of truth for buildGogCommand and the approval gate (conduit-31jg.43).
// Callers must run validateAccountArg first.
func (s *gogSettings) sendUsesOwner(args map[string]interface{}) bool {
	if acct, ok := args["account"].(string); ok && acct != "" && !s.isAgentAlias(acct) {
		return true
	}
	from, _ := args["from"].(string)
	return s.isOwnerAlias(from)
}

// validateAccountArg rejects account/inbox values outside the configured
// alias set. Model-supplied identities are never interpolated into the
// shell; they only select one of the fixed account expansions
// (conduit-31jg.2).
func (s *gogSettings) validateAccountArg(args map[string]interface{}, key string) error {
	raw, present := args[key]
	if !present || raw == nil {
		return nil
	}
	v, ok := raw.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", key)
	}
	if v == "" || s.isAgentAlias(v) || s.isOwnerAlias(v) {
		return nil
	}
	return fmt.Errorf("unknown %s %q (allowed: %s)", key, v, strings.Join(s.aliasList, ", "))
}

// ConfigureGog applies skills.gog settings to the executor. workspaceDir
// anchors relative paths. An invalid config is logged by the caller and the
// executor keeps its current (default) settings.
func (e *Executor) ConfigureGog(cfg *GogConfig, workspaceDir string) error {
	s, err := resolveGogSettings(cfg, workspaceDir)
	if err != nil {
		return err
	}
	e.workspaceDir = workspaceDir
	e.gog = s
	return nil
}

// defaultGogSettings resolves the built-in defaults (always valid).
func defaultGogSettings() *gogSettings {
	s, err := resolveGogSettings(nil, "")
	if err != nil {
		panic(fmt.Sprintf("skills: default gog settings invalid: %v", err))
	}
	return s
}
