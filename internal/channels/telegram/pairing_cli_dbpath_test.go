package telegram

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/sessions"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writePairingTestConfig writes a config whose FILE NAME ("custom.json")
// would have led the old CLI to open "custom.db", while database.path points
// at "server.db" — the file the server actually opens.
func writePairingTestConfig(t *testing.T) (cfgPath, serverDB, filenameDB string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "custom.json")
	serverDB = filepath.Join(dir, "server.db")
	filenameDB = filepath.Join(dir, "custom.db")
	raw := `{"port": 18789, "database": {"path": "` + serverDB + `"},
		"tools": {"max_tool_chains": 5, "enabled_tools": ["read"]}}`
	require.NoError(t, os.WriteFile(cfgPath, []byte(raw), 0600))
	return
}

// conduit-31jg.48: `conduit pairing` must open the same database the server
// opens (auth.ResolveDatabasePath(config.Load(--config))), not a path derived
// from the config filename via the CONDUIT_DB_PATH env hack.
func TestPairingCLI_UsesServerDatabase(t *testing.T) {
	t.Setenv("CONDUIT_DATA_DIR", t.TempDir())
	cfgPath, serverDB, filenameDB := writePairingTestConfig(t)

	// What the server opens (gateway.go: sessions.NewStore(auth.ResolveDatabasePath(cfg))).
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Equal(t, serverDB, auth.ResolveDatabasePath(cfg))

	store, err := sessions.NewStore(auth.ResolveDatabasePath(cfg))
	require.NoError(t, err)
	p, err := NewPairingStorage(store.DB()).CreatePairing("user-42", time.Hour)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	// Even if a stale env var from the old hack is present, it is ignored.
	t.Setenv("CONDUIT_DB_PATH", filenameDB)

	cli := &PairingCLIConfig{ConfigPath: cfgPath}
	got, err := cli.resolveDatabasePath()
	require.NoError(t, err)
	assert.Equal(t, serverDB, got)

	require.NoError(t, approvePairing(cli, p.Code))

	store2, err := sessions.NewStore(serverDB)
	require.NoError(t, err)
	defer store2.Close()
	after, err := NewPairingStorage(store2.DB()).GetPairingByCode(p.Code)
	require.NoError(t, err)
	assert.False(t, after.IsActive, "approval must land in the server's database")

	_, statErr := os.Stat(filenameDB)
	assert.True(t, os.IsNotExist(statErr), "filename-derived DB must not be created")
}

// An explicit --database still overrides, and a missing config is an error
// rather than silently creating a fresh default database.
func TestPairingCLI_ResolveOverridesAndErrors(t *testing.T) {
	cli := &PairingCLIConfig{ConfigPath: "/nonexistent/config.json"}
	_, err := cli.resolveDatabasePath()
	require.Error(t, err)

	cli = &PairingCLIConfig{ConfigPath: "/nonexistent/config.json", DatabasePath: "/tmp/explicit.db"}
	got, err := cli.resolveDatabasePath()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/explicit.db", got)
}
