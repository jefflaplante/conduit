// Package eval implements the Conduit evaluation harness (bd-18u).
//
// It loads eval corpora (YAML rubric + JSONL turn transcripts), replays them
// through a ReplayFunc, applies deterministic checks, and writes JSONL and
// markdown reports. v0.1 is replay-only: assistant turns are pre-scripted in
// the corpus and checks validate the scripted transcript — zero cost, zero
// side effects. Live replay against a real model is a later task; the
// ReplayFunc interface here is its seam.
package eval

// Version of the eval corpus format. Bump when the format changes
// incompatibly; the loader rejects other versions.
const CorpusVersion = 1

// DefaultCorpusDir is the repo-relative location of the synthetic corpus.
const DefaultCorpusDir = "internal/eval/testdata"

// ToolCall is one tool invocation inside a transcript turn.
type ToolCall struct {
	// Name is the tool name as invoked.
	Name string `yaml:"name" json:"name"`
	// ArgsJSON is the raw JSON of the tool arguments, used for fragment
	// matching (args_substr). Empty when unknown.
	ArgsJSON string `yaml:"args_json,omitempty" json:"args_json,omitempty"`
}

// Turn is a single row of a case transcript.
type Turn struct {
	// Num is the 1-based conversation turn the row belongs to. A turn
	// typically spans a user row plus one or more assistant rows.
	Num int `yaml:"turn" json:"turn"`
	// Role is "user", "assistant", or "system".
	Role string `yaml:"role" json:"role"`
	// Content is the message text.
	Content string `yaml:"content" json:"content"`
	// Tools records the tool calls made in this row (assistant rows only).
	Tools []ToolCall `yaml:"tools,omitempty" json:"tools,omitempty"`
	// ToolsExpected are fragments this row must exhibit (replay assertions).
	ToolsExpected []ToolFragment `yaml:"tools_expected,omitempty" json:"tools_expected,omitempty"`
	// Allow restricts the tool names permitted in this row's turn.
	Allow []string `yaml:"allow,omitempty" json:"allow,omitempty"`

	// PromptTokens and CompletionTokens are scripted token counts for cost
	// accounting (design doc: cost formula on scripted token counts).
	PromptTokens     int64 `yaml:"prompt_tokens,omitempty" json:"prompt_tokens,omitempty"`
	CompletionTokens int64 `yaml:"completion_tokens,omitempty" json:"completion_tokens,omitempty"`
}

// ToolFragment is a tools_expected entry: the tool name must match and each
// listed substring must appear in the invoked args JSON.
type ToolFragment struct {
	Name       string   `yaml:"name" json:"name"`
	ArgsSubstr []string `yaml:"args_substr,omitempty" json:"args_substr,omitempty"`
}

// RubricItem is one rubric line. Deterministic items map to the built-in
// checks; llm_judge items are scored by a judge model in live mode (v0.2),
// so a replay run simply carries them as unjudged.
type RubricItem struct {
	ID     string  `yaml:"id"`
	Type   string  `yaml:"type"` // "deterministic" or "llm_judge"
	Check  string  `yaml:"check"`
	Detail string  `yaml:"detail,omitempty"`
	Weight float64 `yaml:"weight,omitempty"`
}

// Case is one eval case: metadata + rubric (YAML frontmatter) + turns
// (fenced JSONL body).
type Case struct {
	ID              string       `json:"id"`
	Title           string       `json:"title"`
	Family          string       `json:"family,omitempty"`
	Tags            []string     `json:"tags,omitempty"`
	ModelsUnderTest []string     `yaml:"models_under_test,omitempty" json:"models_under_test,omitempty"`
	Mode            string       `json:"mode,omitempty"` // "replay" (default) or "live" (v0.2)
	Rubric          []RubricItem `json:"rubric,omitempty"`
	Turns           []Turn       `json:"turns"`
	// Source is the file the case was loaded from (diagnostics only).
	Source string `json:"source,omitempty"`
}

// normalizeCase fills defaults: Mode "replay", rubric item weights 1.
func normalizeCase(c *Case) {
	if c.Mode == "" {
		c.Mode = "replay"
	}
	for i := range c.Rubric {
		if c.Rubric[i].Weight == 0 {
			c.Rubric[i].Weight = 1
		}
	}
}
