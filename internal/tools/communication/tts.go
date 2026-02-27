package communication

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
)

// TTSTool converts text to speech audio files
type TTSTool struct {
	services     *types.ToolServices
	workspaceDir string
}

func NewTTSTool(services *types.ToolServices) *TTSTool {
	tool := &TTSTool{services: services}

	// Use a default workspace directory (this would need to be configured properly)
	tool.workspaceDir = "./workspace"

	return tool
}

func (t *TTSTool) Name() string {
	return "Tts"
}

func (t *TTSTool) Description() string {
	return "Convert text to speech and return a MEDIA path for audio files"
}

func (t *TTSTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to convert to speech",
			},
			"voice": map[string]interface{}{
				"type":        "string",
				"description": "Voice to use (e.g., en-US-AriaNeural, en-US-GuyNeural)",
				"default":     "en-US-AriaNeural",
			},
			"rate": map[string]interface{}{
				"type":        "string",
				"description": "Speech rate (e.g., +0%, +10%, -10%)",
				"default":     "+0%",
			},
			"format": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"mp3", "ogg", "wav"},
				"description": "Output audio format",
				"default":     "ogg",
			},
			"channel": map[string]interface{}{
				"type":        "string",
				"description": "Target channel ID for format optimization",
			},
		},
		"required": []string{"text"},
	}
}

func (t *TTSTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	text, ok := args["text"].(string)
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   "text parameter is required and must be a string",
		}, nil
	}

	if strings.TrimSpace(text) == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "text parameter cannot be empty",
		}, nil
	}

	voice := t.getStringArg(args, "voice", "en-US-AriaNeural")
	rate := t.getStringArg(args, "rate", "+0%")
	format := t.getStringArg(args, "format", "ogg")
	channel := t.getStringArg(args, "channel", "")

	// Optimize format for channel if specified
	if channel != "" {
		format = t.optimizeFormatForChannel(channel, format)
	}

	// Generate audio file
	audioPath, err := t.generateAudio(ctx, text, voice, rate, format)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to generate audio: %v", err),
		}, nil
	}

	// Get file info
	fileInfo, err := os.Stat(audioPath)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get file info: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("MEDIA: %s", audioPath),
		Data: map[string]interface{}{
			"audio_path": audioPath,
			"voice":      voice,
			"rate":       rate,
			"format":     format,
			"size":       fileInfo.Size(),
			"text":       text,
		},
	}, nil
}

// generateAudio generates audio using Edge TTS
func (t *TTSTool) generateAudio(ctx context.Context, text, voice, rate, format string) (string, error) {
	// Create temporary directory for audio files
	audioDir := filepath.Join(t.workspaceDir, "audio")
	if err := os.MkdirAll(audioDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create audio directory: %w", err)
	}

	// Generate unique filename
	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("tts_%s.%s", timestamp, format)
	audioPath := filepath.Join(audioDir, filename)

	// Check if edge-tts is available
	if !t.isEdgeTTSAvailable() {
		return "", fmt.Errorf("edge-tts is not available. Install with: pip install edge-tts")
	}

	// Build edge-tts command
	args := []string{
		"--voice", voice,
		"--rate", rate,
		"--text", text,
		"--write-media", audioPath,
	}

	// Add format-specific options
	switch format {
	case "mp3":
		args = append(args, "--write-media", strings.Replace(audioPath, ".mp3", ".mp3", 1))
	case "wav":
		args = append(args, "--write-media", strings.Replace(audioPath, ".wav", ".wav", 1))
	case "ogg":
		// Edge TTS outputs webm by default, we'll need to convert
		tempPath := strings.Replace(audioPath, ".ogg", ".webm", 1)
		args = append(args, "--write-media", tempPath)
	}

	// Execute edge-tts command
	cmd := exec.CommandContext(ctx, "edge-tts", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("edge-tts command failed: %v (output: %s)", err, string(output))
	}

	// Convert webm to ogg if needed
	if format == "ogg" {
		tempPath := strings.Replace(audioPath, ".ogg", ".webm", 1)
		if err := t.convertWebmToOgg(ctx, tempPath, audioPath); err != nil {
			return "", fmt.Errorf("failed to convert to ogg: %w", err)
		}
		// Clean up temporary webm file
		os.Remove(tempPath)
	}

	// Verify file was created
	if _, err := os.Stat(audioPath); err != nil {
		return "", fmt.Errorf("audio file was not created: %w", err)
	}

	return audioPath, nil
}

// isEdgeTTSAvailable checks if edge-tts is installed
func (t *TTSTool) isEdgeTTSAvailable() bool {
	cmd := exec.Command("edge-tts", "--help")
	return cmd.Run() == nil
}

// convertWebmToOgg converts webm audio to ogg using ffmpeg
func (t *TTSTool) convertWebmToOgg(ctx context.Context, inputPath, outputPath string) error {
	// Check if ffmpeg is available
	cmd := exec.Command("ffmpeg", "-version")
	if cmd.Run() != nil {
		return fmt.Errorf("ffmpeg is not available")
	}

	// Convert using ffmpeg
	cmd = exec.CommandContext(ctx, "ffmpeg",
		"-i", inputPath,
		"-c:a", "libvorbis",
		"-q:a", "3",
		"-y", // overwrite output file
		outputPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg conversion failed: %v (output: %s)", err, string(output))
	}

	return nil
}

// optimizeFormatForChannel optimizes audio format for specific channels
func (t *TTSTool) optimizeFormatForChannel(channel, defaultFormat string) string {
	switch strings.ToLower(channel) {
	case "telegram":
		// Telegram prefers OGG for voice messages
		return "ogg"
	case "discord":
		// Discord works well with MP3
		return "mp3"
	case "whatsapp":
		// WhatsApp prefers OGG
		return "ogg"
	default:
		return defaultFormat
	}
}

// getAvailableVoices returns a list of available voices (placeholder)
func (t *TTSTool) getAvailableVoices(ctx context.Context) ([]string, error) {
	// This would execute edge-tts --list-voices and parse the output
	cmd := exec.CommandContext(ctx, "edge-tts", "--list-voices")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list voices: %w", err)
	}

	// Parse voice list (simplified for now)
	lines := strings.Split(string(output), "\n")
	var voices []string
	for _, line := range lines {
		if strings.Contains(line, "Name:") {
			// Extract voice name from line
			parts := strings.Split(line, "Name: ")
			if len(parts) > 1 {
				voice := strings.TrimSpace(parts[1])
				if voice != "" {
					voices = append(voices, voice)
				}
			}
		}
	}

	return voices, nil
}

// Helper methods
func (t *TTSTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

// GetSchemaHints implements types.EnhancedSchemaProvider.
func (t *TTSTool) GetSchemaHints() map[string]schema.SchemaHints {
	return map[string]schema.SchemaHints{
		"text": {
			Examples: []interface{}{
				"Hello, how can I help you today?",
				"The weather is sunny with temperatures reaching 75 degrees.",
				"Task completed successfully!",
			},
			ValidationHints: []string{
				"Text cannot be empty or whitespace only",
				"Long text may take longer to process",
				"Special characters and punctuation affect pronunciation",
			},
		},
		"voice": {
			Examples: []interface{}{
				"en-US-AriaNeural",
				"en-US-GuyNeural",
				"en-US-JennyNeural",
				"en-GB-SoniaNeural",
				"fr-FR-DeniseNeural",
			},
			ValidationHints: []string{
				"Default voice is en-US-AriaNeural",
				"Use edge-tts --list-voices to see all available voices",
				"Voice determines language and accent",
			},
		},
		"rate": {
			Examples: []interface{}{
				"+0%",
				"+10%",
				"-10%",
				"+25%",
				"-25%",
			},
			ValidationHints: []string{
				"Positive values speed up speech",
				"Negative values slow down speech",
				"Range typically -50% to +100%",
			},
		},
		"format": {
			Examples: []interface{}{"ogg", "mp3", "wav"},
			ValidationHints: []string{
				"ogg: Best for Telegram voice messages",
				"mp3: Universal compatibility",
				"wav: Highest quality, larger files",
			},
		},
		"channel": {
			DiscoveryType:     "channels",
			EnumFromDiscovery: false,
			Examples: []interface{}{
				"telegram",
				"discord",
				"1098302846",
			},
			ValidationHints: []string{
				"Automatically optimizes format for channel",
				"telegram -> ogg, discord -> mp3",
				"Channel must be available for optimization",
			},
		},
	}
}
