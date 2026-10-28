package gateway

import (
	"path/filepath"

	"conduit/internal/config"
	"conduit/internal/datadir"
	"conduit/internal/sandbox"
)

// Approved-config snapshot (config.VerifyApproved). Every config.json
// written by an approved update_config is recorded as the approved config;
// the server refuses to start on any other content until the owner runs
// `conduit config accept`.
//
// The config file, its .bak, its snapshot and the snapshot's hash record
// are protected (sandbox.Protect) so the Write, Edit and Bash tools refuse
// to write them.

// approvedStateDir is where the snapshot's hash record lives: the data
// directory's config dir.
func approvedStateDir(dataDir string) string {
	dd, err := datadir.New(dataDir)
	if err != nil {
		return filepath.Join(dataDir, "config")
	}
	return dd.ConfigDir()
}

// protectConfigFiles registers the config file, its backup, its snapshot
// and its hash record as protected. Symlinks are resolved, as
// config.ReplaceFile does.
func protectConfigFiles(path, stateDir string) {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	sandbox.Protect(path)
	sandbox.Protect(target + config.BackupSuffix)
	sandbox.Protect(config.ApprovedSnapshotPath(path))
	sandbox.Protect(config.ApprovedRecordPath(path, stateDir))
}

// recordApprovedConfig stores data as the last approved config.
func (g *Gateway) recordApprovedConfig(path string, data []byte) {
	if err := config.RecordApproved(path, approvedStateDir(g.currentConfig().DataDir), data); err != nil {
		// The next start will refuse this file until the owner accepts it.
		g.log().Error("config snapshot: could not record the approved config; the next start will ask for `conduit config accept`",
			"component", "config_reload", "snapshot", config.ApprovedSnapshotPath(path), "error", err)
	}
}
