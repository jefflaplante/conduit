package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewImageTool(t *testing.T) {
	t.Run("with nil services", func(t *testing.T) {
		tool := NewImageTool(nil)
		require.NotNil(t, tool)
		assert.NotNil(t, tool.httpClient)
		assert.Equal(t, "./workspace", tool.workspaceDir)
	})

	t.Run("with services but nil WebClient", func(t *testing.T) {
		services := &types.ToolServices{}
		tool := NewImageTool(services)
		require.NotNil(t, tool)
		assert.NotNil(t, tool.httpClient)
	})

	t.Run("with services and WebClient", func(t *testing.T) {
		customClient := &http.Client{Timeout: 30 * time.Second}
		services := &types.ToolServices{WebClient: customClient}
		tool := NewImageTool(services)
		require.NotNil(t, tool)
		assert.Equal(t, customClient, tool.httpClient)
	})
}

func TestImageTool_Name(t *testing.T) {
	tool := NewImageTool(nil)
	assert.Equal(t, "Image", tool.Name())
}

func TestImageTool_Description(t *testing.T) {
	tool := NewImageTool(nil)
	desc := tool.Description()
	assert.Contains(t, desc, "Analyze images")
	assert.Contains(t, desc, "vision")
}

func TestImageTool_Parameters(t *testing.T) {
	tool := NewImageTool(nil)
	params := tool.Parameters()

	require.NotNil(t, params)
	assert.Equal(t, "object", params["type"])

	props, ok := params["properties"].(map[string]interface{})
	require.True(t, ok)

	// Check required parameters
	required, ok := params["required"].([]string)
	require.True(t, ok)
	assert.Contains(t, required, "image")

	// Check image parameter
	image, ok := props["image"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "string", image["type"])

	// Check prompt parameter
	prompt, ok := props["prompt"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "string", prompt["type"])
	assert.Equal(t, "Describe what you see in this image", prompt["default"])

	// Check maxBytesMb parameter
	maxBytesMb, ok := props["maxBytesMb"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "number", maxBytesMb["type"])
	assert.Equal(t, 5.0, maxBytesMb["default"])

	// Check extractText parameter
	extractText, ok := props["extractText"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "boolean", extractText["type"])
	assert.Equal(t, false, extractText["default"])

	// Check detectObjects parameter
	detectObjects, ok := props["detectObjects"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "boolean", detectObjects["type"])
	assert.Equal(t, false, detectObjects["default"])
}

func TestImageTool_Execute_MissingImage(t *testing.T) {
	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "image parameter is required")
}

func TestImageTool_Execute_ImageNotString(t *testing.T) {
	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": 12345,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "image parameter is required")
}

func TestImageTool_Execute_FromFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a minimal JPEG file (just the header)
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00}
	imagePath := filepath.Join(tmpDir, "test.jpg")
	err := os.WriteFile(imagePath, jpegData, 0644)
	require.NoError(t, err)

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": imagePath,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Image Analysis Result")
	assert.Contains(t, result.Content, "JPEG image detected")

	// Check data fields
	assert.Equal(t, "jpeg", result.Data["image_format"])
	assert.Equal(t, len(jpegData), result.Data["image_size"])
}

func TestImageTool_Execute_FromPNGFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a minimal PNG file (just the header)
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	imagePath := filepath.Join(tmpDir, "test.png")
	err := os.WriteFile(imagePath, pngData, 0644)
	require.NoError(t, err)

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": imagePath,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "PNG image detected")
	assert.Equal(t, "png", result.Data["image_format"])
}

func TestImageTool_Execute_FromGIFFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a minimal GIF file (just some bytes with .gif extension)
	gifData := []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00}
	imagePath := filepath.Join(tmpDir, "test.gif")
	err := os.WriteFile(imagePath, gifData, 0644)
	require.NoError(t, err)

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": imagePath,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "gif", result.Data["image_format"])
}

func TestImageTool_Execute_FromWebPFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a minimal WebP file
	webpData := []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50}
	imagePath := filepath.Join(tmpDir, "test.webp")
	err := os.WriteFile(imagePath, webpData, 0644)
	require.NoError(t, err)

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": imagePath,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "webp", result.Data["image_format"])
}

func TestImageTool_Execute_FileNotFound(t *testing.T) {
	tool := NewImageTool(nil)
	tool.workspaceDir = t.TempDir()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": "/nonexistent/path/to/image.jpg",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "failed to load image")
	assert.Contains(t, result.Error, "file not found")
}

func TestImageTool_Execute_FileTooLarge(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file larger than the default 5MB limit
	largeData := bytes.Repeat([]byte{0xFF}, 6*1024*1024)
	imagePath := filepath.Join(tmpDir, "large.jpg")
	err := os.WriteFile(imagePath, largeData, 0644)
	require.NoError(t, err)

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": imagePath,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exceeds limit")
}

func TestImageTool_Execute_CustomMaxSize(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a 2MB file
	data := bytes.Repeat([]byte{0xFF, 0xD8, 0xFF, 0xE0}, 512*1024)
	imagePath := filepath.Join(tmpDir, "medium.jpg")
	err := os.WriteFile(imagePath, data, 0644)
	require.NoError(t, err)

	tool := NewImageTool(nil)

	// Should fail with 1MB limit
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image":      imagePath,
		"maxBytesMb": 1.0,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exceeds limit")

	// Should succeed with 3MB limit
	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"image":      imagePath,
		"maxBytesMb": 3.0,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestImageTool_Execute_FromDataURL(t *testing.T) {
	// Create a simple base64-encoded image (need at least 8 bytes for PNG detection check)
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}
	b64Data := base64.StdEncoding.EncodeToString(jpegData)
	dataURL := "data:image/jpeg;base64," + b64Data

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": dataURL,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "jpeg", result.Data["image_format"])
}

func TestImageTool_Execute_FromDataURL_PNG(t *testing.T) {
	// Need exactly 8 bytes for PNG magic check, add more to be safe
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	b64Data := base64.StdEncoding.EncodeToString(pngData)
	dataURL := "data:image/png;base64," + b64Data

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": dataURL,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "png", result.Data["image_format"])
}

func TestImageTool_Execute_FromDataURL_GIF(t *testing.T) {
	// Need at least 8 bytes for PNG detection check in analyzeImage
	gifData := []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00}
	b64Data := base64.StdEncoding.EncodeToString(gifData)
	dataURL := "data:image/gif;base64," + b64Data

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": dataURL,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "gif", result.Data["image_format"])
}

func TestImageTool_Execute_FromDataURL_Invalid(t *testing.T) {
	tool := NewImageTool(nil)

	// Missing comma separator
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": "data:image/jpeg;base64INVALIDDATA",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "invalid data URL format")
}

func TestImageTool_Execute_FromDataURL_InvalidBase64(t *testing.T) {
	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": "data:image/jpeg;base64,!!invalid!!base64!!",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "failed to decode base64")
}

func TestImageTool_Execute_FromDataURL_TooLarge(t *testing.T) {
	// Create a large data URL
	largeData := bytes.Repeat([]byte{0xFF}, 6*1024*1024)
	b64Data := base64.StdEncoding.EncodeToString(largeData)
	dataURL := "data:image/jpeg;base64," + b64Data

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": dataURL,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exceeds limit")
}

func TestImageTool_Execute_FromURL(t *testing.T) {
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpegData)
	}))
	defer server.Close()

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": server.URL + "/test.jpg",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "jpeg", result.Data["image_format"])
	assert.Contains(t, result.Content, "JPEG image detected")
}

func TestImageTool_Execute_FromURL_PNG(t *testing.T) {
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngData)
	}))
	defer server.Close()

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": server.URL + "/test.png",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "png", result.Data["image_format"])
}

func TestImageTool_Execute_FromURL_GIF(t *testing.T) {
	gifData := []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		w.Write(gifData)
	}))
	defer server.Close()

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": server.URL + "/test.gif",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "gif", result.Data["image_format"])
}

func TestImageTool_Execute_FromURL_UnknownContentType(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02, 0x03}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(data)
	}))
	defer server.Close()

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": server.URL + "/test.bin",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "unknown", result.Data["image_format"])
}

func TestImageTool_Execute_FromURL_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": server.URL + "/notfound.jpg",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "HTTP error 404")
}

func TestImageTool_Execute_FromURL_TooLarge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		// Stream more than 5MB
		for i := 0; i < 6*1024; i++ {
			w.Write(bytes.Repeat([]byte{0xFF}, 1024))
		}
	}))
	defer server.Close()

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": server.URL + "/large.jpg",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exceeds limit")
}

func TestImageTool_Execute_FromURL_ConnectionError(t *testing.T) {
	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": "http://localhost:99999/unreachable.jpg",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "failed to fetch image")
}

func TestImageTool_Execute_WithCustomPrompt(t *testing.T) {
	tmpDir := t.TempDir()

	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	imagePath := filepath.Join(tmpDir, "test.jpg")
	err := os.WriteFile(imagePath, jpegData, 0644)
	require.NoError(t, err)

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image":  imagePath,
		"prompt": "What objects are in this image?",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "What objects are in this image?")
	assert.Equal(t, "What objects are in this image?", result.Data["prompt"])
}

func TestImageTool_Execute_WithOptions(t *testing.T) {
	tmpDir := t.TempDir()

	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	imagePath := filepath.Join(tmpDir, "test.jpg")
	err := os.WriteFile(imagePath, jpegData, 0644)
	require.NoError(t, err)

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image":         imagePath,
		"model":         "gpt-4-vision",
		"extractText":   true,
		"detectObjects": true,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	options, ok := result.Data["options"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "gpt-4-vision", options["model"])
	assert.Equal(t, true, options["extractText"])
	assert.Equal(t, true, options["detectObjects"])
}

func TestImageTool_Execute_RelativePath(t *testing.T) {
	tmpDir := t.TempDir()

	// Create image in workspace
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	imagePath := filepath.Join(tmpDir, "images", "test.jpg")
	err := os.MkdirAll(filepath.Dir(imagePath), 0755)
	require.NoError(t, err)
	err = os.WriteFile(imagePath, jpegData, 0644)
	require.NoError(t, err)

	tool := NewImageTool(nil)
	tool.workspaceDir = tmpDir

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": "images/test.jpg",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestImageTool_LoadFromDataURL_UnknownMimeType(t *testing.T) {
	tool := NewImageTool(nil)

	// Test with unknown MIME type
	unknownData := []byte{0x00, 0x01, 0x02, 0x03}
	b64Data := base64.StdEncoding.EncodeToString(unknownData)
	dataURL := "data:application/octet-stream;base64," + b64Data

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": dataURL,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "unknown", result.Data["image_format"])
}

func TestImageTool_LoadFromFile_UnknownExtension(t *testing.T) {
	tmpDir := t.TempDir()

	data := []byte{0x00, 0x01, 0x02, 0x03}
	imagePath := filepath.Join(tmpDir, "test.xyz")
	err := os.WriteFile(imagePath, data, 0644)
	require.NoError(t, err)

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": imagePath,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "unknown", result.Data["image_format"])
}

func TestImageTool_AnalyzeImage_JPEGDetection(t *testing.T) {
	tool := NewImageTool(nil)

	// Test JPEG magic bytes detection (variant E0)
	jpegE0Data := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	result, err := tool.analyzeImage(context.Background(), jpegE0Data, "test", nil)
	require.NoError(t, err)
	assert.Contains(t, result.Description, "JPEG")

	// Test JPEG magic bytes detection (variant E1 - EXIF)
	jpegE1Data := []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x10}
	result, err = tool.analyzeImage(context.Background(), jpegE1Data, "test", nil)
	require.NoError(t, err)
	assert.Contains(t, result.Description, "JPEG")
}

func TestImageTool_AnalyzeImage_PNGDetection(t *testing.T) {
	tool := NewImageTool(nil)

	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00}
	result, err := tool.analyzeImage(context.Background(), pngData, "test", nil)
	require.NoError(t, err)
	assert.Contains(t, result.Description, "PNG")
}

func TestImageTool_AnalyzeImage_SmallImage(t *testing.T) {
	tool := NewImageTool(nil)

	// Test with image too small for format detection
	smallData := []byte{0x00, 0x01, 0x02}
	result, err := tool.analyzeImage(context.Background(), smallData, "test", nil)
	require.NoError(t, err)
	assert.Contains(t, result.Description, "placeholder")
}

func TestImageTool_FormatAnalysisResult(t *testing.T) {
	tool := NewImageTool(nil)

	t.Run("basic result", func(t *testing.T) {
		result := &ImageAnalysisResult{
			Description: "A beautiful sunset over the ocean",
		}
		content := tool.formatAnalysisResult(result, "Describe what you see in this image")
		assert.Contains(t, content, "Image Analysis Result")
		assert.Contains(t, content, "A beautiful sunset over the ocean")
		assert.NotContains(t, content, "**Question:**")
	})

	t.Run("with custom prompt", func(t *testing.T) {
		result := &ImageAnalysisResult{
			Description: "There are 3 people in this image",
		}
		content := tool.formatAnalysisResult(result, "How many people are in this image?")
		assert.Contains(t, content, "**Question:** How many people are in this image?")
	})

	t.Run("with objects detected", func(t *testing.T) {
		result := &ImageAnalysisResult{
			Description: "A street scene",
			Objects:     []string{"car", "person", "tree"},
		}
		content := tool.formatAnalysisResult(result, "Describe what you see in this image")
		assert.Contains(t, content, "**Objects Detected:**")
		assert.Contains(t, content, "- car")
		assert.Contains(t, content, "- person")
		assert.Contains(t, content, "- tree")
	})

	t.Run("with extracted text", func(t *testing.T) {
		result := &ImageAnalysisResult{
			Description: "A sign",
			Text:        "STOP",
		}
		content := tool.formatAnalysisResult(result, "Describe what you see in this image")
		assert.Contains(t, content, "**Text Found:** STOP")
	})

	t.Run("with confidence", func(t *testing.T) {
		result := &ImageAnalysisResult{
			Description: "A cat",
			Confidence:  0.95,
		}
		content := tool.formatAnalysisResult(result, "Describe what you see in this image")
		assert.Contains(t, content, "**Confidence:** 95.0%")
	})

	t.Run("with all fields", func(t *testing.T) {
		result := &ImageAnalysisResult{
			Description: "A stop sign at an intersection",
			Objects:     []string{"stop sign", "road"},
			Text:        "STOP",
			Confidence:  0.98,
		}
		content := tool.formatAnalysisResult(result, "What is in this image?")
		assert.Contains(t, content, "**Question:**")
		assert.Contains(t, content, "**Description:**")
		assert.Contains(t, content, "**Objects Detected:**")
		assert.Contains(t, content, "**Text Found:**")
		assert.Contains(t, content, "**Confidence:**")
	})
}

func TestImageTool_Execute_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow server
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0})
	}))
	defer server.Close()

	tool := NewImageTool(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"image": server.URL + "/slow.jpg",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "failed to fetch image")
}

func TestImageTool_Execute_URLWithHTTPS(t *testing.T) {
	// Just verify HTTPS URLs are recognized as URLs
	tool := NewImageTool(nil)

	// We can't easily test HTTPS without a proper server, but we can verify
	// the URL detection works
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": "https://localhost:99999/unreachable.jpg",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	// The error should be about fetching, not about file not found
	assert.Contains(t, result.Error, "failed to fetch image")
}

func TestImageAnalysisResult_Metadata(t *testing.T) {
	tmpDir := t.TempDir()

	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	imagePath := filepath.Join(tmpDir, "test.jpg")
	err := os.WriteFile(imagePath, jpegData, 0644)
	require.NoError(t, err)

	tool := NewImageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": imagePath,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Check that result metadata is present
	analysisResult, ok := result.Data["result"].(*ImageAnalysisResult)
	require.True(t, ok)
	assert.NotNil(t, analysisResult.Metadata)
	assert.Contains(t, analysisResult.Metadata, "source")
	assert.Contains(t, analysisResult.Metadata, "path")
}

func TestImageTool_SelfTest(t *testing.T) {
	t.Run("with nil services", func(t *testing.T) {
		tool := NewImageTool(nil)
		result := tool.SelfTest(context.Background(), nil)

		require.NotNil(t, result)
		assert.True(t, result.IsFunctional())
		assert.NotEmpty(t, result.Message)
		assert.NotZero(t, result.TestedAt)
		assert.NotZero(t, result.TestDuration)
	})

	t.Run("with services", func(t *testing.T) {
		services := &types.ToolServices{
			WebClient: &http.Client{Timeout: 30 * time.Second},
		}
		tool := NewImageTool(services)
		result := tool.SelfTest(context.Background(), nil)

		require.NotNil(t, result)
		assert.Equal(t, types.SelfTestStatusOK, result.Status)
		assert.Contains(t, result.Capabilities, "load_from_file")
		assert.Contains(t, result.Capabilities, "load_from_url")
		assert.Contains(t, result.Capabilities, "load_from_data_url")
		assert.Contains(t, result.Capabilities, "format_detection")
	})

	t.Run("with verbose option", func(t *testing.T) {
		tool := NewImageTool(nil)
		opts := &types.SelfTestOptions{
			Verbose: true,
		}
		result := tool.SelfTest(context.Background(), opts)

		require.NotNil(t, result)
		assert.NotNil(t, result.Details)
		assert.Contains(t, result.Details, "workspace_dir")
		assert.Contains(t, result.Details, "supported_formats")
	})

	t.Run("with examples option", func(t *testing.T) {
		tool := NewImageTool(nil)
		opts := &types.SelfTestOptions{
			IncludeExamples: true,
		}
		result := tool.SelfTest(context.Background(), opts)

		require.NotNil(t, result)
		assert.NotEmpty(t, result.Examples)
		assert.GreaterOrEqual(t, len(result.Examples), 3)

		// Check first example has required fields
		ex := result.Examples[0]
		assert.NotEmpty(t, ex.Name)
		assert.NotEmpty(t, ex.Description)
		assert.NotNil(t, ex.Args)
	})

	t.Run("dependencies reported", func(t *testing.T) {
		tool := NewImageTool(nil)
		result := tool.SelfTest(context.Background(), nil)

		require.NotNil(t, result)
		assert.NotEmpty(t, result.Dependencies)

		// Should have HTTPClient and ToolServices dependencies
		depNames := make([]string, 0, len(result.Dependencies))
		for _, dep := range result.Dependencies {
			depNames = append(depNames, dep.Name)
		}
		assert.Contains(t, depNames, "HTTPClient")
		assert.Contains(t, depNames, "ToolServices")
	})
}
