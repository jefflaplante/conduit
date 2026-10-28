package rem

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/brain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H6: orphan detection must resolve relative file: sources against
// WorkspaceDir (as Groom does), never against the process working directory.

func ltmExists(t *testing.T, rem *REMCycle, key string) bool {
	t.Helper()
	var n int
	require.NoError(t, rem.db.QueryRow("SELECT COUNT(*) FROM brain_ltm WHERE key = ?", key).Scan(&n))
	return n == 1
}

func TestPrune_RelativeFileSourceResolvedAgainstWorkspace(t *testing.T) {
	rem, b, ws := setupTestREMCycle(t)
	defer b.Close()
	t.Chdir(t.TempDir()) // the gateway's CWD is not the workspace

	ctx := brain.WithUserID(context.Background(), "testuser")
	require.NoError(t, os.WriteFile(filepath.Join(ws, "MEMORY.md"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "memory"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "memory", "notes.md"), []byte("x"), 0o644))

	require.NoError(t, b.Store(ctx, "live.root", "v", brain.TierLongTerm, "file:MEMORY.md"))
	require.NoError(t, b.Store(ctx, "live.fragment", "v", brain.TierLongTerm, "file:MEMORY.md#L3"))
	require.NoError(t, b.Store(ctx, "live.nested", "v", brain.TierLongTerm, "file:memory/notes.md"))
	require.NoError(t, b.Store(ctx, "gone.rel", "v", brain.TierLongTerm, "file:deleted.md"))
	require.NoError(t, b.Store(ctx, "gone.fragment", "v", brain.TierLongTerm, "file:deleted.md#section"))

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"gone.rel", "gone.fragment"}, result.Orphaned)
	for _, k := range []string{"live.root", "live.fragment", "live.nested"} {
		assert.True(t, ltmExists(t, rem, k), "%s has an existing workspace source and must survive", k)
	}
	for _, k := range []string{"gone.rel", "gone.fragment"} {
		assert.False(t, ltmExists(t, rem, k), "%s source is missing and must be archived", k)
	}
}

func TestPrune_RelativeSourceIgnoresCWD(t *testing.T) {
	// A file that exists only in the CWD does not keep a workspace-relative
	// fact alive: the workspace path is the one that is checked.
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()
	cwd := t.TempDir()
	t.Chdir(cwd)
	require.NoError(t, os.WriteFile(filepath.Join(cwd, "only-in-cwd.md"), []byte("x"), 0o644))

	ctx := brain.WithUserID(context.Background(), "testuser")
	require.NoError(t, b.Store(ctx, "cwd.only", "v", brain.TierLongTerm, "file:only-in-cwd.md"))

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"cwd.only"}, result.Orphaned)
}

func TestPrune_RelativeSourceSkippedWithoutWorkspaceDir(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()
	rem.config.WorkspaceDir = ""
	t.Chdir(t.TempDir())

	ctx := brain.WithUserID(context.Background(), "testuser")
	require.NoError(t, b.Store(ctx, "rel.unknown", "v", brain.TierLongTerm, "file:MEMORY.md"))

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	assert.Empty(t, result.Orphaned, "a relative source cannot be resolved without WorkspaceDir")
	assert.True(t, ltmExists(t, rem, "rel.unknown"))
}

func TestPrune_AbsoluteSourcesStillChecked(t *testing.T) {
	rem, b, ws := setupTestREMCycle(t)
	defer b.Close()
	rem.config.WorkspaceDir = "" // absolute paths need no workspace

	ctx := brain.WithUserID(context.Background(), "testuser")
	live := filepath.Join(ws, "live.md")
	require.NoError(t, os.WriteFile(live, []byte("x"), 0o644))
	gone := filepath.Join(ws, "gone.md")

	require.NoError(t, b.Store(ctx, "abs.live", "v", brain.TierLongTerm, live))
	require.NoError(t, b.Store(ctx, "abs.live.file", "v", brain.TierLongTerm, "file:"+live))
	require.NoError(t, b.Store(ctx, "abs.live.fragment", "v", brain.TierLongTerm, "file:"+live+"#L1"))
	require.NoError(t, b.Store(ctx, "abs.gone", "v", brain.TierLongTerm, gone))
	require.NoError(t, b.Store(ctx, "abs.gone.fragment", "v", brain.TierLongTerm, "file:"+gone+"#L1"))

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"abs.gone", "abs.gone.fragment"}, result.Orphaned)
}

func TestPrune_HashInFileNameNotTreatedAsFragment(t *testing.T) {
	rem, b, ws := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")
	require.NoError(t, os.WriteFile(filepath.Join(ws, "notes#1.md"), []byte("x"), 0o644))
	require.NoError(t, b.Store(ctx, "hash.name", "v", brain.TierLongTerm, "file:notes#1.md"))

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	assert.Empty(t, result.Orphaned)
}
