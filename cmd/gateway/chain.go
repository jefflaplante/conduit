package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"conduit/internal/chain"
	"conduit/internal/config"

	"github.com/spf13/cobra"
)

// ChainRootCmd creates the chain command tree.
func ChainRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chain",
		Short: "Manage and execute saved tool chains",
		Long: `Save and execute common multi-tool workflows as reusable chains.
Chains are JSON files stored in workspace/chains/ that define a sequence
of tool invocations with variable substitution and dependency ordering.`,
	}

	cmd.AddCommand(
		chainListCmd(),
		chainShowCmd(),
		chainCreateCmd(),
		chainRunCmd(),
		chainDeleteCmd(),
		chainValidateCmd(),
	)

	return cmd
}

// chainListCmd lists saved chains.
func chainListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved chains",
		Long: `List all saved chain definitions in the workspace.

Examples:
  conduit chain list
  conduit chain list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChainList(outputJSON)
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")

	return cmd
}

// chainShowCmd displays chain details.
func chainShowCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Display chain details",
		Long: `Show the full definition of a saved chain including steps, variables,
and dependency graph.

Examples:
  conduit chain show my-workflow
  conduit chain show deploy-pipeline --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChainShow(args[0], outputJSON)
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")

	return cmd
}

// chainCreateCmd creates a new chain from a JSON definition.
func chainCreateCmd() *cobra.Command {
	var fromFile string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new chain",
		Long: `Create a new chain definition. Provide the definition via a JSON file
using the --from flag, or a minimal scaffold will be created.

Examples:
  conduit chain create deploy --from=deploy.json
  conduit chain create my-workflow`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChainCreate(args[0], fromFile)
		},
	}

	cmd.Flags().StringVar(&fromFile, "from", "", "JSON file containing chain definition")

	return cmd
}

// chainRunCmd executes a saved chain.
func chainRunCmd() *cobra.Command {
	var (
		vars       []string
		outputJSON bool
		dryRun     bool
	)

	cmd := &cobra.Command{
		Use:   "run <name>",
		Short: "Execute a saved chain",
		Long: `Run a saved chain with optional variable substitution.
In dry-run mode, the chain is validated and the execution plan is shown
without actually running any tools.

Examples:
  conduit chain run deploy --var env=production --var version=1.2.3
  conduit chain run my-workflow --dry-run
  conduit chain run build --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChainRun(args[0], vars, outputJSON, dryRun)
		},
	}

	cmd.Flags().StringArrayVar(&vars, "var", nil, "Variable in key=value format (repeatable)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate and show plan without executing")

	return cmd
}

// chainDeleteCmd removes a chain.
func chainDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Remove a chain",
		Long: `Delete a saved chain definition from the workspace.

Examples:
  conduit chain delete old-workflow`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChainDelete(args[0])
		},
	}

	return cmd
}

// chainValidateCmd validates a chain without executing it.
func chainValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <name>",
		Short: "Validate a chain definition",
		Long: `Check a chain definition for structural errors without executing it.
This checks tool names, dependency ordering, variable references, and cycles.

Examples:
  conduit chain validate my-workflow`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChainValidate(args[0])
		},
	}

	return cmd
}

// --- command implementations ---

func runChainList(outputJSON bool) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	chainsDir := resolveChainsDir(cfg)
	summaries, err := chain.ListChains(chainsDir)
	if err != nil {
		return fmt.Errorf("failed to list chains: %w", err)
	}

	if len(summaries) == 0 {
		fmt.Println("No chains found.")
		fmt.Printf("Create one with: conduit chain create <name> --from=<file.json>\n")
		return nil
	}

	if outputJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(summaries)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTEPS\tVARS\tDESCRIPTION")
	fmt.Fprintln(w, "----\t-----\t----\t-----------")

	for _, s := range summaries {
		desc := s.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\n", s.Name, s.StepCount, s.Variables, desc)
	}

	return w.Flush()
}

func runChainShow(name string, outputJSON bool) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	chainsDir := resolveChainsDir(cfg)
	c, err := chain.FindChain(chainsDir, name)
	if err != nil {
		return fmt.Errorf("chain not found: %w", err)
	}

	if outputJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(c)
	}

	fmt.Printf("Chain: %s\n", c.Name)
	if c.Description != "" {
		fmt.Printf("Description: %s\n", c.Description)
	}
	fmt.Printf("Steps: %d\n", len(c.Steps))
	fmt.Println()

	if len(c.Variables) > 0 {
		fmt.Println("Variables:")
		for _, v := range c.Variables {
			req := ""
			if v.Required {
				req = " (required)"
			}
			def := ""
			if v.Default != "" {
				def = fmt.Sprintf(" [default: %s]", v.Default)
			}
			desc := ""
			if v.Description != "" {
				desc = fmt.Sprintf(" - %s", v.Description)
			}
			fmt.Printf("  {{%s}}%s%s%s\n", v.Name, req, def, desc)
		}
		fmt.Println()
	}

	fmt.Println("Steps:")
	for i, step := range c.Steps {
		fmt.Printf("  %d. [%s] %s\n", i+1, step.ID, step.ToolName)
		if len(step.DependsOn) > 0 {
			fmt.Printf("     Depends on: %s\n", strings.Join(step.DependsOn, ", "))
		}
		if step.OnError != "" && step.OnError != "stop" {
			fmt.Printf("     On error: %s\n", step.OnError)
		}
		if len(step.Params) > 0 {
			paramsJSON, _ := json.Marshal(step.Params)
			paramStr := string(paramsJSON)
			if len(paramStr) > 80 {
				paramStr = paramStr[:77] + "..."
			}
			fmt.Printf("     Params: %s\n", paramStr)
		}
	}
	fmt.Println()

	return nil
}

func runChainCreate(name string, fromFile string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	chainsDir := resolveChainsDir(cfg)

	var c *chain.Chain

	if fromFile != "" {
		// Load from provided JSON file.
		c, err = chain.LoadChain(fromFile)
		if err != nil {
			return fmt.Errorf("failed to load chain definition: %w", err)
		}
		// Override name with the provided argument.
		c.Name = name
	} else {
		// Create a minimal scaffold.
		c = &chain.Chain{
			Name:        name,
			Description: fmt.Sprintf("Chain: %s", name),
			Steps: []chain.ChainStep{
				{
					ID:       "step-1",
					ToolName: "Bash",
					Params:   map[string]interface{}{"command": "echo 'hello from {{name}}'"},
				},
			},
			Variables: []chain.Variable{
				{Name: "name", Description: "A greeting name", Default: "world"},
			},
		}
	}

	// Validate before saving.
	errs := chain.ValidateChain(c, nil)
	if len(errs) > 0 {
		fmt.Println("Validation warnings:")
		for _, e := range errs {
			fmt.Printf("  - %s\n", e)
		}
		fmt.Println()
	}

	if err := chain.SaveChain(c, chainsDir); err != nil {
		return fmt.Errorf("failed to save chain: %w", err)
	}

	fmt.Printf("Chain %q created with %d step(s)\n", c.Name, len(c.Steps))
	fmt.Printf("Saved to: %s\n", filepath.Join(chainsDir, name+".json"))
	fmt.Printf("Edit the chain file to customize steps and variables.\n")

	return nil
}

func runChainRun(name string, varFlags []string, outputJSON bool, dryRun bool) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	chainsDir := resolveChainsDir(cfg)
	c, err := chain.FindChain(chainsDir, name)
	if err != nil {
		return fmt.Errorf("chain not found: %w", err)
	}

	// Parse variable flags.
	vars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid --var format %q, expected key=value", v)
		}
		vars[parts[0]] = parts[1]
	}

	// Validate first.
	validationErrs := chain.ValidateChain(c, nil)
	if len(validationErrs) > 0 {
		fmt.Println("Validation errors:")
		for _, e := range validationErrs {
			fmt.Printf("  - %s\n", e)
		}
		return fmt.Errorf("chain has %d validation error(s)", len(validationErrs))
	}

	if dryRun {
		fmt.Printf("Dry run for chain %q\n", c.Name)
		fmt.Printf("Steps: %d\n", len(c.Steps))
		fmt.Printf("Variables provided: %d\n", len(vars))
		fmt.Println()

		fmt.Println("Execution plan:")
		for i, step := range c.Steps {
			deps := ""
			if len(step.DependsOn) > 0 {
				deps = fmt.Sprintf(" (after: %s)", strings.Join(step.DependsOn, ", "))
			}
			fmt.Printf("  %d. %s: %s%s\n", i+1, step.ID, step.ToolName, deps)
		}
		fmt.Println()
		fmt.Println("Chain validated successfully. Use without --dry-run to execute.")
		return nil
	}

	// For actual execution, we need a tool executor.
	// In this CLI context, we don't have access to the full tool registry.
	// Print a message indicating the chain was validated and would be executed.
	fmt.Printf("Chain %q validated successfully (%d steps).\n", c.Name, len(c.Steps))
	fmt.Println()
	fmt.Println("Note: Chain execution requires a running gateway with tool registry access.")
	fmt.Println("The chain has been validated and is ready for execution via the gateway API.")
	fmt.Println()

	if outputJSON {
		result := map[string]interface{}{
			"chain":     c.Name,
			"steps":     len(c.Steps),
			"variables": vars,
			"validated": true,
			"message":   "Chain execution requires a running gateway",
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Println("Execution plan:")
	for i, step := range c.Steps {
		deps := ""
		if len(step.DependsOn) > 0 {
			deps = fmt.Sprintf(" (after: %s)", strings.Join(step.DependsOn, ", "))
		}
		onErr := ""
		if step.OnError != "" && step.OnError != "stop" {
			onErr = fmt.Sprintf(" [on-error: %s]", step.OnError)
		}
		fmt.Printf("  %d. [%s] %s%s%s\n", i+1, step.ID, step.ToolName, deps, onErr)
	}

	return nil
}

func runChainDelete(name string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	chainsDir := resolveChainsDir(cfg)
	if err := chain.DeleteChain(chainsDir, name); err != nil {
		return fmt.Errorf("failed to delete chain: %w", err)
	}

	fmt.Printf("Chain %q deleted.\n", name)
	return nil
}

func runChainValidate(name string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	chainsDir := resolveChainsDir(cfg)
	c, err := chain.FindChain(chainsDir, name)
	if err != nil {
		return fmt.Errorf("chain not found: %w", err)
	}

	errs := chain.ValidateChain(c, nil)
	if len(errs) == 0 {
		fmt.Printf("Chain %q is valid (%d steps, %d variables).\n", c.Name, len(c.Steps), len(c.Variables))
		return nil
	}

	fmt.Printf("Chain %q has %d validation error(s):\n", c.Name, len(errs))
	for _, e := range errs {
		fmt.Printf("  - %s\n", e)
	}

	return fmt.Errorf("validation failed with %d error(s)", len(errs))
}

// --- helpers ---

func resolveChainsDir(cfg *config.Config) string {
	workspace := cfg.Tools.Sandbox.WorkspaceDir
	if workspace == "" {
		workspace = "."
	}
	return filepath.Join(workspace, "chains")
}

func init() {
	rootCmd.AddCommand(ChainRootCmd())
}
