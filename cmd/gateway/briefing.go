package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"conduit/internal/briefing"
	"conduit/internal/config"
	"conduit/internal/sessions"

	"github.com/spf13/cobra"
)

// BriefingRootCmd creates the briefing command tree.
func BriefingRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "briefing",
		Short: "Generate and manage session briefings",
		Long: `Generate context handoff summaries for session transitions.
When switching sessions or starting a new one, briefing generates a summary
of what happened, what's in progress, and what's next.`,
	}

	cmd.AddCommand(
		briefingGenerateCmd(),
		briefingShowCmd(),
		briefingListCmd(),
	)

	return cmd
}

// briefingGenerateCmd generates a briefing from the current/last session.
func briefingGenerateCmd() *cobra.Command {
	var (
		sessionID  string
		outputJSON bool
		limit      int
	)

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a briefing from a session",
		Long: `Generate a context handoff briefing from the current or specified session.
The briefing summarizes what happened, key decisions, files changed, tools used,
open questions, and next steps.

Examples:
  conduit briefing generate                          # Latest session
  conduit briefing generate --session=my-session-key # Specific session
  conduit briefing generate --json                   # JSON output`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBriefingGenerate(sessionID, outputJSON, limit)
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "Session key to generate briefing from (default: latest)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum number of messages to analyze (0 = all)")

	return cmd
}

// briefingShowCmd displays a saved briefing.
func briefingShowCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "show [ID]",
		Short: "Display a saved briefing",
		Long: `Show the full contents of a saved briefing by its ID.

Examples:
  conduit briefing show briefing-abc-20260101-120000
  conduit briefing show briefing-abc-20260101-120000 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBriefingShow(args[0], outputJSON)
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")

	return cmd
}

// briefingListCmd lists recent briefings.
func briefingListCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent briefings",
		Long: `List all saved briefings, sorted by most recent first.

Examples:
  conduit briefing list
  conduit briefing list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBriefingList(outputJSON)
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")

	return cmd
}

// --- command implementations ---

func runBriefingGenerate(sessionID string, outputJSON bool, limit int) error {
	// Load config to find workspace and database paths.
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Open session store.
	dbPath := cfg.Database.Path
	if dbPath == "" {
		dbPath = "gateway.db"
	}

	store, err := sessions.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open session store: %w", err)
	}
	defer store.Close()

	// Resolve session.
	var session *sessions.Session
	if sessionID != "" {
		session, err = store.GetSession(sessionID)
		if err != nil {
			return fmt.Errorf("session not found: %w", err)
		}
	} else {
		// Get the most recent session across all users/channels.
		activeSessions, err := store.ListActiveSessions(1)
		if err != nil {
			return fmt.Errorf("failed to list sessions: %w", err)
		}
		if len(activeSessions) == 0 {
			return fmt.Errorf("no sessions found")
		}
		session = &activeSessions[0]
	}

	// Retrieve messages.
	msgs, err := store.GetMessages(session.Key, limit)
	if err != nil {
		return fmt.Errorf("failed to get messages: %w", err)
	}

	if len(msgs) == 0 {
		return fmt.Errorf("no messages in session %s", session.Key)
	}

	// Convert session messages to briefing messages.
	briefingMsgs := make([]briefing.Message, len(msgs))
	for i, m := range msgs {
		briefingMsgs[i] = briefing.Message{
			ID:        m.ID,
			Role:      m.Role,
			Content:   m.Content,
			Timestamp: m.Timestamp,
			Metadata:  m.Metadata,
		}
	}

	// Generate briefing.
	gen := briefing.NewGenerator()
	b, err := gen.Generate(session.Key, briefingMsgs)
	if err != nil {
		return fmt.Errorf("failed to generate briefing: %w", err)
	}

	// Save the briefing to workspace/briefings/.
	briefingsDir := resolveBriefingsDir(cfg)
	if err := briefing.Save(b, briefingsDir); err != nil {
		return fmt.Errorf("failed to save briefing: %w", err)
	}

	// Output.
	if outputJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(b)
	}

	fmt.Printf("Briefing generated: %s\n", b.ID)
	fmt.Printf("Session: %s\n", b.SessionID)
	fmt.Printf("Messages analyzed: %d\n", b.MessageCount)
	if b.Duration > 0 {
		fmt.Printf("Session duration: %s\n", b.Duration.Round(time.Second))
	}
	fmt.Printf("Saved to: %s\n\n", filepath.Join(briefingsDir, b.ID+".json"))

	printBriefingText(b)

	return nil
}

func runBriefingShow(id string, outputJSON bool) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	briefingsDir := resolveBriefingsDir(cfg)
	path := filepath.Join(briefingsDir, id+".json")

	b, err := briefing.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load briefing: %w", err)
	}

	if outputJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(b)
	}

	fmt.Printf("Briefing: %s\n", b.ID)
	fmt.Printf("Session: %s\n", b.SessionID)
	fmt.Printf("Generated: %s\n", b.Timestamp.Format(time.RFC3339))
	fmt.Printf("Messages: %d\n", b.MessageCount)
	if b.Duration > 0 {
		fmt.Printf("Duration: %s\n", b.Duration.Round(time.Second))
	}
	fmt.Println()

	printBriefingText(b)

	return nil
}

func runBriefingList(outputJSON bool) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	briefingsDir := resolveBriefingsDir(cfg)
	summaries, err := briefing.ListBriefings(briefingsDir)
	if err != nil {
		return fmt.Errorf("failed to list briefings: %w", err)
	}

	if len(summaries) == 0 {
		fmt.Println("No briefings found.")
		fmt.Printf("Generate one with: conduit briefing generate\n")
		return nil
	}

	if outputJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(summaries)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSESSION\tTIMESTAMP\tDURATION\tSUMMARY")
	fmt.Fprintln(w, "--\t-------\t---------\t--------\t-------")

	for _, s := range summaries {
		summary := s.Summary
		if len(summary) > 60 {
			summary = summary[:57] + "..."
		}

		durationStr := "-"
		if s.Duration > 0 {
			durationStr = s.Duration.Round(time.Second).String()
		}

		sessionID := s.SessionID
		if len(sessionID) > 20 {
			sessionID = sessionID[:17] + "..."
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			s.ID,
			sessionID,
			s.Timestamp.Format("2006-01-02 15:04"),
			durationStr,
			summary,
		)
	}

	return w.Flush()
}

// --- helpers ---

func resolveBriefingsDir(cfg *config.Config) string {
	workspace := cfg.Tools.Sandbox.WorkspaceDir
	if workspace == "" {
		workspace = "."
	}
	return filepath.Join(workspace, "briefings")
}

func printBriefingText(b *briefing.Briefing) {
	fmt.Printf("Summary:\n  %s\n\n", b.Summary)

	if len(b.KeyDecisions) > 0 {
		fmt.Println("Key Decisions:")
		for _, d := range b.KeyDecisions {
			fmt.Printf("  - %s\n", d)
		}
		fmt.Println()
	}

	if len(b.FilesChanged) > 0 {
		fmt.Println("Files Changed:")
		for _, f := range b.FilesChanged {
			fmt.Printf("  - %s\n", f)
		}
		fmt.Println()
	}

	if len(b.ToolsUsed) > 0 {
		fmt.Println("Tools Used:")
		for _, t := range b.ToolsUsed {
			fmt.Printf("  - %s (%dx)\n", t.Name, t.Count)
		}
		fmt.Println()
	}

	if len(b.OpenQuestions) > 0 {
		fmt.Println("Open Questions:")
		for _, q := range b.OpenQuestions {
			fmt.Printf("  - %s\n", q)
		}
		fmt.Println()
	}

	if len(b.NextSteps) > 0 {
		fmt.Println("Next Steps:")
		for _, s := range b.NextSteps {
			fmt.Printf("  - %s\n", s)
		}
		fmt.Println()
	}
}

func init() {
	rootCmd.AddCommand(BriefingRootCmd())
}
