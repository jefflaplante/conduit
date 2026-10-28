package gateway

import (
	"bytes"
	"os"
	"path/filepath"

	"conduit/internal/config"
	"conduit/internal/sandbox"
)

// Approved-config snapshot. Every config.json written by an approved
// update_config is copied to approvedSnapshotPath (0600, next to the
// config). At startup the file the gateway loaded is compared with it: a
// difference means config.json was changed some other way (a hand edit, or
// a write the Bash checker did not catch), and is logged at ERROR with the
// changed keys, the security-sensitive ones and any lowered approval. The
// gateway still starts with the file as it is, since a hand edit is the
// owner's normal way to configure it; the snapshot is then re-based so the
// error is logged once per change.
//
// The config file, its snapshot and its .bak are protected (sandbox.Protect)
// so the Write, Edit and Bash tools refuse to write them.

// approvedSnapshotPath is the snapshot file for config path.
func approvedSnapshotPath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".approved")
}

// protectConfigFiles registers the config file, its backup and its
// snapshot as protected. Symlinks are resolved, as config.ReplaceFile does.
func protectConfigFiles(path string) {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	sandbox.Protect(path)
	sandbox.Protect(target + config.BackupSuffix)
	sandbox.Protect(approvedSnapshotPath(path))
}

// recordApprovedConfig stores data as the last approved config.
func (g *Gateway) recordApprovedConfig(path string, data []byte) {
	if err := writeSnapshot(approvedSnapshotPath(path), data); err != nil {
		g.log().Warn("config snapshot: could not record the approved config", "component", "config_reload",
			"snapshot", approvedSnapshotPath(path), "error", err)
	}
}

// checkApprovedConfig compares the config file with the last approved one.
func (g *Gateway) checkApprovedConfig(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // startup already reported an unreadable config
	}
	snap := approvedSnapshotPath(path)
	approved, err := os.ReadFile(snap)
	switch {
	case os.IsNotExist(err):
		g.recordApprovedConfig(path, data)
		return
	case err != nil:
		g.log().Warn("config snapshot: could not read the approved config", "component", "config_reload",
			"snapshot", snap, "error", err)
		return
	case bytes.Equal(approved, data):
		return
	}

	keys, sensitive, weakenings := describeConfigChange(approved, data)
	g.log().Error("config file changed outside update_config since the last approved write",
		"component", "config_reload", "config_path", path,
		"changed_keys", keys, "security_keys", sensitive, "approval_lowered", weakenings,
		"hint", "if you did not edit it, compare it with "+snap)
	g.recordApprovedConfig(path, data)
}

// describeConfigChange lists the keys that differ between two config
// files, those KeyRisk marks security-sensitive, and any lowered approval.
// Values are never returned. Unparseable input gives "(unparseable)".
func describeConfigChange(old, next []byte) (keys, sensitive, weakenings []string) {
	patch, err := config.PatchFromFiles(old, next)
	if err != nil {
		return []string{"(unparseable)"}, nil, nil
	}
	if ops, err := config.FlattenPatch(patch); err == nil {
		for _, op := range ops {
			k := config.JoinPath(op.Path)
			keys = append(keys, k)
			if config.KeyRisk(op.Path) != "" {
				sensitive = append(sensitive, k)
			}
		}
	}
	if oc, err := config.Parse(old); err == nil {
		if nc, err := config.Parse(next); err == nil {
			weakenings = config.ApprovalWeakenings(oc, nc)
		}
	}
	return keys, sensitive, weakenings
}

func writeSnapshot(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
