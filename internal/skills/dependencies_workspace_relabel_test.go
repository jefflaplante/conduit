package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkspaceRelabelStopsResolution verifies that lines labelled "Workspace:"
// are not treated as skill-relative reference deps after the SKILL.md relabel.
func TestWorkspaceRelabelStopsResolution(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(dir, "mytest")
	os.MkdirAll(skill, 0o755)
	content := "## 📎 Dependencies\n\n- **Workspace:** `reference/foo.md` (exists at workspace root only)\n"
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte(content), 0o644)

	paths := parseDependencyPaths(content)
	if len(paths) != 0 {
		t.Fatalf("expected 0 resolved dep paths, got %v", paths)
	}
	_ = strings.TrimSpace("")
}
