package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"conduit/internal/tools/types"
)

// ImageAnalysisResult represents the result of image analysis
type ImageAnalysisResult struct {
	Description string                 `json:"description"`
	Objects     []string               `json:"objects,omitempty"`
	Text        string                 `json:"text,omitempty"`
	Confidence  float64                `json:"confidence,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// ImageTool analyzes images using vision models
type ImageTool struct {
	services     *types.ToolServices
	httpClient   *http.Client
	workspaceDir string
}

func NewImageTool(services *types.ToolServices) *ImageTool {
	tool := &ImageTool{services: services}

	// Get HTTP client from services if available
	if services != nil && services.WebClient != nil {
		tool.httpClient = services.WebClient
	}

	// Fallback defaults
	if tool.httpClient == nil {
		tool.httpClient = &http.Client{
			Timeout: 60 * time.Second,
		}
	}
	tool.workspaceDir = "./workspace"

	return tool
}

func (t *ImageTool) Name() string {
	return "Image"
}

func (t *ImageTool) Description() string {
	return "Analyze images with vision models for description, object detection, and text extraction"
}

func (t *ImageTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"image": map[string]interface{}{
				"type":        "string",
				"description": "Image path, URL, or base64 data",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Specific question or analysis prompt for the image",
				"default":     "Describe what you see in this image",
			},
			"model": map[string]interface{}{
				"type":        "string",
				"description": "Vision model to use (e.g., gpt-4-vision, claude-3-vision)",
			},
			"maxBytesMb": map[string]interface{}{
				"type":        "number",
				"description": "Maximum image size in MB",
				"default":     5.0,
			},
			"extractText": map[string]interface{}{
				"type":        "boolean",
				"description": "Enable OCR text extraction",
				"default":     false,
			},
			"detectObjects": map[string]interface{}{
				"type":        "boolean",
				"description": "Enable object detection",
				"default":     false,
			},
		},
		"required": []string{"image"},
	}
}

func (t *ImageTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	image, ok := args["image"].(string)
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   "image parameter is required and must be a string",
		}, nil
	}

	prompt := t.getStringArg(args, "prompt", "Describe what you see in this image")
	model := t.getStringArg(args, "model", "")
	maxBytesMb := t.getFloatArg(args, "maxBytesMb", 5.0)
	extractText := t.getBoolArg(args, "extractText", false)
	detectObjects := t.getBoolArg(args, "detectObjects", false)

	// Load image data
	imageData, metadata, err := t.loadImageData(ctx, image, maxBytesMb)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to load image: %v", err),
		}, nil
	}

	// Build analysis options
	options := map[string]interface{}{
		"model":         model,
		"extractText":   extractText,
		"detectObjects": detectObjects,
	}

	// Analyze image
	result, err := t.analyzeImage(ctx, imageData, prompt, options)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("image analysis failed: %v", err),
		}, nil
	}

	// Combine metadata
	if result.Metadata == nil {
		result.Metadata = make(map[string]interface{})
	}
	for k, v := range metadata {
		result.Metadata[k] = v
	}

	// Format response
	content := t.formatAnalysisResult(result, prompt)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"result":       result,
			"prompt":       prompt,
			"options":      options,
			"image_size":   len(imageData),
			"image_format": metadata["format"],
		},
	}, nil
}

// loadImageData loads image data from various sources
func (t *ImageTool) loadImageData(ctx context.Context, image string, maxBytesMb float64) ([]byte, map[string]interface{}, error) {
	metadata := make(map[string]interface{})
	maxBytes := int64(maxBytesMb * 1024 * 1024)

	// Check if it's a base64 data URL
	if strings.HasPrefix(image, "data:") {
		return t.loadFromDataURL(image, maxBytes, metadata)
	}

	// Check if it's a URL
	if strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		return t.loadFromURL(ctx, image, maxBytes, metadata)
	}

	// Treat as file path
	return t.loadFromFile(image, maxBytes, metadata)
}

// loadFromDataURL loads image from base64 data URL
func (t *ImageTool) loadFromDataURL(dataURL string, maxBytes int64, metadata map[string]interface{}) ([]byte, map[string]interface{}, error) {
	// Parse data URL: data:image/jpeg;base64,iVBORw0KGgoAAAA...
	parts := strings.SplitN(dataURL, ",", 2)
	if len(parts) != 2 {
		return nil, nil, fmt.Errorf("invalid data URL format")
	}

	// Extract MIME type
	mimeInfo := parts[0]
	if strings.Contains(mimeInfo, "image/jpeg") {
		metadata["format"] = "jpeg"
	} else if strings.Contains(mimeInfo, "image/png") {
		metadata["format"] = "png"
	} else if strings.Contains(mimeInfo, "image/gif") {
		metadata["format"] = "gif"
	} else {
		metadata["format"] = "unknown"
	}

	// Decode base64
	imageData, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode base64: %w", err)
	}

	if int64(len(imageData)) > maxBytes {
		return nil, nil, fmt.Errorf("image size (%d bytes) exceeds limit (%d bytes)", len(imageData), maxBytes)
	}

	metadata["source"] = "data_url"
	metadata["size"] = len(imageData)

	return imageData, metadata, nil
}

// loadFromURL loads image from HTTP URL
func (t *ImageTool) loadFromURL(ctx context.Context, url string, maxBytes int64, metadata map[string]interface{}) ([]byte, map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("User-Agent", "Conduit-Gateway/1.0 (Image Analyzer)")
	req.Header.Set("Accept", "image/*")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, resp.Status)
	}

	// Read with size limit
	limitedReader := io.LimitReader(resp.Body, maxBytes+1)
	imageData, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read image data: %w", err)
	}

	if int64(len(imageData)) > maxBytes {
		return nil, nil, fmt.Errorf("image size exceeds limit of %d bytes", maxBytes)
	}

	// Extract metadata from headers
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "jpeg") {
		metadata["format"] = "jpeg"
	} else if strings.Contains(contentType, "png") {
		metadata["format"] = "png"
	} else if strings.Contains(contentType, "gif") {
		metadata["format"] = "gif"
	} else {
		metadata["format"] = "unknown"
	}

	metadata["source"] = "url"
	metadata["url"] = url
	metadata["size"] = len(imageData)
	metadata["content_type"] = contentType

	return imageData, metadata, nil
}

// loadFromFile loads image from local file
func (t *ImageTool) loadFromFile(path string, maxBytes int64, metadata map[string]interface{}) ([]byte, map[string]interface{}, error) {
	// Make path relative to workspace if it's not absolute
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.workspaceDir, path)
	}

	// Check file exists and size
	fileInfo, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("file not found: %w", err)
	}

	if fileInfo.Size() > maxBytes {
		return nil, nil, fmt.Errorf("file size (%d bytes) exceeds limit (%d bytes)", fileInfo.Size(), maxBytes)
	}

	// Read file
	imageData, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Detect format from extension
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg":
		metadata["format"] = "jpeg"
	case ".png":
		metadata["format"] = "png"
	case ".gif":
		metadata["format"] = "gif"
	case ".webp":
		metadata["format"] = "webp"
	default:
		metadata["format"] = "unknown"
	}

	metadata["source"] = "file"
	metadata["path"] = path
	metadata["size"] = len(imageData)
	metadata["modified"] = fileInfo.ModTime()

	return imageData, metadata, nil
}

// analyzeImage performs the actual image analysis
func (t *ImageTool) analyzeImage(ctx context.Context, imageData []byte, prompt string, options map[string]interface{}) (*ImageAnalysisResult, error) {
	// Placeholder implementation - would integrate with actual vision service
	result := &ImageAnalysisResult{
		Description: "Image analysis service not available. This is a placeholder response.",
		Confidence:  0.0,
		Metadata: map[string]interface{}{
			"analyzed_at": time.Now(),
			"service":     "placeholder",
		},
	}

	// Basic image format detection
	if len(imageData) > 4 {
		if bytes.Equal(imageData[:4], []byte{0xFF, 0xD8, 0xFF, 0xE0}) ||
			bytes.Equal(imageData[:4], []byte{0xFF, 0xD8, 0xFF, 0xE1}) {
			result.Description = "JPEG image detected. " + result.Description
		} else if bytes.Equal(imageData[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
			result.Description = "PNG image detected. " + result.Description
		}
	}

	return result, nil
}

// formatAnalysisResult formats the analysis result for display
func (t *ImageTool) formatAnalysisResult(result *ImageAnalysisResult, prompt string) string {
	var builder strings.Builder

	builder.WriteString("Image Analysis Result:\n\n")

	if prompt != "Describe what you see in this image" {
		builder.WriteString(fmt.Sprintf("**Question:** %s\n\n", prompt))
	}

	builder.WriteString(fmt.Sprintf("**Description:** %s\n\n", result.Description))

	if len(result.Objects) > 0 {
		builder.WriteString("**Objects Detected:**\n")
		for _, obj := range result.Objects {
			builder.WriteString(fmt.Sprintf("- %s\n", obj))
		}
		builder.WriteString("\n")
	}

	if result.Text != "" {
		builder.WriteString(fmt.Sprintf("**Text Found:** %s\n\n", result.Text))
	}

	if result.Confidence > 0 {
		builder.WriteString(fmt.Sprintf("**Confidence:** %.1f%%\n\n", result.Confidence*100))
	}

	return builder.String()
}

// Helper methods
func (t *ImageTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

func (t *ImageTool) getFloatArg(args map[string]interface{}, key string, defaultVal float64) float64 {
	if val, ok := args[key].(float64); ok {
		return val
	}
	if val, ok := args[key].(int); ok {
		return float64(val)
	}
	return defaultVal
}

func (t *ImageTool) getBoolArg(args map[string]interface{}, key string, defaultVal bool) bool {
	if val, ok := args[key].(bool); ok {
		return val
	}
	return defaultVal
}

// ToolResult is imported from the tools package
