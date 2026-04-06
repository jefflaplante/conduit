package communication

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	toolargs "conduit/internal/tools/args"
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

	voice := toolargs.GetString(args, "voice", "en-US-AriaNeural")
	rate := toolargs.GetString(args, "rate", "+0%")
	format := toolargs.GetString(args, "format", "ogg")
	channel := toolargs.GetString(args, "channel", "")

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

// GetUsageExamples implements types.UsageExampleProvider.
func (t *TTSTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "Basic TTS",
			Description: "Convert text to speech with default settings",
			Args: map[string]interface{}{
				"text": "Hello, how can I help you today?",
			},
			Expected: "Generates OGG audio file with default voice",
		},
		{
			Name:        "TTS with custom voice",
			Description: "Use a different voice for speech synthesis",
			Args: map[string]interface{}{
				"text":  "Good morning! The weather looks great today.",
				"voice": "en-US-GuyNeural",
			},
			Expected: "Generates audio with male voice",
		},
		{
			Name:        "TTS for Telegram",
			Description: "Generate audio optimized for Telegram voice messages",
			Args: map[string]interface{}{
				"text":    "Task completed successfully!",
				"channel": "telegram",
			},
			Expected: "Generates OGG audio optimized for Telegram",
		},
		{
			Name:        "TTS with adjusted rate",
			Description: "Generate slower speech for clarity",
			Args: map[string]interface{}{
				"text": "This is an important announcement.",
				"rate": "-10%",
			},
			Expected: "Generates audio at 90% normal speed",
		},
	}
}

// SelfTest implements types.SelfTester for TTSTool.
func (t *TTSTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Capabilities: []string{},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check edge-tts availability
	edgeTTSDep := types.DependencyStatus{
		Name:     "edge-tts",
		Required: true,
	}

	if t.isEdgeTTSAvailable() {
		edgeTTSDep.Available = true
		edgeTTSDep.Status = "installed"
		result.Capabilities = append(result.Capabilities, "text-to-speech")
	} else {
		edgeTTSDep.Available = false
		edgeTTSDep.Status = "not_installed"
		edgeTTSDep.Message = "edge-tts command not found"
		result.Status = types.SelfTestStatusFailed
		result.Message = "TTS tool requires edge-tts which is not installed"
		result.Suggestions = []string{
			"Install edge-tts: pip install edge-tts",
			"Verify Python pip is available",
		}
	}
	deps = append(deps, edgeTTSDep)

	// Check ffmpeg availability (for OGG conversion)
	ffmpegDep := types.DependencyStatus{
		Name:     "ffmpeg",
		Required: false,
	}

	cmd := exec.Command("ffmpeg", "-version")
	if cmd.Run() == nil {
		ffmpegDep.Available = true
		ffmpegDep.Status = "installed"
		result.Capabilities = append(result.Capabilities, "ogg-conversion")
	} else {
		ffmpegDep.Available = false
		ffmpegDep.Status = "not_installed"
		ffmpegDep.Message = "ffmpeg not found; OGG format will not work"

		// Only degrade if edge-tts is available
		if result.Status == types.SelfTestStatusOK {
			result.Status = types.SelfTestStatusDegraded
			result.Message = "TTS available but OGG conversion requires ffmpeg"
			result.UnavailableCapabilities = append(result.UnavailableCapabilities, "ogg-conversion")
			result.Suggestions = []string{
				"Install ffmpeg for OGG format support",
				"Use mp3 or wav format instead",
			}
		}
	}
	deps = append(deps, ffmpegDep)

	// Check workspace directory
	workspaceDep := types.DependencyStatus{
		Name:     "workspace",
		Required: true,
	}

	audioDir := filepath.Join(t.workspaceDir, "audio")
	if err := os.MkdirAll(audioDir, 0755); err != nil {
		workspaceDep.Available = false
		workspaceDep.Status = "error"
		workspaceDep.Message = fmt.Sprintf("cannot create audio directory: %v", err)

		if result.Status != types.SelfTestStatusFailed {
			result.Status = types.SelfTestStatusFailed
			result.Message = "Cannot create workspace directory for audio files"
			result.Suggestions = append(result.Suggestions,
				"Check workspace directory permissions",
				fmt.Sprintf("Ensure %s is writable", t.workspaceDir),
			)
		}
	} else {
		workspaceDep.Available = true
		workspaceDep.Status = "writable"
		workspaceDep.Message = audioDir
	}
	deps = append(deps, workspaceDep)

	// Set final message if not already set
	if result.Message == "" {
		if result.Status == types.SelfTestStatusOK {
			result.Message = "TTS tool fully functional"
		}
	}

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.Verbose {
		result.Details = map[string]interface{}{
			"workspace_dir": t.workspaceDir,
			"audio_dir":     audioDir,
		}
	}

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = t.GetUsageExamples()
	}

	return result
}
