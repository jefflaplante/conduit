package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"conduit/internal/brain"

	"github.com/spf13/cobra"
)

// BrainRootCmd returns the top-level "brain" command group for brain operations.
func BrainRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "brain",
		Short: "Brain LTM graph operations",
		Long:  `Manage the Brain long-term memory system - export graphs, inspect state.`,
	}

	cmd.AddCommand(brainExportCmd())

	return cmd
}

func brainExportCmd() *cobra.Command {
	var (
		outputPath string
		dbPath     string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the Brain LTM graph to JSON",
		Long: `Export the complete LTM graph (nodes and edges) to a JSON file.
Includes all non-expired nodes with full metadata and all relationships.

The output file contains:
- nodes: Array of graph nodes with key, value, source, salience, warmth, access_count, created_at
- edges: Array of relationships between nodes with key_a, key_b, relationship, confidence, last_traversed_at

This is read-only and does not modify brain state.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Use the gateway's data directory if dbPath is not specified
			if dbPath == "" {
				homeDir, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("failed to get home directory: %w", err)
				}
				dbPath = filepath.Join(homeDir, ".conduit", "brain.db")
			}

			// Default output path if not specified
			if outputPath == "" {
				outputPath = filepath.Join(filepath.Dir(dbPath), "brain-export.json")
			}

			// Initialize brain
			b, err := brain.New(dbPath)
			if err != nil {
				return fmt.Errorf("failed to initialize brain at %s: %w", dbPath, err)
			}
			defer b.Close()

			// Export graph to file
			if err := b.ExportGraphFile(outputPath); err != nil {
				return fmt.Errorf("failed to export graph: %w", err)
			}

			// Load the export to get counts
			data, err := os.ReadFile(outputPath)
			if err != nil {
				return fmt.Errorf("failed to read export file: %w", err)
			}

			var graph brain.Graph
			if err := json.Unmarshal(data, &graph); err != nil {
				return fmt.Errorf("failed to parse export: %w", err)
			}

			fmt.Println("Brain graph exported successfully!")
			fmt.Printf("  Database:  %s\n", dbPath)
			fmt.Printf("  Output:    %s\n", outputPath)
			fmt.Printf("  Nodes:     %d\n", len(graph.Nodes))
			fmt.Printf("  Edges:     %d\n", len(graph.Edges))

			return nil
		},
	}

	cmd.Flags().StringVar(&outputPath, "out", "", "Output file path (default: ~/.conduit/brain-export.json)")
	cmd.Flags().StringVar(&dbPath, "db", "", "Brain database path (default: ~/.conduit/brain.db)")

	return cmd
}