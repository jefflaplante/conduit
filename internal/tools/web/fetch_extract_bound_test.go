package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H8: markdown extraction re-emitted every nested element's whole subtree,
// so output grew with nesting depth (100 KB of HTML -> 40 MB), and
// maxChars was applied only afterwards. maxChars < 0 panicked.

// nestedHTML wraps a short text in depth levels of each tag, cycling, with
// a word of text at every level (so re-emitting subtrees is quadratic).
func nestedHTML(depth int, tags ...string) string {
	var b strings.Builder
	b.WriteString("<html><body>")
	for i := 0; i < depth; i++ {
		b.WriteString("<" + tags[i%len(tags)] + ">lvl ")
	}
	b.WriteString("deep text")
	for i := depth - 1; i >= 0; i-- {
		b.WriteString("</" + tags[i%len(tags)] + ">")
	}
	b.WriteString("</body></html>")
	return b.String()
}

func TestExtractMarkdown_NestedFormattingIsLinear(t *testing.T) {
	tool := &WebFetchTool{}
	cases := map[string][]string{
		"strong":     {"strong"},
		"blockquote": {"blockquote"},
		"em":         {"em"},
		"mixed":      {"strong", "blockquote", "em", "b", "i", "code"},
		"p-in-li":    {"li", "p"},
	}
	for name, tags := range cases {
		t.Run(name, func(t *testing.T) {
			html := nestedHTML(500, tags...) // x/net/html caps the open-element stack at 512
			out, err := tool.extractFromHTML(html, "markdown")
			require.NoError(t, err)
			assert.Equal(t, 1, strings.Count(out, "deep text"), "each text node is emitted once")
			assert.Equal(t, 500, strings.Count(out, "lvl"), "each level's text is emitted once")
			assert.Less(t, len(out), len(html), "output must be linear in the input, not depth x input")
		})
	}
}

func TestExtractMarkdown_RepeatedNestedSiblingsLinear(t *testing.T) {
	// The review's shape: many siblings, each a nested formatting stack.
	tool := &WebFetchTool{}
	var b strings.Builder
	b.WriteString("<html><body>")
	for i := 0; i < 500; i++ {
		b.WriteString("<blockquote><strong><em><b><i>word</i></b></em></strong></blockquote>")
	}
	b.WriteString("</body></html>")
	html := b.String()

	out, err := tool.extractFromHTML(html, "markdown")
	require.NoError(t, err)
	assert.Equal(t, 500, strings.Count(out, "word"))
	assert.Less(t, len(out), len(html))
}

func TestExtractMarkdown_CappedDuringExtraction(t *testing.T) {
	tool := &WebFetchTool{}
	var b strings.Builder
	b.WriteString("<html><body>")
	for i := 0; i < 20000; i++ {
		b.WriteString("<p>paragraph text that repeats</p>")
	}
	b.WriteString("</body></html>")

	out, hitCap, err := tool.extractFromHTMLCapped(b.String(), "markdown", 1000)
	require.NoError(t, err)
	assert.True(t, hitCap)
	assert.LessOrEqual(t, len(out), extractionCap(1000))
}

func TestExtractMarkdown_NormalPageSubstance(t *testing.T) {
	tool := &WebFetchTool{}
	html := `<html><head><title>Doc</title></head><body><main>
<h1>Intro</h1>
<p>Visit <a href="https://example.com">Example</a> and read <strong>this</strong>.</p>
<ul><li>One</li><li><a href="/two">Two</a></li></ul>
<blockquote><p>Quoted</p></blockquote>
<p>Use <code>go test</code> here.</p>
</main></body></html>`
	out, err := tool.extractFromHTML(html, "markdown")
	require.NoError(t, err)
	for _, want := range []string{
		"# Doc", "# Intro", "[Example](https://example.com)", "**this**",
		"- One", "- [Two](/two)", "> Quoted", "`go test`",
	} {
		assert.Contains(t, out, want)
	}
	// Each text appears once (no parent + child duplication).
	assert.Equal(t, 1, strings.Count(out, "Quoted"))
	assert.Equal(t, 1, strings.Count(out, "Example"))
	assert.Equal(t, 1, strings.Count(out, "this"))
}

func TestWebFetch_NegativeMaxCharsDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("some body text"))
	}))
	defer srv.Close()

	tool := newLoopbackFetchTool()
	for _, mc := range []interface{}{-1, float64(-100)} {
		res, err := tool.Execute(context.Background(), map[string]interface{}{"url": srv.URL, "maxChars": mc})
		require.NoError(t, err)
		require.True(t, res.Success, res.Error)
		assert.NotContains(t, res.Content, "some body text", "maxChars clamps to 0")
		assert.Equal(t, true, res.Data["truncated"])
		assert.Equal(t, 0, res.Data["maxChars"])
	}
}
