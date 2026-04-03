package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWebFetchTool_NilServices(t *testing.T) {
	tool := NewWebFetchTool(nil)
	require.NotNil(t, tool)
	assert.NotNil(t, tool.httpClient, "should create default HTTP client when services is nil")
}

func TestNewWebFetchTool_WithServices(t *testing.T) {
	customClient := &http.Client{Timeout: 15 * time.Second}
	services := &types.ToolServices{
		WebClient: customClient,
	}

	tool := NewWebFetchTool(services)
	require.NotNil(t, tool)
	assert.Equal(t, customClient, tool.httpClient, "should use provided HTTP client")
}

func TestNewWebFetchTool_ServicesNoClient(t *testing.T) {
	services := &types.ToolServices{
		WebClient: nil,
	}

	tool := NewWebFetchTool(services)
	require.NotNil(t, tool)
	assert.NotNil(t, tool.httpClient, "should create default client when services.WebClient is nil")
}

func TestWebFetchTool_Name(t *testing.T) {
	tool := NewWebFetchTool(nil)
	assert.Equal(t, "WebFetch", tool.Name())
}

func TestWebFetchTool_Description(t *testing.T) {
	tool := NewWebFetchTool(nil)
	desc := tool.Description()
	assert.Contains(t, strings.ToLower(desc), "fetch")
	assert.Contains(t, desc, "URL")
}

func TestWebFetchTool_Parameters(t *testing.T) {
	tool := NewWebFetchTool(nil)
	params := tool.Parameters()

	require.NotNil(t, params)
	assert.Equal(t, "object", params["type"])

	props, ok := params["properties"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, props, "url")
	assert.Contains(t, props, "extractMode")
	assert.Contains(t, props, "maxChars")

	required, ok := params["required"].([]string)
	require.True(t, ok)
	assert.Contains(t, required, "url")
}

func TestWebFetchTool_Execute_MissingURL(t *testing.T) {
	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "url parameter is required")
}

func TestWebFetchTool_Execute_InvalidURLType(t *testing.T) {
	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": 12345,
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "url parameter is required")
}

func TestWebFetchTool_Execute_InvalidURLFormat(t *testing.T) {
	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": "://invalid-url",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "invalid URL")
}

func TestWebFetchTool_Execute_UnsupportedScheme(t *testing.T) {
	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": "ftp://example.com/file.txt",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "only HTTP and HTTPS")
}

func TestWebFetchTool_Execute_UnsupportedSchemeFile(t *testing.T) {
	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": "file:///etc/passwd",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "only HTTP and HTTPS")
}

func TestWebFetchTool_Execute_HTMLContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<h1>Hello World</h1>
<p>This is a test paragraph.</p>
</body>
</html>`)
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": server.URL,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Test Page")
	assert.Contains(t, result.Content, "Hello World")
	assert.Contains(t, result.Content, "test paragraph")
}

func TestWebFetchTool_Execute_PlainTextContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "Plain text content here.")
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": server.URL,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Plain text content here.")
}

func TestWebFetchTool_Execute_UnsupportedContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte{0x00, 0x01, 0x02})
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": server.URL,
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "unsupported content type")
}

func TestWebFetchTool_Execute_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": server.URL,
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "HTTP error 404")
}

func TestWebFetchTool_Execute_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": server.URL,
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "HTTP error 500")
}

func TestWebFetchTool_Execute_Truncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		// Write content longer than maxChars
		io.WriteString(w, strings.Repeat("A", 1000))
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url":      server.URL,
		"maxChars": 100,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "[Content truncated...]")
	assert.True(t, result.Data["truncated"].(bool))
}

func TestWebFetchTool_Execute_TextExtractMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `<html><head><title>Title</title></head><body><h1>Heading</h1><p>Paragraph</p></body></html>`)
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url":         server.URL,
		"extractMode": "text",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Heading")
	assert.Contains(t, result.Content, "Paragraph")
	assert.Equal(t, "text", result.Data["extractMode"])
}

func TestWebFetchTool_Execute_ResultData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "test content")
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url":         server.URL,
		"maxChars":    50000,
		"extractMode": "markdown",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, server.URL, result.Data["url"])
	assert.Equal(t, "markdown", result.Data["extractMode"])
	assert.Equal(t, 50000, result.Data["maxChars"])
	assert.IsType(t, 0, result.Data["length"])
}

func TestWebFetchTool_Execute_ConnectionRefused(t *testing.T) {
	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": "http://127.0.0.1:59999",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "failed to fetch content")
}

func TestWebFetchTool_Execute_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := tool.Execute(ctx, map[string]interface{}{
		"url": server.URL,
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "failed to fetch content")
}

func TestCleanContent_PreservesNewlines(t *testing.T) {
	tool := &WebFetchTool{}

	input := "Line one\nLine two\nLine three"
	result := tool.cleanContent(input)

	assert.Contains(t, result, "\n")

	lines := strings.Split(result, "\n")
	assert.GreaterOrEqual(t, len(lines), 3)
}

func TestCleanContent_CollapsesExcessiveNewlines(t *testing.T) {
	tool := &WebFetchTool{}

	input := "Line one\n\n\n\n\nLine two"
	result := tool.cleanContent(input)

	// Should collapse 5 newlines down to 2
	assert.NotContains(t, result, "\n\n\n")
	assert.Contains(t, result, "\n\n")
}

func TestCleanContent_CollapsesHorizontalWhitespace(t *testing.T) {
	tool := &WebFetchTool{}

	input := "word1    word2\t\tword3"
	result := tool.cleanContent(input)

	assert.NotContains(t, result, "    ")
	assert.NotContains(t, result, "\t")
	assert.Contains(t, result, "word1 word2 word3")
}

func TestCleanContent_MarkdownStructure(t *testing.T) {
	tool := &WebFetchTool{}

	input := "# Title\n\nParagraph one.\n\nParagraph two.\n\n- Item 1\n- Item 2"
	result := tool.cleanContent(input)

	assert.Contains(t, result, "# Title")
	assert.Contains(t, result, "Paragraph one.")
	assert.Contains(t, result, "- Item 1\n- Item 2")
}

func TestCleanContent_TrimWhitespace(t *testing.T) {
	tool := &WebFetchTool{}

	input := "   \n\n  Content here  \n\n   "
	result := tool.cleanContent(input)

	assert.Equal(t, "Content here", result)
}

func TestCleanContent_EmptyInput(t *testing.T) {
	tool := &WebFetchTool{}

	result := tool.cleanContent("")
	assert.Equal(t, "", result)
}

func TestCleanContent_OnlyWhitespace(t *testing.T) {
	tool := &WebFetchTool{}

	result := tool.cleanContent("   \t\n\n   ")
	assert.Equal(t, "", result)
}

func TestExtractFromHTML_WithTitle(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><head><title>Page Title</title></head><body><p>Content</p></body></html>`
	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "# Page Title")
}

func TestExtractFromHTML_TextMode(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><head><title>Page Title</title></head><body><p>Content</p></body></html>`
	result, err := tool.extractFromHTML(html, "text")

	require.NoError(t, err)
	assert.Contains(t, result, "Page Title")
	assert.NotContains(t, result, "# Page Title")
}

func TestExtractFromHTML_RemovesUnwantedElements(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html>
<head><title>Test</title></head>
<body>
<nav>Navigation</nav>
<main><p>Main Content</p></main>
<script>alert('evil');</script>
<style>.hidden{display:none}</style>
<footer>Footer content</footer>
<aside>Sidebar</aside>
</body>
</html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "Main Content")
	assert.NotContains(t, result, "alert")
	assert.NotContains(t, result, ".hidden")
}

func TestExtractFromHTML_EmptyTitle(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><head><title></title></head><body><p>Content</p></body></html>`
	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "Content")
}

func TestExtractFromHTML_NoTitle(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body><p>Content only</p></body></html>`
	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "Content only")
}

func TestExtractFromHTML_InvalidHTML(t *testing.T) {
	tool := &WebFetchTool{}

	// goquery is tolerant of malformed HTML - it will parse what it can
	html := `<<<invalid html>>>some visible text`
	result, err := tool.extractFromHTML(html, "markdown")

	// goquery doesn't return an error for malformed HTML, it parses what it can
	require.NoError(t, err)
	// Result may be empty or contain partial content depending on how goquery handles it
	assert.NotNil(t, result)
}

func TestExtractMarkdown_Headings(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body>
<h1>Heading 1</h1>
<h2>Heading 2</h2>
<h3>Heading 3</h3>
<h4>Heading 4</h4>
<h5>Heading 5</h5>
<h6>Heading 6</h6>
</body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "# Heading 1")
	assert.Contains(t, result, "## Heading 2")
	assert.Contains(t, result, "### Heading 3")
	assert.Contains(t, result, "#### Heading 4")
	assert.Contains(t, result, "#### Heading 5")
	assert.Contains(t, result, "#### Heading 6")
}

func TestExtractMarkdown_Paragraphs(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body>
<p>First paragraph.</p>
<p>Second paragraph.</p>
</body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "First paragraph.")
	assert.Contains(t, result, "Second paragraph.")
}

func TestExtractMarkdown_Links(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body>
<p>Visit <a href="https://example.com">Example</a> for more info.</p>
</body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "[Example](https://example.com)")
}

func TestExtractMarkdown_LinkWithoutHref(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body>
<p>Click <a>here</a> to continue.</p>
</body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "here")
}

func TestExtractMarkdown_Lists(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body>
<ul>
<li>Item one</li>
<li>Item two</li>
<li>Item three</li>
</ul>
</body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "- Item one")
	assert.Contains(t, result, "- Item two")
	assert.Contains(t, result, "- Item three")
}

func TestExtractMarkdown_TextFormatting(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body>
<p><strong>Bold text</strong> and <em>italic text</em> and <code>code text</code></p>
</body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "**Bold text**")
	assert.Contains(t, result, "*italic text*")
	assert.Contains(t, result, "`code text`")
}

func TestExtractMarkdown_BTag(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body><p><b>Bold with b tag</b></p></body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "**Bold with b tag**")
}

func TestExtractMarkdown_ITag(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body><p><i>Italic with i tag</i></p></body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "*Italic with i tag*")
}

func TestExtractMarkdown_Blockquote(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body><blockquote>A quoted text</blockquote></body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "> A quoted text")
}

func TestExtractMarkdown_MainContentSelector(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body>
<div>Outer content</div>
<main>
<p>Main content paragraph</p>
</main>
</body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "Main content paragraph")
}

func TestExtractMarkdown_ArticleContentSelector(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body>
<article>
<p>Article content paragraph</p>
</article>
</body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "Article content paragraph")
}

func TestExtractMarkdown_ContentClassSelector(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body>
<div class="content">
<p>Content class paragraph</p>
</div>
</body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "Content class paragraph")
}

func TestExtractMarkdown_EmptyElements(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body>
<h1></h1>
<p></p>
<p>Visible content</p>
</body></html>`

	result, err := tool.extractFromHTML(html, "markdown")

	require.NoError(t, err)
	assert.Contains(t, result, "Visible content")
}

func TestExtractText_Basic(t *testing.T) {
	tool := &WebFetchTool{}

	html := `<html><body>
<p>Paragraph text here.</p>
<script>var x = 1;</script>
<style>.class { color: red; }</style>
</body></html>`

	result, err := tool.extractFromHTML(html, "text")

	require.NoError(t, err)
	assert.Contains(t, result, "Paragraph text here.")
	assert.NotContains(t, result, "var x")
	assert.NotContains(t, result, "color: red")
}

func TestWebFetchTool_Execute_RequestHeaders(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "OK")
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": server.URL,
	})

	require.NoError(t, err)
	assert.Contains(t, receivedHeaders.Get("User-Agent"), "Conduit-Gateway")
	assert.Contains(t, receivedHeaders.Get("Accept"), "text/html")
	assert.Contains(t, receivedHeaders.Get("Accept-Language"), "en-US")
}

func TestWebFetchTool_Execute_TextCSS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "body { color: red; }")
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": server.URL,
	})

	require.NoError(t, err)
	// text/css contains "text/" so it's treated as plain text
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "body { color: red; }")
}

func TestWebFetchTool_Execute_MaxCharsFromFloat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, strings.Repeat("X", 500))
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url":      server.URL,
		"maxChars": float64(100), // JSON numbers are float64
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "[Content truncated...]")
}

func TestWebFetchTool_Execute_EmptyHTMLBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `<html><body></body></html>`)
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url": server.URL,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestWebFetchTool_Execute_NoTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "Short content")
	}))
	defer server.Close()

	tool := NewWebFetchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url":      server.URL,
		"maxChars": 50000,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.False(t, result.Data["truncated"].(bool))
	assert.NotContains(t, result.Content, "[Content truncated...]")
}
