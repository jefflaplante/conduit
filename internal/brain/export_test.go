package brain

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestExportGraphJSON verifies that ExportGraph returns JSON-serializable
// data with nodes and edges matching the LTM state.
func TestExportGraphJSON(t *testing.T) {
	// Set up a temporary brain database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	brain, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer brain.Close()

	ctx := context.Background()

	// Store some test entries
	testEntries := []struct {
		key    string
		value  string
		tier   Tier
		source string
	}{
		{"solar.panel_count", "30", TierLongTerm, "file:solar.md"},
		{"jeff.birthday", "Oct 5", TierLongTerm, "user"},
		{"solar.inverter", "EG4 18kPV", TierLongTerm, "file:infrastructure.md"},
		{"jeff.favorite_color", "Navy blue", TierLongTerm, "user"},
	}

	for _, te := range testEntries {
		if err := brain.Store(ctx, te.key, te.value, te.tier, te.source); err != nil {
			t.Fatalf("Store %s: %v", te.key, err)
		}
	}

	// Export the graph
	graph, err := brain.ExportGraph()
	if err != nil {
		t.Fatalf("ExportGraph: %v", err)
	}

	// Verify nodes
	if len(graph.Nodes) != 4 {
		t.Errorf("expected 4 nodes, got %d", len(graph.Nodes))
	}

	// Verify we can serialize to JSON
	jsonData, err := json.MarshalIndent(graph, "", "  ")
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Verify it's valid JSON by unmarshaling back
	var decoded Graph
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// Verify structure
	if len(decoded.Nodes) != 4 {
		t.Errorf("decoded expected 4 nodes, got %d", len(decoded.Nodes))
	}

	// Verify node fields
	for _, node := range decoded.Nodes {
		if node.Key == "" {
			t.Error("node key is empty")
		}
		if node.Value == "" {
			t.Errorf("node %s value is empty", node.Key)
		}
		if node.Salience <= 0 {
			t.Errorf("node %s salience %f <= 0", node.Key, node.Salience)
		}
		if node.CreatedAt.IsZero() {
			t.Errorf("node %s CreatedAt is zero", node.Key)
		}
	}

	// Verify edges if any exist (namespace edges may not exist immediately)
	if len(decoded.Edges) > 0 {
		for _, edge := range decoded.Edges {
			if edge.KeyA == "" || edge.KeyB == "" {
				t.Error("edge key_a or key_b is empty")
			}
			if edge.Confidence <= 0 {
				t.Errorf("edge %s->%s confidence %f <= 0", edge.KeyA, edge.KeyB, edge.Confidence)
			}
		}
	}
}

// TestExportGraphToFile verifies file writing works.
func TestExportGraphToFile(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	outPath := filepath.Join(tmpDir, "export.json")

	brain, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer brain.Close()

	ctx := context.Background()

	// Store a test entry
	if err := brain.Store(ctx, "test.key", "test value", TierLongTerm, "test"); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Export to file
	if err := brain.ExportGraphFile(outPath); err != nil {
		t.Fatalf("ExportGraphFile: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(outPath); os.IsNotExist(err) {
		t.Errorf("export file not created: %s", outPath)
	}

	// Read and verify valid JSON
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var graph Graph
	if err := json.Unmarshal(data, &graph); err != nil {
		t.Fatalf("json.Unmarshal from file: %v", err)
	}

	if len(graph.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(graph.Nodes))
	}
}

// TestExportGraphEmpty verifies export works with empty brain.
func TestExportGraphEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	brain, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer brain.Close()

	graph, err := brain.ExportGraph()
	if err != nil {
		t.Fatalf("ExportGraph on empty brain: %v", err)
	}

	if graph == nil {
		t.Fatal("graph is nil")
	}

	if graph.Nodes == nil {
		t.Fatal("graph.Nodes is nil")
	}

	if graph.Edges == nil {
		t.Fatal("graph.Edges is nil")
	}

	if len(graph.Nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(graph.Nodes))
	}

	if len(graph.Edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(graph.Edges))
	}

	// Verify it serializes to valid JSON
	_, err = json.Marshal(graph)
	if err != nil {
		t.Errorf("json.Marshal empty graph: %v", err)
	}
}