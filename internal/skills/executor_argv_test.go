package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var argvActions = []string{"status", "note", "show", "list", "run"}

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
			name:   "scalars become --key=value in sorted key order",
			action: "note",
			args:   map[string]interface{}{"minutes": 45, "text": "Back at 3"},
			want:   []string{"note", "--minutes=45", "--text=Back at 3"},
		},
		{
			name:   "bool true becomes bare flag",
			action: "show",
			args:   map[string]interface{}{"dither": true, "image": "chart.png"},
			want:   []string{"show", "--dither", "--image=chart.png"},
		},
		{
			name:   "bool false is omitted",
			action: "show",
			args:   map[string]interface{}{"dither": false, "image": "photo.jpg"},
			want:   []string{"show", "--image=photo.jpg"},
		},
		{
			name:   "leading dashes on keys are dropped",
			action: "show",
			args:   map[string]interface{}{"--dither": true, "-fit": "cover"},
			want:   []string{"show", "--dither", "--fit=cover"},
		},
		{
			name:   "a value starting with a dash stays a value",
			action: "note",
			args:   map[string]interface{}{"text": "--attach"},
			want:   []string{"note", "--text=--attach"},
		},
		{
			name:   "slice repeats --key=word",
			action: "list",
			args:   map[string]interface{}{"tag": []string{"a", "b"}},
			want:   []string{"list", "--tag=a", "--tag=b"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildScriptArgv(argvActions, tc.action, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}

// The model must not be able to reach options a skill never exposed, such
// as ics-email's --attach (action "--as-file" plus an "--attach" key used
// to email any file).
func TestBuildScriptArgvRejectsInjection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		args   map[string]interface{}
	}{
		{"undeclared action", "send", nil},
		{"option as action", "--as-file", nil},
		{"key carrying a value", "note", map[string]interface{}{"attach=/etc/passwd": true}},
		{"key with a space", "note", map[string]interface{}{"to x": "y"}},
		{"empty key", "note", map[string]interface{}{"--": "x"}},
	} {
		if argv, err := buildScriptArgv(argvActions, tc.action, tc.args); err == nil {
			t.Errorf("%s: want error, got argv %q", tc.name, argv)
		}
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
		Actions:  []string{"status", "note"},
		Argv:     true,
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

	// Args map onto --flag=value.
	res, err = e.ExecuteSkill(context.Background(), skill, "note", map[string]interface{}{"--minutes": "15", "text": "hello world"})
	if err != nil {
		t.Fatalf("Execute(note) error: %v", err)
	}
	if !res.Success || !strings.Contains(res.Output, "--minutes=15") || !strings.Contains(res.Output, "--text=hello world") {
		t.Fatalf("note args not forwarded: %+v", res)
	}
}

// JSON numbers decode as float64; large integers must not reach the script
// as 1.7e+09, and objects go as JSON, not Go map syntax.
func TestBuildScriptArgvValueFormatting(t *testing.T) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(`{"since":1700000000,"id":1234567,"ratio":0.25,"opts":{"a":1},"ids":[10000000,2],"none":null}`), &args); err != nil {
		t.Fatal(err)
	}
	argv, err := buildScriptArgv(argvActions, "run", args)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv, " ")
	want := `run --id=1234567 --ids=10000000 --ids=2 --opts={"a":1} --ratio=0.25 --since=1700000000`
	if got != want {
		t.Fatalf("argv = %q\nwant   %q", got, want)
	}
}

// Without "argv: true" a script gets no argv, only the stdin envelope:
// envelope readers such as water-tank collect.py take argv[1] as data.
func TestExecuteScriptNoArgvWithoutOptIn(t *testing.T) {
	dir := t.TempDir()
	src := "import sys, json\nprint('argv:', sys.argv[1:], 'action:', json.load(sys.stdin)['action'])\n"
	if err := os.WriteFile(filepath.Join(dir, "demo.py"), []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	skill := Skill{
		Name:     "demo",
		Location: dir,
		Scripts:  []SkillScript{{Name: "demo", Path: "demo.py", Language: "python"}},
	}
	res, err := e.ExecuteSkill(context.Background(), skill, "--as-file", map[string]interface{}{"--attach": "/etc/passwd"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || !strings.Contains(res.Output, "argv: [] action: --as-file") {
		t.Fatalf("want empty argv and the action in the envelope, got %+v", res)
	}
}

func TestLoaderParsesArgvOptIn(t *testing.T) {
	skill, err := NewSkillLoader().LoadSkillFromContent("---\nname: demo\ndescription: d\nargv: true\nactions:\n  - status\n---\nbody\n")
	if err != nil {
		t.Fatal(err)
	}
	if !skill.Argv || !slices.Contains(skill.Actions, "status") {
		t.Fatalf("argv=%v actions=%v, want argv with status declared", skill.Argv, skill.Actions)
	}
}
