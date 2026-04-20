package vision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockVisionAnalyzer is a test double for types.VisionAnalyzer. It records
// the last call and returns a canned response or error.
type mockVisionAnalyzer struct {
	mu         sync.Mutex
	calls      int
	lastImage  []byte
	lastMedia  string
	lastPrompt string
	response   string
	err        error
}

func (m *mockVisionAnalyzer) AnalyzeImage(ctx context.Context, image []byte, mediaType, prompt string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastImage = append([]byte(nil), image...)
	m.lastMedia = mediaType
	m.lastPrompt = prompt
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

func TestImageTool_AnalyzeImage_RoutesThroughVisionAnalyzer(t *testing.T) {
	mock := &mockVisionAnalyzer{response: "A photo of a cat on a sunny windowsill."}
	services := &types.ToolServices{Vision: mock}
	tool := NewImageTool(services)

	// JPEG magic bytes so detectMediaType reports image/jpeg.
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}

	result, err := tool.analyzeImage(context.Background(), jpegData, "What is in this image?", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "A photo of a cat on a sunny windowsill.", result.Description)
	assert.Equal(t, float64(1.0), result.Confidence)
	assert.Equal(t, "vision_analyzer", result.Metadata["service"])
	assert.Equal(t, "image/jpeg", result.Metadata["media_type"])

	assert.Equal(t, 1, mock.calls)
	assert.Equal(t, jpegData, mock.lastImage)
	assert.Equal(t, "image/jpeg", mock.lastMedia)
	assert.Equal(t, "What is in this image?", mock.lastPrompt)
}

func TestImageTool_AnalyzeImage_VisionAnalyzerError(t *testing.T) {
	mock := &mockVisionAnalyzer{err: errors.New("vision: provider unavailable")}
	services := &types.ToolServices{Vision: mock}
	tool := NewImageTool(services)

	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00}

	result, err := tool.analyzeImage(context.Background(), pngData, "describe", nil)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "provider unavailable")
}

func TestImageTool_AnalyzeImage_FallbackWhenServiceNil(t *testing.T) {
	// No Vision service wired — should return the placeholder response and
	// still detect JPEG/PNG magic bytes for backwards compatibility.
	tool := NewImageTool(nil)

	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}
	result, err := tool.analyzeImage(context.Background(), jpegData, "test", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "placeholder", result.Metadata["service"])
	assert.Contains(t, result.Description, "JPEG image detected")
	assert.Contains(t, result.Description, "placeholder")
}

func TestImageTool_Execute_UsesVisionAnalyzer(t *testing.T) {
	// End-to-end: tool.Execute with a real file should route the loaded bytes
	// through the wired VisionAnalyzer and surface its response in Content.
	tmpDir := t.TempDir()
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}
	imagePath := filepath.Join(tmpDir, "test.jpg")
	require.NoError(t, os.WriteFile(imagePath, jpegData, 0o644))

	mock := &mockVisionAnalyzer{response: "Yes, there is a cat."}
	services := &types.ToolServices{Vision: mock}
	tool := NewImageTool(services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image":  imagePath,
		"prompt": "Is there a cat?",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Yes, there is a cat.")
	assert.NotContains(t, result.Content, "placeholder")

	// Verify the analyzer saw the correct media_type (derived from the .jpg extension).
	assert.Equal(t, "image/jpeg", mock.lastMedia)
	assert.Equal(t, "Is there a cat?", mock.lastPrompt)
}

func TestImageTool_SelfTest_VisionAnalyzerDependency(t *testing.T) {
	t.Run("ok when wired", func(t *testing.T) {
		services := &types.ToolServices{Vision: &mockVisionAnalyzer{}}
		tool := NewImageTool(services)
		result := tool.SelfTest(context.Background(), nil)

		require.NotNil(t, result)
		assert.Equal(t, types.SelfTestStatusOK, result.Status)
		assert.Contains(t, result.Capabilities, "vision_analysis")
		assert.NotContains(t, result.UnavailableCapabilities, "vision_analysis")

		// VisionAnalyzer dependency reported available.
		var found bool
		for _, d := range result.Dependencies {
			if d.Name == "VisionAnalyzer" {
				found = true
				assert.True(t, d.Available)
			}
		}
		assert.True(t, found, "VisionAnalyzer dependency should be reported")
	})

	t.Run("degraded when missing", func(t *testing.T) {
		tool := NewImageTool(&types.ToolServices{}) // Vision nil
		result := tool.SelfTest(context.Background(), nil)

		require.NotNil(t, result)
		assert.Equal(t, types.SelfTestStatusDegraded, result.Status)
		assert.True(t, result.IsFunctional(), "degraded tools are still functional")
		assert.Contains(t, result.UnavailableCapabilities, "vision_analysis")
		assert.NotEmpty(t, result.Suggestions)

		var found bool
		for _, d := range result.Dependencies {
			if d.Name == "VisionAnalyzer" {
				found = true
				assert.False(t, d.Available)
			}
		}
		assert.True(t, found, "VisionAnalyzer dependency should be reported")
	})
}

func TestDetectMediaType(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		hint   string
		expect string
	}{
		{"jpeg hint", []byte{0x00}, "jpeg", "image/jpeg"},
		{"png hint", []byte{0x00}, "png", "image/png"},
		{"gif hint", []byte{0x00}, "gif", "image/gif"},
		{"webp hint", []byte{0x00}, "webp", "image/webp"},
		{"jpeg magic e0", []byte{0xFF, 0xD8, 0xFF, 0xE0}, "", "image/jpeg"},
		{"jpeg magic e1", []byte{0xFF, 0xD8, 0xFF, 0xE1}, "", "image/jpeg"},
		{"png magic", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, "", "image/png"},
		{"gif89a magic", []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61}, "", "image/gif"},
		{"webp magic", []byte{0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0, 0x57, 0x45, 0x42, 0x50}, "", "image/webp"},
		{"unknown", []byte{0x01, 0x02, 0x03}, "", ""},
		{"unknown hint falls back to magic", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, "unknown", "image/png"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expect, detectMediaType(tc.data, tc.hint))
		})
	}
}
