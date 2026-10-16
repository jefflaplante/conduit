package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildScriptArgv covers the argv forwarding for script skills (bd-3439).
func TestBuildScriptArgv(t *testing.T) {
	tests := []struct {
		name   string
		action string
		args   map[string]interface{}
		want   []string
	}{
		{
			name:   "action only",
			action: "status",
			want:   []string{"status"},
		},
		{
			name:   "scalars become --key value in sorted key order",
			action: "note",
			args:   map[string]interface{}{"minutes": 45, "text": "Back at 3"},
			want:   []string{"note", "--minutes", "45", "--text", "Back at 3"},
		},
		{
			name:   "bool true becomes bare flag",
			action: "show",
			args:   map[string]interface{}{"dither": true, "image": "chart.png"},
			want:   []string{"show", "--dither", "--image", "chart.png"},
		},
		{
			name:   "bool false is omitted",
			action: "show",
			args:   map[string]interface{}{"dither": false, "image": "photo.jpg"},
			want:   []string{"show", "--image", "photo.jpg"},
		},
		{
			name:   "explicit --key passes verbatim",
			action: "show",
			args:   map[string]interface{}{"--fit": "cover", "--dither": true},
			want:   []string{"show", "--dither", "--fit", "cover"},
		},
		{
			name:   "slice repeats --key word",
			action: "list",
			args:   map[string]interface{}{"tag": []string{"a", "b"}},
			want:   []string{"list", "--tag", "a", "--tag", "b"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildScriptArgv(tc.action, tc.args)
			if len(got) != len(tc.want) {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("argv = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

// TestExecuteScriptForwardsArgv proves the bd-3439 fix end to end: an
// argparse-style script (like trmnl.py) exits 2 with empty argv, and the
// executor now forwards the action as argv so it succeeds.
func TestExecuteScriptForwardsArgv(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "demo.py")
	src := `import sys
if len(sys.argv) < 2:
    sys.stderr.write("arguments are required: cmd\n")
    sys.exit(2)
if sys.argv[1] == "status":
    print("pin: none")
    sys.exit(0)
print("ok:", " ".join(sys.argv[1:]))
`
	if err := os.WriteFile(script, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}

	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	skill := Skill{
		Name:     "demo",
		Location: dir,
		Scripts: []SkillScript{{
			Name:     "demo",
			Path:     "demo.py",
			Language: "python",
		}},
	}

	res, err := e.ExecuteSkill(context.Background(), skill, "status", nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("status with forwarded argv failed: %+v", res)
	}
	if !strings.Contains(res.Output, "pin: none") {
		t.Fatalf("unexpected output: %q", res.Output)
	}

	// Args map onto --flag value pairs.
	res, err = e.ExecuteSkill(context.Background(), skill, "note", map[string]interface{}{"--minutes": "15", "text": "hello world"})
	if err != nil {
		t.Fatalf("Execute(note) error: %v", err)
	}
	if !res.Success || !strings.Contains(res.Output, "--minutes 15") || !strings.Contains(res.Output, "--text hello world") {
		t.Fatalf("note args not forwarded: %+v", res)
	}
}
