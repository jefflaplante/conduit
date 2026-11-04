package web

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"conduit/internal/httpsafe"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
	"conduit/internal/version"
	"github.com/PuerkitoBio/goquery"
)

// WebFetchTool fetches and extracts readable content from URLs
type WebFetchTool struct {
	services     *types.ToolServices
	httpClient   *http.Client
	maxBodyBytes int64 // conduit-31jg.7: cap on raw response bytes read
}

func NewWebFetchTool(services *types.ToolServices) *WebFetchTool {
	tool := &WebFetchTool{
		services:     services,
		maxBodyBytes: httpsafe.WebPageBodyLimit,
	}

	// conduit-31jg.7: WebFetch URLs are model-controlled (and so reachable by
	// prompt injection), so it never uses the shared, unguarded WebClient.
	// It builds its own client whose dialer checks the resolved IP of every
	// connection, redirects included. Only the timeout is inherited.
	tool.httpClient = NewSafeClient(services, 30*time.Second)

	return tool
}

// NewSafeClient returns an SSRF-guarded HTTP client for fetching
// model-controlled URLs, using the tools.web policy (block_private_networks,
// allowed_hosts). Only the timeout is inherited from the shared WebClient;
// defaultTimeout applies when it has none. Shared by WebFetch and Image.
// conduit-31jg.62
func NewSafeClient(services *types.ToolServices, defaultTimeout time.Duration) *http.Client {
	timeout := defaultTimeout
	if services != nil && services.WebClient != nil && services.WebClient.Timeout > 0 {
		timeout = services.WebClient.Timeout
	}
	return httpsafe.NewClient(SSRFPolicy(services), timeout, nil)
}

// SSRFPolicy builds the outbound-fetch policy from tools.web config.
// conduit-31jg.7
func SSRFPolicy(services *types.ToolServices) httpsafe.Policy {
	var p httpsafe.Policy
	if services != nil && services.ConfigMgr != nil {
		w := services.ConfigMgr.Tools.Web
		p.BlockPrivate = w.BlockPrivateNetworks
		p.AllowedHosts = append([]string(nil), w.AllowedHosts...)
	}
	return p
}

func (t *WebFetchTool) Name() string {
	return "WebFetch"
}

func (t *WebFetchTool) Description() string {
	return "Fetch and extract readable content from a URL, converting HTML to markdown or text"
}

func (t *WebFetchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "HTTP or HTTPS URL to fetch",
			},
			"extractMode": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"markdown", "text"},
				"description": "Content extraction mode",
				"default":     "markdown",
			},
			"maxChars": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum characters to return (truncates when exceeded)",
				"default":     50000,
			},
		},
		"required": []string{"url"},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	urlStr, ok := args["url"].(string)
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   "url parameter is required and must be a string",
		}, nil
	}

	extractMode := toolargs.GetString(args, "extractMode", "markdown")
	maxChars := toolargs.GetInt(args, "maxChars", 50000)
	if maxChars < 0 {
		maxChars = 0 // H8: a negative limit panicked in the truncation slice
	}

	// Validate URL
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("invalid URL: %v", err),
		}, nil
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return &types.ToolResult{
			Success: false,
			Error:   "only HTTP and HTTPS URLs are supported",
		}, nil
	}

	// Fetch and extract content
	content, truncated, err := t.fetchAndExtract(ctx, urlStr, extractMode, maxChars)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to fetch content: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"url":         urlStr,
			"extractMode": extractMode,
			"length":      len(content),
			"maxChars":    maxChars,
			"truncated":   truncated || len(content) >= maxChars,
		},
	}, nil
}

// fetchAndExtract fetches content from URL and extracts readable text
func (t *WebFetchTool) fetchAndExtract(ctx context.Context, urlStr, extractMode string, maxChars int) (string, bool, error) {
	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", false, fmt.Errorf("failed to create request: %w", err)
	}

	// Set realistic headers
	req.Header.Set("User-Agent", version.UserAgent()+" (web fetch)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	// conduit-31jg.7: no explicit Accept-Encoding. Setting it disables Go's
	// transparent gzip decoding, so gzip pages came back as compressed
	// bytes and the size limit applied to compressed data. Letting the
	// transport negotiate gzip means the limit below bounds decoded bytes.

	// Perform request
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, resp.Status)
	}

	// Read response body. conduit-31jg.7: bounded read; an oversized page is
	// truncated (and flagged) rather than buffered whole.
	limit := t.maxBodyBytes
	if limit <= 0 {
		limit = httpsafe.WebPageBodyLimit
	}
	body, err := httpsafe.ReadLimited(resp.Body, limit)
	bodyTruncated := errors.Is(err, httpsafe.ErrBodyTooLarge)
	if err != nil && !bodyTruncated {
		return "", false, fmt.Errorf("failed to read response: %w", err)
	}

	// Extract content based on content type
	contentType := resp.Header.Get("Content-Type")

	if maxChars < 0 {
		maxChars = 0
	}

	var content string
	var extractCapped bool
	if strings.Contains(contentType, "text/html") {
		content, extractCapped, err = t.extractFromHTMLCapped(string(body), extractMode, maxChars)
		if err != nil {
			return "", false, fmt.Errorf("failed to extract from HTML: %w", err)
		}
	} else if strings.Contains(contentType, "text/") {
		// Plain text content
		content = string(body)
	} else {
		return "", false, fmt.Errorf("unsupported content type: %s", contentType)
	}

	// Truncate if needed
	truncated := bodyTruncated
	if len(content) > maxChars || extractCapped {
		if len(content) > maxChars {
			content = content[:maxChars]
		}
		content += "\n\n[Content truncated...]"
		truncated = true
	} else if bodyTruncated {
		content += "\n\n[Content truncated: response exceeded size limit]"
	}

	return content, truncated, nil
}

// extractFromHTML extracts readable content from HTML, with no output cap.
func (t *WebFetchTool) extractFromHTML(html, extractMode string) (string, error) {
	content, _, err := t.extractFromHTMLCapped(html, extractMode, -1)
	return content, err
}

// extractionCap is the raw markdown the extractor may build for a maxChars
// limit (H8): room for cleanContent's whitespace collapse, still O(maxChars).
func extractionCap(maxChars int) int {
	const slack = 4096
	if maxChars > (math.MaxInt-slack)/2 {
		return math.MaxInt
	}
	return 2*maxChars + slack
}

// extractFromHTMLCapped is extractFromHTML with markdown extraction stopped
// once it has built extractionCap(maxChars) bytes (maxChars < 0: no cap).
// capped reports that extraction stopped early.
func (t *WebFetchTool) extractFromHTMLCapped(html, extractMode string, maxChars int) (string, bool, error) {
	// Parse HTML
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", false, fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Remove unwanted elements
	doc.Find("script, style, nav, footer, aside, .ad, .advertisement, .sidebar").Remove()

	var content strings.Builder

	// Extract title
	title := doc.Find("title").Text()
	if title != "" {
		title = strings.TrimSpace(title)
		if extractMode == "markdown" {
			content.WriteString(fmt.Sprintf("# %s\n\n", title))
		} else {
			content.WriteString(fmt.Sprintf("%s\n\n", title))
		}
	}

	// Extract main content
	capped := false
	if extractMode == "markdown" {
		limit := -1
		if maxChars >= 0 {
			limit = extractionCap(maxChars)
		}
		var md string
		md, capped = t.extractMarkdown(doc, limit)
		content.WriteString(md)
	} else {
		content.WriteString(t.extractText(doc))
	}

	// Clean up the content
	result := content.String()
	result = t.cleanContent(result)

	return result, capped, nil
}

// extractMarkdown converts HTML to markdown-like format. limit < 0 means
// no cap; otherwise extraction stops after limit bytes and capped is true.
//
// H8: every text node is emitted exactly once. A block element (heading,
// p, li, blockquote) is rendered once with its whole subtree inline, and an
// inline formatting element (a, strong/b, em/i, code) is formatted only at
// its outermost occurrence. Previously each element re-emitted its full
// subtree text, multiplying output by nesting depth.
func (t *WebFetchTool) extractMarkdown(doc *goquery.Document, limit int) (string, bool) {
	// Look for main content areas first
	mainSelectors := []string{
		"main", "article", ".content", ".post", ".entry", "#content", "#main",
	}

	var mainContent *goquery.Selection
	for _, selector := range mainSelectors {
		if sel := doc.Find(selector); sel.Length() > 0 {
			mainContent = sel.First()
			break
		}
	}

	// If no main content found, use body
	if mainContent == nil {
		mainContent = doc.Find("body")
	}

	out := &cappedBuilder{limit: limit}
	mainContent.Contents().Each(func(_ int, s *goquery.Selection) {
		mdBlock(s, out)
	})
	return out.String(), out.full
}

// cappedBuilder is a strings.Builder that stops growing at limit bytes
// (limit < 0: unlimited) and records that it did.
type cappedBuilder struct {
	b     strings.Builder
	limit int
	full  bool
}

func (c *cappedBuilder) WriteString(s string) {
	if c.full {
		return
	}
	if c.limit >= 0 && c.b.Len()+len(s) > c.limit {
		s = s[:c.limit-c.b.Len()]
		c.full = true
	}
	c.b.WriteString(s)
}

func (c *cappedBuilder) String() string { return c.b.String() }

// remaining is the byte budget left (-1: unlimited).
func (c *cappedBuilder) remaining() int {
	if c.limit < 0 {
		return -1
	}
	return c.limit - c.b.Len()
}

// mdBlock renders one node at block level. Text directly inside an
// unformatted container (div, section, ...) is not emitted, as before.
func mdBlock(s *goquery.Selection, out *cappedBuilder) {
	if out.full {
		return
	}
	tag := goquery.NodeName(s)
	switch tag {
	case "#text", "#comment":
		return
	case "h1", "h2", "h3", "h4", "h5", "h6", "p", "li", "blockquote":
		inner := &cappedBuilder{limit: out.remaining()}
		mdInlineChildren(s, inner)
		if inner.full {
			out.full = true // the formatted write below is cut at the cap
		}
		text := strings.TrimSpace(inner.String())
		if text == "" {
			return
		}
		switch tag {
		case "h1":
			out.WriteString("# " + text + "\n\n")
		case "h2":
			out.WriteString("## " + text + "\n\n")
		case "h3":
			out.WriteString("### " + text + "\n\n")
		case "h4", "h5", "h6":
			out.WriteString("#### " + text + "\n\n")
		case "p":
			out.WriteString(text + "\n\n")
		case "li":
			out.WriteString("- " + text + "\n")
		case "blockquote":
			out.WriteString("> " + text + "\n\n")
		}
	case "a", "strong", "b", "em", "i", "code":
		f, full := mdInlineFormat(s, tag, out.remaining())
		if f != "" {
			out.WriteString(f + " ")
		}
		if full {
			out.full = true
		}
	case "br":
		out.WriteString("\n")
	default:
		s.Contents().Each(func(_ int, c *goquery.Selection) {
			mdBlock(c, out)
		})
	}
}

// mdInlineChildren renders s's children inline into out: text nodes as-is,
// inline formatting at its outermost occurrence, nested blocks flattened.
func mdInlineChildren(s *goquery.Selection, out *cappedBuilder) {
	s.Contents().Each(func(_ int, c *goquery.Selection) {
		if out.full {
			return
		}
		tag := goquery.NodeName(c)
		switch tag {
		case "#text":
			out.WriteString(c.Text())
		case "#comment":
		case "a", "strong", "b", "em", "i", "code":
			f, full := mdInlineFormat(c, tag, out.remaining())
			out.WriteString(f)
			if full {
				out.full = true
			}
		case "br", "p", "div", "li", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6":
			out.WriteString(" ")
			mdInlineChildren(c, out)
			out.WriteString(" ")
		default:
			mdInlineChildren(c, out)
		}
	})
}

// mdInlineFormat formats an inline element from its subtree's plain text
// (read once, capped at limit bytes); nested formatting is not repeated.
// It returns "" when the element has no text, and full when the text was
// cut at the cap.
func mdInlineFormat(s *goquery.Selection, tag string, limit int) (string, bool) {
	tb := &cappedBuilder{limit: limit}
	plainText(s, tb)
	text := strings.TrimSpace(tb.String())
	if text == "" {
		return "", tb.full
	}
	switch tag {
	case "a":
		if href, ok := s.Attr("href"); ok && href != "" {
			return "[" + text + "](" + href + ")", tb.full
		}
		return text, tb.full
	case "strong", "b":
		return "**" + text + "**", tb.full
	case "em", "i":
		return "*" + text + "*", tb.full
	default: // code
		return "`" + text + "`", tb.full
	}
}

// plainText appends the text of s's subtree to out, stopping at its cap.
func plainText(s *goquery.Selection, out *cappedBuilder) {
	s.Contents().Each(func(_ int, c *goquery.Selection) {
		if out.full {
			return
		}
		switch goquery.NodeName(c) {
		case "#text":
			out.WriteString(c.Text())
		case "#comment":
		default:
			plainText(c, out)
		}
	})
}

// extractText extracts plain text from HTML
func (t *WebFetchTool) extractText(doc *goquery.Document) string {
	// Remove script and style elements
	doc.Find("script, style").Remove()

	// Get text content
	text := doc.Text()

	// Clean up whitespace
	return t.cleanContent(text)
}

// cleanContent cleans up extracted content
func (t *WebFetchTool) cleanContent(content string) string {
	// Collapse runs of horizontal whitespace (spaces, tabs) but preserve newlines
	re := regexp.MustCompile(`[^\S\n]+`)
	content = re.ReplaceAllString(content, " ")

	// Collapse 3+ consecutive newlines down to 2
	re = regexp.MustCompile(`\n\s*\n\s*\n`)
	content = re.ReplaceAllString(content, "\n\n")

	// Remove leading/trailing whitespace
	content = strings.TrimSpace(content)

	return content
}

// SelfTest implements types.SelfTester for WebFetchTool.
func (t *WebFetchTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Message:      "WebFetch tool is functional",
		Capabilities: []string{"fetch_url", "extract_markdown", "extract_text"},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}

	httpClientStatus := types.DependencyStatus{
		Name:     "HTTPClient",
		Required: true,
	}

	if t.httpClient != nil {
		httpClientStatus.Available = true
		httpClientStatus.Status = "ready"
	} else {
		httpClientStatus.Available = false
		httpClientStatus.Status = "not_configured"
		result.Status = types.SelfTestStatusFailed
		result.Message = "HTTP client not available"
		result.Suggestions = []string{"Check WebClient configuration in ToolServices"}
	}
	deps = append(deps, httpClientStatus)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "Fetch webpage as markdown",
				Description: "Fetch a URL and convert HTML content to readable markdown",
				Args: map[string]interface{}{
					"url":         "https://example.com",
					"extractMode": "markdown",
				},
				Expected: "Returns page content formatted as markdown",
			},
			{
				Name:        "Fetch plain text",
				Description: "Fetch a URL and extract plain text content",
				Args: map[string]interface{}{
					"url":         "https://example.com",
					"extractMode": "text",
					"maxChars":    10000,
				},
				Expected: "Returns plain text content, truncated if exceeds maxChars",
			},
		}
	}

	return result
}
