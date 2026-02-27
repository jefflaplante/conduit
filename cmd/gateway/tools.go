package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/types"

	"github.com/spf13/cobra"
)

// ToolsRootCmd creates the tools management command tree
func ToolsRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "Manage and discover available tools",
		Long: `The tools command provides utilities for discovering, inspecting,
and understanding the available tools in Conduit-Go.

This includes listing all available tools, showing detailed schemas,
and providing usage examples.`,
	}

	cmd.AddCommand(
		toolsListCmd(),
		toolsDescribeCmd(),
		toolsSchemaCmd(),
		toolsExamplesCmd(),
	)

	return cmd
}

// toolsListCmd lists all available tools
func toolsListCmd() *cobra.Command {
	var (
		category string
		detailed bool
	)

	cmd := &cobra.Command{
		Use:   "list [FILTER]",
		Short: "List available tools",
		Long: `List all available tools, optionally filtered by name or category.

Examples:
  conduit tools list                    # List all tools
  conduit tools list file              # List tools with 'file' in name
  conduit tools list --category=core   # List tools in core category
  conduit tools list --detailed        # Show detailed information`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runToolsList(args, category, detailed)
		},
	}

	cmd.Flags().StringVar(&category, "category", "", "Filter by tool category")
	cmd.Flags().BoolVarP(&detailed, "detailed", "d", false, "Show detailed tool information")

	return cmd
}

// toolsDescribeCmd describes a specific tool
func toolsDescribeCmd() *cobra.Command {
	var (
		format   string
		examples bool
	)

	cmd := &cobra.Command{
		Use:   "describe TOOL_NAME",
		Short: "Describe a specific tool in detail",
		Long: `Show detailed information about a specific tool including its parameters,
usage examples, and validation rules.

Examples:
  conduit tools describe Message       # Describe the Message tool
  conduit tools describe Read --examples  # Include usage examples
  conduit tools describe Write --format=json  # JSON output`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runToolsDescribe(args[0], format, examples)
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format (text, json)")
	cmd.Flags().BoolVar(&examples, "examples", false, "Include usage examples")

	return cmd
}

// toolsSchemaCmd shows raw JSON schema for a tool
func toolsSchemaCmd() *cobra.Command {
	var pretty bool

	cmd := &cobra.Command{
		Use:   "schema TOOL_NAME",
		Short: "Show JSON schema for a tool",
		Long: `Output the raw JSON schema for a specific tool. This is useful
for developers and for understanding the exact parameter structure.

Examples:
  conduit tools schema Message         # Raw schema
  conduit tools schema Read --pretty  # Pretty-printed JSON`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runToolsSchema(args[0], pretty)
		},
	}

	cmd.Flags().BoolVar(&pretty, "pretty", false, "Pretty-print JSON output")

	return cmd
}

// toolsExamplesCmd shows usage examples for tools
func toolsExamplesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "examples [TOOL_NAME]",
		Short: "Show usage examples",
		Long: `Show usage examples for tools. If no tool is specified,
shows examples for all tools that have them.

Examples:
  conduit tools examples               # Examples for all tools
  conduit tools examples Message      # Examples for Message tool`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var toolName string
			if len(args) > 0 {
				toolName = args[0]
			}
			return runToolsExamples(toolName)
		},
	}

	return cmd
}

// createRegistry creates a basic registry for CLI use
func createRegistry() *tools.Registry {
	// Create basic sandbox config
	sandboxCfg := config.SandboxConfig{
		WorkspaceDir: "/tmp",
		AllowedPaths: []string{"/tmp", "/home"},
	}

	// Create basic tools config with all core tools enabled
	toolsCfg := config.ToolsConfig{
		Sandbox: sandboxCfg,
		EnabledTools: []string{
			"Read", "Write", "Edit", "Bash", "Glob",
			"MemorySearch", "SessionsList", "SessionsSend",
			"SessionsSpawn", "SessionStatus", "Gateway",
			"WebSearch", "WebFetch", "Message", "Tts", "Cron", "Image",
			"Chain",
		},
	}

	registry := tools.NewRegistry(toolsCfg)

	// Create minimal tool services for CLI usage
	services := &types.ToolServices{
		SessionStore:  nil, // Not needed for CLI discovery
		ConfigMgr:     nil, // Not needed for CLI discovery
		WebClient:     nil, // Not needed for CLI discovery
		SkillsManager: nil, // Not needed for CLI discovery
		Gateway:       nil, // Not needed for CLI discovery
	}

	registry.SetServices(services)
	return registry
}

// runToolsList implements the tools list command
func runToolsList(args []string, category string, detailed bool) error {
	reg := createRegistry()
	toolsMap := reg.GetAvailableTools()

	var filter string
	if len(args) > 0 {
		filter = strings.ToLower(args[0])
	}

	// Convert to slice and filter
	var toolNames []string
	for name := range toolsMap {
		// Apply name filter
		if filter != "" && !strings.Contains(strings.ToLower(name), filter) {
			continue
		}

		// Apply category filter (would need tool metadata)
		if category != "" {
			// For now, skip category filtering - would need enhanced metadata
			// This is a future enhancement opportunity
		}

		toolNames = append(toolNames, name)
	}

	sort.Strings(toolNames)

	if detailed {
		return showDetailedToolsList(toolNames, toolsMap)
	}

	return showSimpleToolsList(toolNames)
}

// showSimpleToolsList shows a simple list of tool names
func showSimpleToolsList(tools []string) error {
	fmt.Printf("Available tools (%d):\n", len(tools))
	for _, name := range tools {
		fmt.Printf("  %s\n", name)
	}
	return nil
}

// showDetailedToolsList shows detailed information for each tool
func showDetailedToolsList(toolNames []string, toolsMap map[string]types.Tool) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TOOL\tDESCRIPTION\tPARAMETERS")
	fmt.Fprintln(w, "----\t-----------\t----------")

	for _, name := range toolNames {
		if tool, ok := toolsMap[name]; ok {
			description := tool.Description()
			if len(description) > 50 {
				description = description[:47] + "..."
			}

			params := tool.Parameters()
			paramCount := len(params)
			paramInfo := fmt.Sprintf("%d params", paramCount)

			// Try to get required parameters from schema
			if props, ok := params["properties"].(map[string]interface{}); ok {
				_ = props // Just to use the variable
				if required, ok := params["required"].([]interface{}); ok {
					paramInfo += fmt.Sprintf(" (%d required)", len(required))
				}
			}

			fmt.Fprintf(w, "%s\t%s\t%s\n", name, description, paramInfo)
		}
	}

	return w.Flush()
}

// findToolSchema finds the schema for a specific tool name
func findToolSchema(registry *tools.Registry, toolName string) (map[string]interface{}, error) {
	schemas := registry.GetToolSchemasWithContext(context.Background())

	for _, schema := range schemas {
		if name, ok := schema["name"].(string); ok && name == toolName {
			return schema, nil
		}
	}

	return nil, fmt.Errorf("tool '%s' not found", toolName)
}

// runToolsDescribe implements the tools describe command
func runToolsDescribe(toolName, format string, includeExamples bool) error {
	reg := createRegistry()

	schema, err := findToolSchema(reg, toolName)
	if err != nil {
		return err
	}

	switch format {
	case "json":
		return outputToolJSON(schema, includeExamples)
	case "text":
		return outputToolText(schema, includeExamples)
	default:
		return fmt.Errorf("unsupported format: %s (use 'text' or 'json')", format)
	}
}

// outputToolJSON outputs tool information as JSON
func outputToolJSON(schema map[string]interface{}, includeExamples bool) error {
	var output interface{} = schema

	if includeExamples {
		// Create extended output with examples
		extended := map[string]interface{}{
			"schema": schema,
			"examples": map[string]interface{}{
				"note": "Examples would be extracted from enhanced schema providers",
			},
		}
		output = extended
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

// outputToolText outputs tool information in human-readable text format
func outputToolText(schema map[string]interface{}, includeExamples bool) error {
	name, _ := schema["name"].(string)
	description, _ := schema["description"].(string)

	fmt.Printf("Tool: %s\n", name)
	fmt.Printf("Description: %s\n\n", description)

	// Parameters
	if params, ok := schema["parameters"].(map[string]interface{}); ok {
		if props, ok := params["properties"].(map[string]interface{}); ok && len(props) > 0 {
			fmt.Println("Parameters:")

			// Sort parameter names for consistent output
			var paramNames []string
			for paramName := range props {
				paramNames = append(paramNames, paramName)
			}
			sort.Strings(paramNames)

			// Get required parameters
			var requiredParams []string
			if required, ok := params["required"].([]interface{}); ok {
				for _, req := range required {
					if reqStr, ok := req.(string); ok {
						requiredParams = append(requiredParams, reqStr)
					}
				}
			}

			for _, paramName := range paramNames {
				param := props[paramName].(map[string]interface{})

				isRequired := false
				for _, req := range requiredParams {
					if req == paramName {
						isRequired = true
						break
					}
				}

				requiredStr := ""
				if isRequired {
					requiredStr = " (required)"
				}

				fmt.Printf("  %s%s:\n", paramName, requiredStr)

				if paramType, ok := param["type"].(string); ok {
					fmt.Printf("    Type: %s\n", paramType)
				}

				if desc, ok := param["description"].(string); ok {
					fmt.Printf("    Description: %s\n", desc)
				}

				// Show enum values if available
				if enum, ok := param["enum"].([]interface{}); ok {
					fmt.Printf("    Valid values: %v\n", enum)
				}

				// Show default value if available
				if defaultVal, ok := param["default"]; ok {
					fmt.Printf("    Default: %v\n", defaultVal)
				}

				fmt.Println()
			}
		} else {
			fmt.Println("Parameters: None")
		}
	} else {
		fmt.Println("Parameters: None")
	}

	// Examples (if requested and available)
	if includeExamples {
		fmt.Println("Examples:")
		fmt.Printf("  # Usage examples for %s would be shown here\n", name)
		fmt.Printf("  # This would be extracted from EnhancedSchemaProvider implementations\n")
		fmt.Println()
	}

	return nil
}

// runToolsSchema implements the tools schema command
func runToolsSchema(toolName string, pretty bool) error {
	reg := createRegistry()

	schema, err := findToolSchema(reg, toolName)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(os.Stdout)
	if pretty {
		encoder.SetIndent("", "  ")
	}

	return encoder.Encode(schema)
}

// runToolsExamples implements the tools examples command
func runToolsExamples(toolName string) error {
	reg := createRegistry()

	if toolName == "" {
		// Show examples for all tools
		toolsMap := reg.GetAvailableTools()
		var toolNames []string
		for name := range toolsMap {
			toolNames = append(toolNames, name)
		}
		sort.Strings(toolNames)

		fmt.Println("Tool Usage Examples:")
		fmt.Println("===================")

		for _, name := range toolNames {
			fmt.Printf("\n%s:\n", name)
			if err := showToolExamples(name); err != nil {
				fmt.Printf("  (No examples available: %s)\n", err.Error())
			}
		}

		return nil
	}

	// Show examples for specific tool
	fmt.Printf("Examples for %s:\n", toolName)
	return showToolExamples(toolName)
}

// showToolExamples shows examples for a specific tool
func showToolExamples(toolName string) error {
	// For now, provide basic examples
	// In the future, this would extract from EnhancedSchemaProvider implementations

	switch strings.ToLower(toolName) {
	case "message":
		fmt.Printf("  # Send a direct message\n")
		fmt.Printf("  {\"action\":\"send\",\"target\":\"1098302846\",\"text\":\"Hello!\"}\n")
		fmt.Printf("  # Check channel status\n")
		fmt.Printf("  {\"action\":\"status\"}\n")

	case "read", "read_file":
		fmt.Printf("  # Read a file\n")
		fmt.Printf("  {\"path\":\"/home/user/document.txt\"}\n")

	case "write", "write_file":
		fmt.Printf("  # Write to a file\n")
		fmt.Printf("  {\"path\":\"/home/user/output.txt\",\"content\":\"Hello World\"}\n")

	case "bash":
		fmt.Printf("  # Execute a command\n")
		fmt.Printf("  {\"command\":\"ls -la\"}\n")
		fmt.Printf("  # Execute with working directory\n")
		fmt.Printf("  {\"command\":\"pwd\",\"cwd\":\"/tmp\"}\n")

	default:
		return fmt.Errorf("no examples available for tool '%s'", toolName)
	}

	return nil
}
