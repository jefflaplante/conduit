package eval

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// smoke unmarshal test: keeps the eval_test.go helper file honest.
func TestMustUnmarshalHelpers(t *testing.T) {
	c := mustUnmarshal(t, "id: demo\ntitle: Demo\n")
	if c.ID != "demo" || c.Title != "Demo" || c.Mode != "replay" {
		t.Fatalf("unexpected case: %+v", c)
	}
	turns := mustUnmarshalTurns(t, `{"turn":1,"role":"user","content":"hi"}`+"\n")
	if len(turns) != 1 || turns[0].Role != "user" {
		t.Fatalf("unexpected turns: %+v", turns)
	}
}

// mustUnmarshal decodes a frontmatter-style YAML fragment.
func mustUnmarshal(t *testing.T, s string) *Case {
	t.Helper()
	var c Case
	if err := yaml.Unmarshal([]byte(s), &c); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}
	normalizeCase(&c)
	return &c
}

// mustUnmarshalTurns decodes JSONL transcript rows.
func mustUnmarshalTurns(t *testing.T, s string) []Turn {
	t.Helper()
	turns, err := ParseTurns([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return turns
}
