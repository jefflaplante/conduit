package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"conduit/internal/config"
	"conduit/internal/datadir"
)

// withPidfileLocations points the well-known system and legacy pidfile
// locations into a temp dir for the duration of the test.
func withPidfileLocations(t *testing.T) (runPath, legacyPath string) {
	t.Helper()
	root := t.TempDir()
	origRun, origLegacy := systemRuntimePidfile, legacyPidfile
	systemRuntimePidfile = filepath.Join(root, "run", pidfileName)
	legacyPidfile = filepath.Join(root, "tmp", pidfileName)
	t.Cleanup(func() { systemRuntimePidfile, legacyPidfile = origRun, origLegacy })
	return systemRuntimePidfile, legacyPidfile
}

// withPidfileFlag sets the global --pidfile flag for the test.
func withPidfileFlag(t *testing.T, v string) {
	t.Helper()
	f := rootCmd.PersistentFlags().Lookup("pidfile")
	orig, origChanged := f.Value.String(), f.Changed
	if err := f.Value.Set(v); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Value.Set(orig); f.Changed = origChanged })
}

func writePID(t *testing.T, path string, pid int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPidfileWritePath(t *testing.T) {
	_, legacy := withPidfileLocations(t)
	tests := []struct {
		name string
		env  pidfileEnv
		want string
	}{
		{"explicit flag wins", pidfileEnv{flag: "/x/p.pid", runtimeDir: "/run/conduit", dataDir: "/data"}, "/x/p.pid"},
		{"runtime dir set", pidfileEnv{runtimeDir: "/run/conduit", dataDir: "/data"}, "/run/conduit/conduit.pid"},
		{"runtime dir list uses first", pidfileEnv{runtimeDir: "/run/conduit:/run/other", dataDir: "/data"}, "/run/conduit/conduit.pid"},
		{"runtime dir unset uses data dir", pidfileEnv{dataDir: "/data"}, "/data/conduit.pid"},
		{"nothing resolvable uses legacy", pidfileEnv{}, legacy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.writePath(); got != tt.want {
				t.Errorf("writePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPidfileCandidates(t *testing.T) {
	run, legacy := withPidfileLocations(t)
	tests := []struct {
		name string
		env  pidfileEnv
		want []string
	}{
		{"explicit flag is exclusive", pidfileEnv{flag: "/x/p.pid", runtimeDir: "/rd", dataDir: "/data"}, []string{"/x/p.pid"}},
		{"runtime dir set", pidfileEnv{runtimeDir: "/rd", dataDir: "/data"}, []string{"/rd/conduit.pid", run, "/data/conduit.pid", legacy}},
		// A CLI run outside the unit: no $RUNTIME_DIRECTORY.
		{"runtime dir unset", pidfileEnv{dataDir: "/data"}, []string{run, "/data/conduit.pid", legacy}},
		{"deduplicated", pidfileEnv{runtimeDir: filepath.Dir(run), dataDir: filepath.Dir(legacy)}, []string{run, legacy}},
		{"no data dir", pidfileEnv{}, []string{run, legacy}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.env.candidates(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("candidates() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocatePidfile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.pid")
	b := filepath.Join(dir, "b.pid")
	c := filepath.Join(dir, "c.pid")
	cands := []string{a, b, c}

	// None exist: first candidate, for the error message.
	if got := locatePidfile(cands); got != a {
		t.Errorf("none exist: got %q, want %q", got, a)
	}

	// Legacy fallback: only the last (legacy /tmp) candidate exists.
	writePID(t, c, os.Getpid())
	if got := locatePidfile(cands); got != c {
		t.Errorf("legacy only: got %q, want %q", got, c)
	}

	// A stale pidfile earlier in the list must not shadow a live one later.
	writePID(t, b, 999999999)
	if got := locatePidfile(cands); got != c {
		t.Errorf("stale b, live c: got %q, want %q", got, c)
	}

	// Stale only: report the first existing so status can say "stale".
	writePID(t, c, 999999998)
	if got := locatePidfile(cands); got != b {
		t.Errorf("all stale: got %q, want %q", got, b)
	}

	// Live preferred candidate wins.
	writePID(t, a, os.Getpid())
	if got := locatePidfile(cands); got != a {
		t.Errorf("live a: got %q, want %q", got, a)
	}
}

func TestServerPidfilePath(t *testing.T) {
	withPidfileLocations(t)
	dataDir := t.TempDir()
	t.Setenv(datadir.EnvVar, dataDir)

	t.Run("env set", func(t *testing.T) {
		rd := t.TempDir()
		t.Setenv("RUNTIME_DIRECTORY", rd)
		if got, want := serverPidfilePath(&config.Config{}), filepath.Join(rd, pidfileName); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("env unset", func(t *testing.T) {
		t.Setenv("RUNTIME_DIRECTORY", "")
		if got, want := serverPidfilePath(&config.Config{}), filepath.Join(dataDir, pidfileName); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("config data_dir", func(t *testing.T) {
		t.Setenv("RUNTIME_DIRECTORY", "")
		t.Setenv(datadir.EnvVar, "")
		cfgDir := t.TempDir()
		if got, want := serverPidfilePath(&config.Config{DataDir: cfgDir}), filepath.Join(cfgDir, pidfileName); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("explicit flag", func(t *testing.T) {
		t.Setenv("RUNTIME_DIRECTORY", t.TempDir())
		flagPath := filepath.Join(t.TempDir(), "custom.pid")
		withPidfileFlag(t, flagPath)
		if got := serverPidfilePath(&config.Config{}); got != flagPath {
			t.Errorf("got %q, want %q", got, flagPath)
		}
		if got := cliPidfileCandidates(); !reflect.DeepEqual(got, []string{flagPath}) {
			t.Errorf("CLI candidates with --pidfile = %q, want only %q", got, flagPath)
		}
	})
}

// The CLI (no $RUNTIME_DIRECTORY) finds the pidfile a systemd-run gateway
// wrote under /run/conduit, and a legacy /tmp one from an older binary.
func TestFindPidfileFromCLI(t *testing.T) {
	run, legacy := withPidfileLocations(t)
	t.Setenv("RUNTIME_DIRECTORY", "")
	t.Setenv(datadir.EnvVar, t.TempDir())
	origCfg := cfgFile
	cfgFile = filepath.Join(t.TempDir(), "missing.json")
	t.Cleanup(func() { cfgFile = origCfg })

	writePID(t, legacy, os.Getpid())
	if got := findPidfile(); got != legacy {
		t.Errorf("legacy only: got %q, want %q", got, legacy)
	}
	if _, err := os.Stat(cfgFile); err == nil {
		t.Error("pidfile lookup created a default config file")
	}

	writePID(t, run, os.Getpid())
	if got := findPidfile(); got != run {
		t.Errorf("run + legacy: got %q, want %q", got, run)
	}

	pid, path, err := readGatewayPidfile()
	if err != nil || pid != os.Getpid() || path != run {
		t.Errorf("readGatewayPidfile() = %d, %q, %v", pid, path, err)
	}
}

func TestReadGatewayPidfileNotFound(t *testing.T) {
	withPidfileLocations(t)
	t.Setenv("RUNTIME_DIRECTORY", "")
	t.Setenv(datadir.EnvVar, t.TempDir())
	origCfg := cfgFile
	cfgFile = ""
	t.Cleanup(func() { cfgFile = origCfg })

	if _, _, err := readGatewayPidfile(); err == nil {
		t.Fatal("expected error when no pidfile exists")
	}
}

func TestWritePidfileMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", pidfileName)
	if err := writePidfile(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}
	if pid, err := readPidfile(p); err != nil || pid != os.Getpid() {
		t.Errorf("readPidfile = %d, %v", pid, err)
	}
}
