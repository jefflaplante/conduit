package nli

import (
	"testing"
)

func TestNewIntentParser(t *testing.T) {
	p := NewIntentParser()
	if p == nil {
		t.Fatal("NewIntentParser returned nil")
	}
	if len(p.actionKeywords) == 0 {
		t.Error("action keywords not initialized")
	}
	if len(p.entityPatterns) == 0 {
		t.Error("entity patterns not initialized")
	}
	if len(p.toolNames) == 0 {
		t.Error("tool names not initialized")
	}
}

func TestParse_EmptyMessage(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("")

	if intent == nil {
		t.Fatal("Parse returned nil for empty message")
	}
	if intent.Confidence != 0 {
		t.Errorf("expected confidence 0 for empty message, got %f", intent.Confidence)
	}
	if intent.Action != ActionQuery {
		t.Errorf("expected default action query, got %s", intent.Action)
	}
}

func TestParse_SimpleQuery(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("search for Go concurrency patterns")

	if intent.Action != ActionQuery {
		t.Errorf("expected query action, got %s", intent.Action)
	}
	if intent.Confidence <= 0 {
		t.Error("expected positive confidence for keyword match")
	}
}

func TestParse_CreateAction(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("create a new file at /tmp/test.go")

	if intent.Action != ActionCreate {
		t.Errorf("expected create action, got %s", intent.Action)
	}
	if intent.Confidence <= 0 {
		t.Error("expected positive confidence")
	}
	// Should detect a file path entity.
	hasFilePath := false
	for _, e := range intent.Entities {
		if e.Type == EntityFilePath {
			hasFilePath = true
			if e.Value != "/tmp/test.go" {
				t.Errorf("expected file path /tmp/test.go, got %s", e.Value)
			}
		}
	}
	if !hasFilePath {
		t.Error("expected a file path entity")
	}
}

func TestParse_DeleteAction(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("delete the session data")

	if intent.Action != ActionDelete {
		t.Errorf("expected delete action, got %s", intent.Action)
	}
}

func TestParse_ModifyAction(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("update the configuration settings")

	if intent.Action != ActionModify {
		t.Errorf("expected modify action, got %s", intent.Action)
	}
}

func TestParse_CommandAction(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("restart the gateway")

	if intent.Action != ActionCommand {
		t.Errorf("expected command action, got %s", intent.Action)
	}
}

func TestParse_QuestionMark(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("what is the current time?")

	if intent.Action != ActionQuery {
		t.Errorf("expected query action for question, got %s", intent.Action)
	}
	if intent.Confidence < 0.5 {
		t.Errorf("expected confidence >= 0.5 for question with keyword 'what', got %f", intent.Confidence)
	}
}

func TestParse_URLEntity(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("fetch the page at https://example.com/api/data")

	hasURL := false
	for _, e := range intent.Entities {
		if e.Type == EntityURL && e.Value == "https://example.com/api/data" {
			hasURL = true
		}
	}
	if !hasURL {
		t.Error("expected URL entity to be extracted")
	}
	if _, ok := intent.Parameters["url"]; !ok {
		t.Error("expected url parameter")
	}
}

func TestParse_ModelNameEntity(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("use claude-opus-4 for this task")

	hasModel := false
	for _, e := range intent.Entities {
		if e.Type == EntityModelName {
			hasModel = true
		}
	}
	if !hasModel {
		t.Error("expected model name entity")
	}
}

func TestParse_SessionEntity(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("check session: abc-123")

	hasSession := false
	for _, e := range intent.Entities {
		if e.Type == EntitySession && e.Value == "abc-123" {
			hasSession = true
		}
	}
	if !hasSession {
		t.Error("expected session entity")
	}
}

func TestParse_ToolNameEntity(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("run the web_search tool with query test")

	hasToolName := false
	for _, e := range intent.Entities {
		if e.Type == EntityToolName && e.Value == "web_search" {
			hasToolName = true
		}
	}
	if !hasToolName {
		t.Error("expected tool name entity")
	}
}

func TestParse_QuotedStrings(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse(`search for "golang concurrency" in the docs`)

	if q, ok := intent.Parameters["query"]; ok {
		if q != "golang concurrency" {
			t.Errorf("expected query 'golang concurrency', got '%s'", q)
		}
	} else {
		t.Error("expected query parameter from quoted string")
	}
}

func TestParse_MultipleQuotedStrings(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse(`compare "file A" and "file B"`)

	queries, ok := intent.Parameters["queries"]
	if !ok {
		t.Fatal("expected queries parameter for multiple quoted strings")
	}
	qs, ok := queries.([]string)
	if !ok {
		t.Fatal("queries parameter should be []string")
	}
	if len(qs) != 2 {
		t.Errorf("expected 2 quoted values, got %d", len(qs))
	}
}

func TestParseMultiStep_EmptyMessage(t *testing.T) {
	p := NewIntentParser()
	intents := p.ParseMultiStep("")

	if intents != nil {
		t.Errorf("expected nil for empty message, got %d intents", len(intents))
	}
}

func TestParseMultiStep_SingleStep(t *testing.T) {
	p := NewIntentParser()
	intents := p.ParseMultiStep("search for Go tutorials")

	if len(intents) != 1 {
		t.Errorf("expected 1 intent for single step, got %d", len(intents))
	}
}

func TestParseMultiStep_SemicolonSplit(t *testing.T) {
	p := NewIntentParser()
	intents := p.ParseMultiStep("search for Go tutorials; fetch the first result; save it to a file")

	if len(intents) != 3 {
		t.Errorf("expected 3 intents from semicolon split, got %d", len(intents))
	}
}

func TestParseMultiStep_NumberedList(t *testing.T) {
	p := NewIntentParser()
	msg := "1. search for Go patterns 2. fetch the top result 3. save to /tmp/result.txt"
	intents := p.ParseMultiStep(msg)

	if len(intents) < 2 {
		t.Errorf("expected at least 2 intents from numbered list, got %d", len(intents))
	}
}

func TestParseMultiStep_SequentialKeywords(t *testing.T) {
	p := NewIntentParser()
	msg := "first search for the latest news, then fetch the top article, finally summarize it"
	intents := p.ParseMultiStep(msg)

	if len(intents) < 2 {
		t.Errorf("expected at least 2 intents from sequential keywords, got %d", len(intents))
	}
}

func TestParseMultiStep_ConjunctionSplit(t *testing.T) {
	p := NewIntentParser()
	msg := "search the web for recipes and also check my memory for cooking tips"
	intents := p.ParseMultiStep(msg)

	if len(intents) < 2 {
		t.Errorf("expected at least 2 intents from conjunction split, got %d", len(intents))
	}
}

func TestExtractEntities_FilePathVariants(t *testing.T) {
	p := NewIntentParser()

	tests := []struct {
		input    string
		expected string
	}{
		{"read /etc/hosts", "/etc/hosts"},
		{"edit ./config.json", "./config.json"},
		{"check ../parent/file.txt", "../parent/file.txt"},
	}

	for _, tc := range tests {
		entities := p.extractEntities(tc.input)
		found := false
		for _, e := range entities {
			if e.Type == EntityFilePath && e.Value == tc.expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected file path %s from input %q", tc.expected, tc.input)
		}
	}
}

func TestExtractEntities_ChannelReference(t *testing.T) {
	p := NewIntentParser()
	entities := p.extractEntities("send a message to #general")

	found := false
	for _, e := range entities {
		if e.Type == EntityChannel && e.Value == "general" {
			found = true
		}
	}
	if !found {
		t.Error("expected channel entity 'general'")
	}
}

func TestParse_TargetInference(t *testing.T) {
	p := NewIntentParser()
	intent := p.Parse("search for weather data")

	if intent.Target == "" {
		t.Error("expected a non-empty target")
	}
}

func TestParse_ConfidenceBounds(t *testing.T) {
	p := NewIntentParser()
	tests := []string{
		"hello",
		"search for things",
		"create a new file",
		"delete everything",
		"update the config and restart the server",
	}

	for _, msg := range tests {
		intent := p.Parse(msg)
		if intent.Confidence < 0 || intent.Confidence > 1 {
			t.Errorf("confidence %f out of bounds for %q", intent.Confidence, msg)
		}
	}
}

func TestKeywordScore(t *testing.T) {
	p := NewIntentParser()

	// Keyword at start of message should score higher.
	scoreStart := p.keywordScore("search for something", "search", 0)
	scoreMiddle := p.keywordScore("please search for something important in the database", "search", 7)

	if scoreStart <= scoreMiddle {
		t.Errorf("expected keyword at start to score higher: start=%f, middle=%f", scoreStart, scoreMiddle)
	}
}

func TestIsAlpha(t *testing.T) {
	if !isAlpha('a') {
		t.Error("expected 'a' to be alpha")
	}
	if !isAlpha('Z') {
		t.Error("expected 'Z' to be alpha")
	}
	if isAlpha('1') {
		t.Error("expected '1' not to be alpha")
	}
	if isAlpha(' ') {
		t.Error("expected space not to be alpha")
	}
}
