package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"conduit/internal/tools/types"
)

// WebFetchTool fetches and extracts readable content from URLs
type WebFetchTool struct {
	services   *types.ToolServices
	httpClient *http.Client
}

func NewWebFetchTool(services *types.ToolServices) *WebFetchTool {
	tool := &WebFetchTool{
		services: services,
	}

	// Get HTTP client from services
	if services != nil && services.WebClient != nil {
		tool.httpClient = services.WebClient
	}

	// Fallback to default client if not provided
	if tool.httpClient == nil {
		tool.httpClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	}

	return tool
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

	extractMode := t.getStringArg(args, "extractMode", "markdown")
	maxChars := t.getIntArg(args, "maxChars", 50000)

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
	content, err := t.fetchAndExtract(ctx, urlStr, extractMode, maxChars)
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
			"truncated":   len(content) >= maxChars,
		},
	}, nil
}

// fetchAndExtract fetches content from URL and extracts readable text
func (t *WebFetchTool) fetchAndExtract(ctx context.Context, urlStr, extractMode string, maxChars int) (string, error) {
	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set realistic headers
	req.Header.Set("User-Agent", "Conduit-Gateway/1.0 (Web Content Fetcher)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Accept-Encoding", "gzip, deflate")

	// Perform request
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP error %d: %s", resp.StatusCode, resp.Status)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Extract content based on content type
	contentType := resp.Header.Get("Content-Type")

	var content string
	if strings.Contains(contentType, "text/html") {
		content, err = t.extractFromHTML(string(body), extractMode)
		if err != nil {
			return "", fmt.Errorf("failed to extract from HTML: %w", err)
		}
	} else if strings.Contains(contentType, "text/") {
		// Plain text content
		content = string(body)
	} else {
		return "", fmt.Errorf("unsupported content type: %s", contentType)
	}

	// Truncate if needed
	if len(content) > maxChars {
		content = content[:maxChars] + "\n\n[Content truncated...]"
	}

	return content, nil
}

// extractFromHTML extracts readable content from HTML
func (t *WebFetchTool) extractFromHTML(html, extractMode string) (string, error) {
	// Parse HTML
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %w", err)
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
	if extractMode == "markdown" {
		content.WriteString(t.extractMarkdown(doc))
	} else {
		content.WriteString(t.extractText(doc))
	}

	// Clean up the content
	result := content.String()
	result = t.cleanContent(result)

	return result, nil
}

// extractMarkdown converts HTML to markdown-like format
func (t *WebFetchTool) extractMarkdown(doc *goquery.Document) string {
	var content strings.Builder

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

	// Process elements
	mainContent.Find("*").Each(func(i int, s *goquery.Selection) {
		tag := goquery.NodeName(s)
		text := strings.TrimSpace(s.Text())

		if text == "" {
			return
		}

		switch tag {
		case "h1":
			content.WriteString(fmt.Sprintf("# %s\n\n", text))
		case "h2":
			content.WriteString(fmt.Sprintf("## %s\n\n", text))
		case "h3":
			content.WriteString(fmt.Sprintf("### %s\n\n", text))
		case "h4", "h5", "h6":
			content.WriteString(fmt.Sprintf("#### %s\n\n", text))
		case "p":
			content.WriteString(fmt.Sprintf("%s\n\n", text))
		case "a":
			href, exists := s.Attr("href")
			if exists && href != "" {
				content.WriteString(fmt.Sprintf("[%s](%s) ", text, href))
			} else {
				content.WriteString(text + " ")
			}
		case "li":
			content.WriteString(fmt.Sprintf("- %s\n", text))
		case "strong", "b":
			content.WriteString(fmt.Sprintf("**%s** ", text))
		case "em", "i":
			content.WriteString(fmt.Sprintf("*%s* ", text))
		case "code":
			content.WriteString(fmt.Sprintf("`%s` ", text))
		case "blockquote":
			content.WriteString(fmt.Sprintf("> %s\n\n", text))
		case "br":
			content.WriteString("\n")
		}
	})

	return content.String()
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
	// Remove excessive whitespace
	re := regexp.MustCompile(`\s+`)
	content = re.ReplaceAllString(content, " ")

	// Fix line breaks
	re = regexp.MustCompile(`\n\s*\n\s*\n`)
	content = re.ReplaceAllString(content, "\n\n")

	// Remove leading/trailing whitespace
	content = strings.TrimSpace(content)

	return content
}

// Helper methods
func (t *WebFetchTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

func (t *WebFetchTool) getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if val, ok := args[key].(float64); ok {
		return int(val)
	}
	if val, ok := args[key].(int); ok {
		return val
	}
	return defaultVal
}
