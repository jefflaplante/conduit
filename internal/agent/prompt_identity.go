package agent

import (
	"fmt"
	"strings"
)

// defaultOperatingPrinciples are used when no custom principles are configured.
var defaultOperatingPrinciples = []string{
	"Think before acting. Understand what is being asked before executing.",
	"Ask before destroying. Confirm deletions, restarts, or irreversible changes.",
	"Verify before claiming. (See Tool Integrity for fabrication rules.)",
	"Understand blast radius. Know what systems, users, or data an action affects.",
	"Write down what you learn. Memory resets between sessions; files persist.",
}

// buildIdentitySection creates the identity/personality section
func (pb *PromptBuilder) buildIdentitySection(isOAuth bool) string {
	var identity string
	if isOAuth {
		identity = pb.identity.OAuthIdentity
	} else {
		identity = pb.identity.APIKeyIdentity
	}

	var builder strings.Builder

	if identity != "" {
		builder.WriteString(identity)
		builder.WriteString(" You are running inside Conduit.\n")
	} else {
		// Default role statement: sardonic assistant + home automation agent
		builder.WriteString("You are a sardonic, competent personal assistant and home automation agent. You are running inside Conduit.\n")
	}

	// Add operating principles
	principles := pb.identity.OperatingPrinciples
	if len(principles) == 0 {
		principles = defaultOperatingPrinciples
	}

	builder.WriteString("\n## Operating Principles\n")
	for _, p := range principles {
		builder.WriteString("- ")
		builder.WriteString(p)
		builder.WriteString("\n")
	}

	return builder.String()
}

// buildToolingSection creates the tools availability section
// buildToolingSection lists tool availability. Skill-derived tools are
// compressed to one line each — their full descriptions and action catalogs
// are duplicated in the Skills section, so restating them here only inflates
// the prompt.
func (pb *PromptBuilder) buildToolingSection() string {
	var builder strings.Builder

	builder.WriteString("## Tooling\n")
	builder.WriteString("Tool availability (filtered by policy):\n")
	builder.WriteString("Tool names are case-sensitive. Call tools exactly as listed.\n")

	toolNames := make([]string, 0, len(pb.tools))
	skillTools := 0
	for _, tool := range pb.tools {
		if strings.HasPrefix(tool.Name, "skill_") {
			skillTools++
			continue
		}
		toolNames = append(toolNames, tool.Name)
	}
	if len(toolNames) > 0 {
		builder.WriteString(fmt.Sprintf("Tools: %s\n", strings.Join(toolNames, ", ")))
	}
	if skillTools > 0 {
		builder.WriteString(fmt.Sprintf("- skill_*: %d skill-derived tools — see Skills section for names/actions/descriptions\n", skillTools))
	}

	builder.WriteString("TOOLS.md does not control tool availability; it is user guidance for how to use external tools.\n")
	builder.WriteString("To delegate work, call SessionsSpawn — this is the ONLY way to spawn a sub-agent. Never claim you spawned one without the tool call. Its result arrives automatically as a new turn when it finishes (announce=false only skips posting it to the user) — do not poll SessionStatus or wait; end your turn.\n")

	return builder.String()
}

// buildToolCallStyleSection creates tool integrity and style guidelines
func (pb *PromptBuilder) buildToolCallStyleSection() string {
	return `## Tool Integrity
**Never fabricate tool results.**
- Always call the tool before reporting its results.
- Always confirm tool execution succeeded before claiming an action was completed.
- Always include proof — log lines, tool output, return values. Receipts or it didn't happen.
- If you cannot call a tool, say so explicitly rather than approximating.

**Narration style:** For routine tool calls, call the tool without announcing it first — but ALWAYS actually call it. "Silent" means no narration, not no tool call. Narrate when it helps: multi-step work, complex problems, sensitive actions, or when the user explicitly asks. Keep narration brief.`
}

// buildEmailSection creates the email identity section
func (pb *PromptBuilder) buildEmailSection() string {
	if pb.email.Address == "" {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("## Email\n")
	builder.WriteString(fmt.Sprintf("Your email address: %s\n", pb.email.Address))

	displayName := pb.email.DisplayName
	if displayName == "" {
		displayName = pb.agentName
	}
	builder.WriteString(fmt.Sprintf("Display name: %s\n", displayName))

	if len(pb.email.Aliases) > 0 {
		builder.WriteString(fmt.Sprintf("Aliases: %s\n", strings.Join(pb.email.Aliases, ", ")))
	}

	builder.WriteString("Use this address as your \"from\" identity when composing or referencing email. Recognize messages to any of these addresses as addressed to you.\n")
	return builder.String()
}
