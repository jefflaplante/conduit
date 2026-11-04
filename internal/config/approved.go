package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Approved-config check (fail closed). Every config file written by an
// approved update_config, or accepted by the owner with `conduit config
// accept`, is copied to ApprovedSnapshotPath (0600, next to the config;
// ApprovedStateSnapshotPath in stateDir when the config's directory is not
// writable, as in a container), and its SHA-256 is recorded in the data
// directory (ApprovedRecordPath).
// The server refuses to start when the config file differs from the
// snapshot, so a change that got past the tool guards (or a hand edit) only
// takes effect once the owner has looked at it and accepted it.
//
// States VerifyApproved distinguishes:
//   - no snapshot and no record: first run; both are written (bootstrap);
//   - no snapshot but a record: the snapshot was deleted; refused;
//   - a snapshot whose hash differs from the record: edited or corrupt;
//     refused;
//   - a snapshot that is not a JSON object: corrupt; refused;
//   - a snapshot that differs from the config: refused, naming the keys;
//   - a snapshot equal to the config: accepted (a missing record is added).

// AcceptCommand is the CLI command that approves the current config file.
const AcceptCommand = "config accept"

// ApprovedSnapshotPath is the approved-config snapshot for config path.
func ApprovedSnapshotPath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".approved")
}

// ApprovedRecordPath is the file in stateDir that records the snapshot's
// hash for config path. stateDir is the data directory's config dir.
func ApprovedRecordPath(path, stateDir string) string {
	return filepath.Join(stateDir, "approved-config-"+approvedID(path)+".sha256")
}

// ApprovedStateSnapshotPath is the fallback snapshot location for configs
// that live in a directory the gateway cannot write to (a container's
// read-only /etc): the same stateDir as the hash record, under the same id,
// so the pair moves together.
func ApprovedStateSnapshotPath(path, stateDir string) string {
	return filepath.Join(stateDir, "approved-config-"+approvedID(path)+".json")
}

func approvedID(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:6])
}

// UnapprovedConfigError says why a config file is not the approved one.
// It carries key names, never values.
type UnapprovedConfigError struct {
	Path     string
	Snapshot string
	Reason   string
	// Keys, Sensitive and Weakenings are set when the snapshot is readable.
	Keys, Sensitive, Weakenings []string
}

func (e *UnapprovedConfigError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is not the approved config: %s.\n", e.Path, e.Reason)
	if len(e.Keys) > 0 {
		fmt.Fprintf(&b, "  Changed keys: %s\n", strings.Join(e.Keys, ", "))
	}
	if len(e.Sensitive) > 0 {
		fmt.Fprintf(&b, "  Security-sensitive: %s\n", strings.Join(e.Sensitive, ", "))
	}
	for _, w := range e.Weakenings {
		fmt.Fprintf(&b, "  LOWERS HUMAN APPROVAL: %s\n", w)
	}
	b.WriteString("Changes made outside update_config need your approval before the gateway starts.\n")
	b.WriteString("If you made this change, review it and approve it with:\n")
	fmt.Fprintf(&b, "  conduit --config %s %s\n", absOr(e.Path), AcceptCommand)
	b.WriteString("If you did not, compare the file with " + e.Snapshot + " (the last approved version) first.")
	return b.String()
}

func absOr(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// ApprovedState is the result of a successful VerifyApproved.
type ApprovedState int

const (
	// ApprovedMatch: the config file is the approved one.
	ApprovedMatch ApprovedState = iota
	// ApprovedBootstrapped: first run; the current file was recorded.
	ApprovedBootstrapped
)

// VerifyApproved checks the config file at path against its approved
// snapshot; see the package comment above. It returns an
// *UnapprovedConfigError when the gateway must not start.
func VerifyApproved(path, stateDir string) (ApprovedState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read config: %w", err)
	}
	snap := ApprovedSnapshotPath(path)
	stateSnap := ApprovedStateSnapshotPath(path, stateDir)
	approved, recorded, loc, err := readApproved(path, stateDir)
	refuse := func(reason string) (ApprovedState, error) {
		return 0, &UnapprovedConfigError{Path: path, Snapshot: loc, Reason: reason}
	}
	switch {
	case errors.Is(err, errNoSnapshot) && recorded == "":
		if err := RecordApproved(path, stateDir, data); err != nil {
			return 0, err
		}
		return ApprovedBootstrapped, nil
	case errors.Is(err, errNoSnapshot):
		return refuse("the approved snapshot is missing (it existed before): not at " +
			snap + " nor at " + stateSnap)
	case err != nil:
		return refuse(err.Error())
	case bytes.Equal(approved, data):
		if recorded == "" {
			if err := writePrivate(ApprovedRecordPath(path, stateDir), []byte(hashHex(data)+"\n")); err != nil {
				return 0, err
			}
		}
		return ApprovedMatch, nil
	}
	keys, sensitive, weakenings := DescribeConfigChange(approved, data)
	return 0, &UnapprovedConfigError{
		Path: path, Snapshot: loc, Reason: "it was changed outside update_config",
		Keys: keys, Sensitive: sensitive, Weakenings: weakenings,
	}
}

var errNoSnapshot = errors.New("no approved snapshot")

// readApproved returns the snapshot and the recorded hash ("" if none).
// The snapshot is looked for next to the config first (the writable-config
// layout), then in stateDir (the read-only-config layout); loc is where it
// was found. The error is errNoSnapshot, or describes an unreadable,
// corrupt or edited snapshot.
func readApproved(path, stateDir string) (snapshot []byte, recorded, loc string, err error) {
	if b, rerr := os.ReadFile(ApprovedRecordPath(path, stateDir)); rerr == nil {
		recorded = strings.TrimSpace(string(b))
	} else if !os.IsNotExist(rerr) {
		return nil, "", "", fmt.Errorf("cannot read the approved-config record: %v", rerr)
	}
	loc = ApprovedSnapshotPath(path)
	for _, snap := range []string{ApprovedSnapshotPath(path), ApprovedStateSnapshotPath(path, stateDir)} {
		snapshot, err = os.ReadFile(snap)
		switch {
		case os.IsNotExist(err):
			loc = snap
			continue
		case err != nil:
			return nil, recorded, snap, fmt.Errorf("cannot read the approved snapshot %s: %v", snap, err)
		}
		loc = snap
		break
	}
	if os.IsNotExist(err) {
		return nil, recorded, loc, errNoSnapshot
	}
	var obj map[string]interface{}
	if jerr := json.Unmarshal(snapshot, &obj); jerr != nil || obj == nil {
		return nil, recorded, loc, fmt.Errorf("the approved snapshot %s is corrupt (not a JSON object)", loc)
	}
	if recorded != "" && recorded != hashHex(snapshot) {
		return nil, recorded, loc, fmt.Errorf("the approved snapshot %s does not match its recorded hash (edited or corrupt)", loc)
	}
	return snapshot, recorded, loc, nil
}

// ReadApprovedSnapshot returns the approved snapshot of path, or a reason
// it cannot be used ("" with a nil snapshot means there is none yet).
func ReadApprovedSnapshot(path, stateDir string) (snapshot []byte, problem string) {
	s, _, _, err := readApproved(path, stateDir)
	switch {
	case errors.Is(err, errNoSnapshot):
		return nil, ""
	case err != nil:
		return nil, err.Error()
	}
	return s, ""
}

// RecordApproved stores data as the approved config for path: the snapshot
// next to it when that directory is writable (the long-standing layout),
// otherwise in stateDir (a container's read-only /etc; the record moves
// with it), plus the hash record in stateDir. Both files are 0600. When
// neither location is writable it fails closed, naming both paths.
func RecordApproved(path, stateDir string, data []byte) error {
	// Try the long-standing layout first: the snapshot next to the config.
	// A directory-permission error (a container's read-only /etc) falls
	// back to stateDir; anything else is a real failure.
	if err := writePrivate(ApprovedSnapshotPath(path), data); err != nil {
		if !isDirUnwritable(err, filepath.Dir(path)) {
			return fmt.Errorf("approved config: %w", err)
		}
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			return fmt.Errorf("approved config: %w (config dir and state dir %s both unwritable)", err, stateDir)
		}
		if err := writePrivate(ApprovedStateSnapshotPath(path, stateDir), data); err != nil {
			return fmt.Errorf("approved config: %w (tried %s and %s)", err, ApprovedSnapshotPath(path), ApprovedStateSnapshotPath(path, stateDir))
		}
	} else if err := syncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("approved config: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("approved config: %w", err)
	}
	if err := writePrivate(ApprovedRecordPath(path, stateDir), []byte(hashHex(data)+"\n")); err != nil {
		return fmt.Errorf("approved config: %w", err)
	}
	return nil
}

// isDirUnwritable reports whether err only means that dir cannot be
// written (it does not exist, or refuses writes): a permission error whose
// path is dir or a temporary file created inside it.
func isDirUnwritable(err error, dir string) bool {
	var perr *fs.PathError
	if !errors.As(err, &perr) {
		return false
	}
	switch perr.Err {
	case syscall.ENOENT, syscall.EACCES, syscall.EPERM, syscall.EROFS:
	default:
		return false
	}
	p := perr.Path
	if p == dir {
		return true
	}
	return filepath.Dir(p) == dir
}

// syncDir flushes a directory entry so a crash right after the rename does
// not lose the just-written snapshot.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DescribeConfigChange lists the keys that differ between two config
// files, those KeyRisk marks security-sensitive, and any lowered approval.
// Values are never returned.
func DescribeConfigChange(old, next []byte) (keys, sensitive, weakenings []string) {
	for _, d := range DiffConfigFiles(old, next) {
		keys = append(keys, d.Path)
		if d.Risk != "" {
			sensitive = append(sensitive, d.Path)
		}
	}
	if oc, err := Parse(old); err == nil {
		if nc, err := Parse(next); err == nil {
			weakenings = ApprovalWeakenings(oc, nc)
		}
	}
	return keys, sensitive, weakenings
}

// ConfigDiffLine is one changed key with redacted old and new values.
type ConfigDiffLine struct {
	Path     string
	Old, New string // JSON, secrets redacted; "(absent)" when missing
	Risk     string // KeyRisk reason, "" when not security-sensitive
}

// DiffConfigFiles compares two config files key by key (objects merged,
// arrays whole). Values are redacted for display. Unparseable input gives
// a single "(whole file)" line.
func DiffConfigFiles(old, next []byte) []ConfigDiffLine {
	whole := []ConfigDiffLine{{Path: "(whole file)", Old: "(unreadable)", New: "(unreadable)", Risk: "unknown"}}
	var a, b map[string]interface{}
	if json.Unmarshal(old, &a) != nil || json.Unmarshal(next, &b) != nil {
		return whole
	}
	var out []ConfigDiffLine
	diffForDisplay(a, b, nil, &out)
	return out
}

func diffForDisplay(a, b map[string]interface{}, path []string, out *[]ConfigDiffLine) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		at := append(append([]string(nil), path...), k)
		av, inA := a[k]
		bv, inB := b[k]
		am, aObj := av.(map[string]interface{})
		bm, bObj := bv.(map[string]interface{})
		if inA && inB && aObj && bObj {
			diffForDisplay(am, bm, at, out)
			continue
		}
		if inA && inB && jsonEqual(av, bv) {
			continue
		}
		*out = append(*out, ConfigDiffLine{
			Path: JoinPath(at), Old: displayValue(at, av, inA), New: displayValue(at, bv, inB), Risk: KeyRisk(at),
		})
	}
}

func jsonEqual(a, b interface{}) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// displayValue renders v for a diff line: redacted, and cut to 120 bytes.
func displayValue(path []string, v interface{}, present bool) string {
	if !present {
		return "(absent)"
	}
	for _, seg := range path {
		if IsSecretKey(strings.ToLower(seg)) && !isEmptyOrScalarSetting(v) {
			return RedactedValue
		}
	}
	r, err := Redacted(v)
	if err != nil {
		return RedactedValue
	}
	s := string(encodeJSON(r))
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

func writePrivate(path string, data []byte) error {
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
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ToolShellEnv is set to "1" in the environment of every command the Bash
// tool runs. `conduit config accept` refuses to run when it is set, so the
// agent cannot approve its own config change.
const ToolShellEnv = "CONDUIT_TOOL_SHELL"
