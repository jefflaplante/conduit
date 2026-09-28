package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"conduit/internal/config"
	"conduit/internal/datadir"
)

// Pidfile location (conduit-1qcg).
//
// The gateway writes its pidfile to the first of:
//  1. --pidfile, when given
//  2. $RUNTIME_DIRECTORY/conduit.pid (systemd RuntimeDirectory=conduit)
//  3. {data_dir}/conduit.pid (CONDUIT_DATA_DIR, then config data_dir, then ~/.conduit)
//  4. /tmp/conduit.pid (legacy; only if no data dir can be resolved)
//
// CLI commands (stop, restart, status, backup restore) run outside the
// unit's environment and so usually lack $RUNTIME_DIRECTORY. They search, in
// order: --pidfile (exclusively, if given), $RUNTIME_DIRECTORY/conduit.pid,
// /run/conduit/conduit.pid, {data_dir}/conduit.pid, /tmp/conduit.pid.
// The /tmp entry keeps a gateway started by an older binary findable during
// the transition.

const pidfileName = "conduit.pid"

// Overridable in tests.
var (
	systemRuntimePidfile = "/run/conduit/" + pidfileName
	legacyPidfile        = "/tmp/" + pidfileName
)

// pidfileEnv holds the inputs of pidfile resolution, kept separate from the
// process environment so resolution is a pure function.
type pidfileEnv struct {
	flag       string // --pidfile
	runtimeDir string // $RUNTIME_DIRECTORY (may be a colon-separated list)
	dataDir    string // resolved data dir root; "" if unknown
}

// runtimeDirPidfile returns $RUNTIME_DIRECTORY/conduit.pid, or "" when unset.
// systemd joins multiple RuntimeDirectory= entries with ':'; use the first.
func (e pidfileEnv) runtimeDirPidfile() string {
	dir, _, _ := strings.Cut(e.runtimeDir, ":")
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, pidfileName)
}

// writePath is where the gateway writes its pidfile.
func (e pidfileEnv) writePath() string {
	if e.flag != "" {
		return e.flag
	}
	if p := e.runtimeDirPidfile(); p != "" {
		return p
	}
	if e.dataDir != "" {
		return filepath.Join(e.dataDir, pidfileName)
	}
	return legacyPidfile
}

// candidates lists, in preference order and without duplicates, the paths
// the CLI searches for a running gateway's pidfile. An explicit --pidfile is
// authoritative and suppresses the search.
func (e pidfileEnv) candidates() []string {
	if e.flag != "" {
		return []string{e.flag}
	}
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(e.runtimeDirPidfile())
	add(systemRuntimePidfile)
	if e.dataDir != "" {
		add(filepath.Join(e.dataDir, pidfileName))
	}
	add(legacyPidfile)
	return out
}

// locatePidfile picks the pidfile the CLI should act on: the first candidate
// naming a live process, else the first that exists (so a stale pidfile is
// reported as such), else the first candidate (for error messages).
func locatePidfile(candidates []string) string {
	if len(candidates) == 0 {
		return legacyPidfile
	}
	firstExisting := ""
	for _, p := range candidates {
		pid, err := readPidfile(p)
		if err != nil {
			if firstExisting == "" {
				if _, statErr := os.Stat(p); statErr == nil {
					firstExisting = p
				}
			}
			continue
		}
		if processAlive(pid) {
			return p
		}
		if firstExisting == "" {
			firstExisting = p
		}
	}
	if firstExisting != "" {
		return firstExisting
	}
	return candidates[0]
}

// processAlive reports whether pid exists. EPERM means it exists but belongs
// to another user (e.g. the CLI runs as a different user than the service).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func pidfileFlag() string {
	p, _ := rootCmd.PersistentFlags().GetString("pidfile")
	return p
}

// serverPidfilePath returns where the running gateway writes its pidfile.
func serverPidfilePath(cfg *config.Config) string {
	env := pidfileEnv{flag: pidfileFlag(), runtimeDir: os.Getenv("RUNTIME_DIRECTORY")}
	if env.flag == "" && env.runtimeDirPidfile() == "" {
		env.dataDir = resolveDataDirRoot(cfg)
	}
	return env.writePath()
}

// cliPidfileCandidates returns the pidfile search list for CLI commands.
func cliPidfileCandidates() []string {
	env := pidfileEnv{flag: pidfileFlag(), runtimeDir: os.Getenv("RUNTIME_DIRECTORY")}
	if env.flag == "" {
		env.dataDir = resolveDataDirRoot(loadConfigIfPresent(cfgFile))
	}
	return env.candidates()
}

// findPidfile returns the pidfile CLI commands should read. See locatePidfile.
func findPidfile() string {
	return locatePidfile(cliPidfileCandidates())
}

// readGatewayPidfile finds and reads the gateway pidfile for stop/restart,
// naming every searched path when none is found.
func readGatewayPidfile() (int, string, error) {
	cands := cliPidfileCandidates()
	path := locatePidfile(cands)
	pid, err := readPidfile(path)
	if err != nil && len(cands) > 1 {
		if _, statErr := os.Stat(path); statErr != nil {
			return 0, path, fmt.Errorf("no gateway pidfile found (searched %s); pass --pidfile if the gateway writes one elsewhere",
				strings.Join(cands, ", "))
		}
	}
	return pid, path, err
}

// resolveDataDirRoot resolves the data dir the same way the gateway does
// (CONDUIT_DATA_DIR, then config data_dir, then ~/.conduit). "" on failure.
func resolveDataDirRoot(cfg *config.Config) string {
	var configured string
	if cfg != nil {
		configured = cfg.DataDir
	}
	dd, err := datadir.New(configured)
	if err != nil {
		return ""
	}
	return dd.Root()
}

// loadConfigIfPresent loads path only if it exists: config.Load writes a
// default config when the file is missing, which a CLI lookup must not do.
func loadConfigIfPresent(path string) *config.Config {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil
	}
	return cfg
}

// writePidfile writes the current PID to path, 0644 regardless of umask
// (the service runs with UMask=0077, but a PID is not secret and the CLI may
// run as another user). The parent directory is created if needed.
func writePidfile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return err
	}
	return os.Chmod(path, 0o644)
}
