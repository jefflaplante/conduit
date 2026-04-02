package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// MediaType represents the type of media to send
type MediaType string

const (
	MediaTypeVoice MediaType = "voice" // Voice message (OGG/OPUS)
	MediaTypeAudio MediaType = "audio" // Audio file (MP3/M4A)
	MediaTypeTTS   MediaType = "tts"   // Text-to-speech (placeholder for now)
	MediaTypePath  MediaType = "path"  // Local file path
)

// MediaLine represents a parsed MEDIA protocol line
type MediaLine struct {
	Type    MediaType // voice, audio, tts, path
	Content string    // base64 data, URL, text, or file path
	Caption string    // optional caption for the media
}

// mediaLineRe matches MEDIA protocol lines with format:
// MEDIA:type:content
// MEDIA:type:content|caption
var mediaLineRe = regexp.MustCompile(`(?m)^MEDIA:(\w+):(.+?)(?:\|(.*))?$`)

// ParseMediaLines extracts all MEDIA protocol lines from text
// Returns the parsed media lines and the remaining text with MEDIA lines removed
func ParseMediaLines(text string) ([]MediaLine, string) {
	var mediaLines []MediaLine

	matches := mediaLineRe.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) >= 3 {
			mediaType := strings.ToLower(match[1])
			content := strings.TrimSpace(match[2])
			caption := ""
			if len(match) >= 4 {
				caption = strings.TrimSpace(match[3])
			}

			mediaLines = append(mediaLines, MediaLine{
				Type:    MediaType(mediaType),
				Content: content,
				Caption: caption,
			})
		}
	}

	// Remove MEDIA lines from text
	remainingText := mediaLineRe.ReplaceAllString(text, "")

	// Also handle the existing simple "MEDIA: /path" format from TTS tool
	simpleMediaRe := regexp.MustCompile(`(?m)^MEDIA:\s+(/[^\n]+)$`)
	simpleMatches := simpleMediaRe.FindAllStringSubmatch(remainingText, -1)
	for _, match := range simpleMatches {
		if len(match) >= 2 {
			mediaLines = append(mediaLines, MediaLine{
				Type:    MediaTypePath,
				Content: strings.TrimSpace(match[1]),
			})
		}
	}
	remainingText = simpleMediaRe.ReplaceAllString(remainingText, "")

	// Collapse excessive newlines
	multiNewline := regexp.MustCompile(`\n{3,}`)
	remainingText = multiNewline.ReplaceAllString(remainingText, "\n\n")
	remainingText = strings.TrimSpace(remainingText)

	return mediaLines, remainingText
}

// MediaSender handles sending media messages via Telegram
type MediaSender struct {
	bot         botAPI
	ctx         context.Context
	httpClient  *http.Client
	ttsProvider TTSProvider
}

// TTSProvider interface for text-to-speech conversion
// This is a placeholder that can be implemented later with actual TTS services
type TTSProvider interface {
	// GenerateAudio converts text to audio and returns the path to the audio file
	GenerateAudio(ctx context.Context, text string) (string, error)
}

// NewMediaSender creates a new MediaSender instance
func NewMediaSender(bot botAPI, ctx context.Context) *MediaSender {
	return &MediaSender{
		bot: bot,
		ctx: ctx,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SetTTSProvider sets the TTS provider for text-to-speech conversion
func (s *MediaSender) SetTTSProvider(provider TTSProvider) {
	s.ttsProvider = provider
}

// SendMedia sends a media line to the specified chat
// Returns an error message to append to the text response, or empty string on success
func (s *MediaSender) SendMedia(chatID int64, media MediaLine) (string, error) {
	switch media.Type {
	case MediaTypeVoice:
		return s.sendVoiceBase64(chatID, media.Content, media.Caption)
	case MediaTypeAudio:
		return s.sendAudioFromURL(chatID, media.Content, media.Caption)
	case MediaTypePath:
		return s.sendVoiceFromPath(chatID, media.Content, media.Caption)
	case MediaTypeTTS:
		return s.sendTTS(chatID, media.Content, media.Caption)
	default:
		return fmt.Sprintf("[Unsupported media type: %s]", media.Type), fmt.Errorf("unsupported media type: %s", media.Type)
	}
}

// sendVoiceBase64 sends voice message from base64 encoded audio data
func (s *MediaSender) sendVoiceBase64(chatID int64, base64Data, caption string) (string, error) {
	// Decode base64 data
	audioData, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		log.Printf("[Telegram/Media] Failed to decode base64 audio: %v", err)
		return "[Voice message failed: invalid audio data]", err
	}

	params := &bot.SendVoiceParams{
		ChatID:    chatID,
		Voice:     &models.InputFileUpload{Filename: "voice.ogg", Data: bytes.NewReader(audioData)},
		Caption:   caption,
		ParseMode: models.ParseModeMarkdownV1,
	}

	_, err = s.bot.SendVoice(s.ctx, params)
	if err != nil {
		log.Printf("[Telegram/Media] Failed to send voice message: %v", err)
		return "[Voice message failed to send]", err
	}

	log.Printf("[Telegram/Media] Voice message sent to chat %d (%d bytes)", chatID, len(audioData))
	return "", nil
}

// sendVoiceFromPath sends voice message from a local file path
func (s *MediaSender) sendVoiceFromPath(chatID int64, filePath, caption string) (string, error) {
	// Read the audio file
	audioData, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("[Telegram/Media] Failed to read audio file %s: %v", filePath, err)
		return "[Voice message failed: could not read audio file]", err
	}

	// Determine filename and extension
	filename := filepath.Base(filePath)
	ext := strings.ToLower(filepath.Ext(filePath))

	// Voice messages should be OGG/OPUS format for Telegram
	if ext == ".ogg" || ext == ".opus" || ext == ".oga" {
		params := &bot.SendVoiceParams{
			ChatID:    chatID,
			Voice:     &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(audioData)},
			Caption:   caption,
			ParseMode: models.ParseModeMarkdownV1,
		}

		_, err = s.bot.SendVoice(s.ctx, params)
		if err != nil {
			log.Printf("[Telegram/Media] Failed to send voice message: %v", err)
			return "[Voice message failed to send]", err
		}

		log.Printf("[Telegram/Media] Voice message sent from %s to chat %d", filename, chatID)
		return "", nil
	}

	// For other formats (mp3, wav, m4a), send as audio file instead
	params := &bot.SendAudioParams{
		ChatID:    chatID,
		Audio:     &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(audioData)},
		Caption:   caption,
		ParseMode: models.ParseModeMarkdownV1,
	}

	_, err = s.bot.SendAudio(s.ctx, params)
	if err != nil {
		log.Printf("[Telegram/Media] Failed to send audio file: %v", err)
		return "[Audio message failed to send]", err
	}

	log.Printf("[Telegram/Media] Audio message sent from %s to chat %d", filename, chatID)
	return "", nil
}

// sendAudioFromURL sends audio message from a URL
func (s *MediaSender) sendAudioFromURL(chatID int64, url, caption string) (string, error) {
	// For URLs, Telegram can fetch directly using InputFileString
	params := &bot.SendAudioParams{
		ChatID:    chatID,
		Audio:     &models.InputFileString{Data: url},
		Caption:   caption,
		ParseMode: models.ParseModeMarkdownV1,
	}

	_, err := s.bot.SendAudio(s.ctx, params)
	if err != nil {
		// Fallback: download and upload
		log.Printf("[Telegram/Media] Direct URL failed, attempting download: %v", err)
		return s.sendAudioFromURLFallback(chatID, url, caption)
	}

	log.Printf("[Telegram/Media] Audio sent from URL to chat %d", chatID)
	return "", nil
}

// sendAudioFromURLFallback downloads audio from URL and uploads it
func (s *MediaSender) sendAudioFromURLFallback(chatID int64, url, caption string) (string, error) {
	resp, err := s.httpClient.Get(url)
	if err != nil {
		log.Printf("[Telegram/Media] Failed to download audio from URL: %v", err)
		return "[Audio message failed: could not download]", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[Telegram/Media] Failed to download audio, status: %d", resp.StatusCode)
		return "[Audio message failed: download error]", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[Telegram/Media] Failed to read audio data: %v", err)
		return "[Audio message failed: read error]", err
	}

	// Extract filename from URL
	filename := filepath.Base(url)
	if filename == "" || filename == "." {
		filename = "audio.mp3"
	}

	params := &bot.SendAudioParams{
		ChatID:    chatID,
		Audio:     &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(audioData)},
		Caption:   caption,
		ParseMode: models.ParseModeMarkdownV1,
	}

	_, err = s.bot.SendAudio(s.ctx, params)
	if err != nil {
		log.Printf("[Telegram/Media] Failed to send audio: %v", err)
		return "[Audio message failed to send]", err
	}

	log.Printf("[Telegram/Media] Audio sent (fallback) to chat %d (%d bytes)", chatID, len(audioData))
	return "", nil
}

// sendTTS converts text to speech and sends as voice message
func (s *MediaSender) sendTTS(chatID int64, text, caption string) (string, error) {
	if s.ttsProvider == nil {
		// TTS not configured - return a message indicating this
		log.Printf("[Telegram/Media] TTS requested but no provider configured")
		return "[TTS not available]", fmt.Errorf("TTS provider not configured")
	}

	// Generate audio using TTS provider
	audioPath, err := s.ttsProvider.GenerateAudio(s.ctx, text)
	if err != nil {
		log.Printf("[Telegram/Media] TTS generation failed: %v", err)
		return "[TTS generation failed]", err
	}

	// Send the generated audio as voice message
	return s.sendVoiceFromPath(chatID, audioPath, caption)
}

// ProcessAndSendMedia processes a response text for MEDIA lines,
// sends any media found, and returns the cleaned text
func (s *MediaSender) ProcessAndSendMedia(chatID int64, text string) (string, []string) {
	mediaLines, cleanedText := ParseMediaLines(text)

	var errors []string
	for _, media := range mediaLines {
		errMsg, err := s.SendMedia(chatID, media)
		if err != nil {
			errors = append(errors, errMsg)
		}
	}

	return cleanedText, errors
}

// botAPI extension for media methods
// These are added to the existing botAPI interface

// mediaBot extends botAPI with media sending methods
type mediaBot interface {
	botAPI
	SendVoice(ctx context.Context, params *bot.SendVoiceParams) (*models.Message, error)
	SendAudio(ctx context.Context, params *bot.SendAudioParams) (*models.Message, error)
}
