package web

import (
	"strings"
	"testing"
)

func TestCleanContent_PreservesNewlines(t *testing.T) {
	tool := &WebFetchTool{}

	input := "Line one\nLine two\nLine three"
	result := tool.cleanContent(input)

	if !strings.Contains(result, "\n") {
		t.Errorf("cleanContent destroyed newlines: got %q", result)
	}

	lines := strings.Split(result, "\n")
	if len(lines) < 3 {
		t.Errorf("expected at least 3 lines, got %d: %q", len(lines), result)
	}
}

func TestCleanContent_CollapsesExcessiveNewlines(t *testing.T) {
	tool := &WebFetchTool{}

	input := "Line one\n\n\n\n\nLine two"
	result := tool.cleanContent(input)

	// Should collapse 5 newlines down to 2
	if strings.Contains(result, "\n\n\n") {
		t.Errorf("cleanContent did not collapse excessive newlines: got %q", result)
	}

	if !strings.Contains(result, "\n\n") {
		t.Errorf("cleanContent collapsed too aggressively: got %q", result)
	}
}

func TestCleanContent_CollapsesHorizontalWhitespace(t *testing.T) {
	tool := &WebFetchTool{}

	input := "word1    word2\t\tword3"
	result := tool.cleanContent(input)

	if strings.Contains(result, "    ") {
		t.Errorf("cleanContent did not collapse spaces: got %q", result)
	}

	if strings.Contains(result, "\t") {
		t.Errorf("cleanContent did not collapse tabs: got %q", result)
	}
}

func TestCleanContent_MarkdownStructure(t *testing.T) {
	tool := &WebFetchTool{}

	input := "# Title\n\nParagraph one.\n\nParagraph two.\n\n- Item 1\n- Item 2"
	result := tool.cleanContent(input)

	if !strings.Contains(result, "# Title") {
		t.Errorf("cleanContent destroyed heading: got %q", result)
	}

	if !strings.Contains(result, "Paragraph one.") {
		t.Errorf("cleanContent destroyed paragraph: got %q", result)
	}

	if !strings.Contains(result, "- Item 1\n- Item 2") {
		t.Errorf("cleanContent destroyed list items: got %q", result)
	}
}
