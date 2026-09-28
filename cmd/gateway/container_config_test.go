package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"conduit/internal/config"
)

// The image's default config must agree with the Containerfile: EXPOSE and
// HEALTHCHECK port, writable /data for the DB and data_dir, /workspace for
// the sandbox, and it must pass config validation (conduit-utxe).
func TestContainerDefaultConfigMatchesContainerfile(t *testing.T) {
	const cfgPath = "../../configs/container/conduit.json"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	// config.Load writes a default when the file is missing; guard first.
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("container config missing: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("container config does not load/validate: %v", err)
	}

	cf, err := os.ReadFile("../../Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	containerfile := string(cf)

	m := regexp.MustCompile(`(?m)^EXPOSE\s+(\d+)`).FindStringSubmatch(containerfile)
	if m == nil {
		t.Fatal("no EXPOSE in Containerfile")
	}
	if exposed, _ := strconv.Atoi(m[1]); cfg.Port != exposed {
		t.Errorf("config port %d != EXPOSE %d", cfg.Port, exposed)
	}
	if !strings.Contains(containerfile, "localhost:"+strconv.Itoa(cfg.Port)+"/health") {
		t.Errorf("HEALTHCHECK does not probe port %d", cfg.Port)
	}
	if !strings.Contains(containerfile, "COPY configs/container/conduit.json /etc/conduit/config.json") {
		t.Error("Containerfile does not install configs/container/conduit.json as the default config")
	}
	if !strings.Contains(containerfile, `VOLUME ["/data", "/workspace"]`) {
		t.Error("Containerfile volumes changed; update this test and the config together")
	}

	under := func(p, dir string) bool {
		return filepath.IsAbs(p) && (p == dir || strings.HasPrefix(p, dir+"/"))
	}
	if cfg.DataDir != "/data" {
		t.Errorf("data_dir = %q, want /data", cfg.DataDir)
	}
	if !under(cfg.Database.Path, "/data") {
		t.Errorf("database.path %q is not under the /data volume", cfg.Database.Path)
	}
	if cfg.Tools.Sandbox.WorkspaceDir != "/workspace" {
		t.Errorf("tools.sandbox.workspace_dir = %q, want /workspace", cfg.Tools.Sandbox.WorkspaceDir)
	}
	if cfg.Workspace.ContextDir != "/workspace" {
		t.Errorf("workspace.context_dir = %q, want /workspace", cfg.Workspace.ContextDir)
	}
}
