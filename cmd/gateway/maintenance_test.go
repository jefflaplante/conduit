package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// captureMaintenanceStdout runs fn with os.Stdout redirected.
func captureMaintenanceStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = old
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("command failed: %v", runErr)
	}
	return string(out)
}

// conduit-3kgo: status no longer claims a scheduler or a schedule exists.
func TestMaintenanceStatus_Honest(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		maintenanceJSONOutput = jsonOut
		out := captureMaintenanceStdout(t, func() error { return showMaintenanceStatus(nil, nil) })
		for _, bad := range []string{"running scheduler", "Schedule", "schedule\""} {
			if strings.Contains(out, bad) {
				t.Errorf("json=%v: status output mentions %q:\n%s", jsonOut, bad, out)
			}
		}
		if !strings.Contains(out, "no background schedule") {
			t.Errorf("json=%v: status output lacks the no-schedule note:\n%s", jsonOut, out)
		}
		if jsonOut {
			var v map[string]interface{}
			if err := json.Unmarshal([]byte(out), &v); err != nil {
				t.Errorf("status --json is not JSON: %v", err)
			}
		}
	}
	maintenanceJSONOutput = false

	out := captureMaintenanceStdout(t, func() error { return showMaintenanceConfig(nil, nil) })
	if strings.Contains(out, "Maintenance Window") || strings.Contains(out, "Schedule") {
		t.Errorf("config output still shows window/schedule:\n%s", out)
	}
}
