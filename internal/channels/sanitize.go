package channels

import (
	"regexp"
	"strings"

	"conduit/internal/constants"
)

// MediaLineRe matches lines starting with "MEDIA:" followed by a file path.
// These are internal control signals (e.g. from TTS) that should not appear in user output.
var MediaLineRe = regexp.MustCompile(`(?m)^MEDIA:\s*\S+.*$`)

// excessiveNewlines matches three or more consecutive newlines for collapsing.
var excessiveNewlines = regexp.MustCompile(`\n{3,}`)

// trailingSilentTokenRe matches trailing silent tokens (NO_REPLY, HEARTBEAT_OK) with optional whitespace.
var trailingSilentTokenRe = regexp.MustCompile(`(?i)\s*(NO_REPLY|HEARTBEAT_OK)\s*$`)

// SanitizeOutgoingText strips all internal signal markers from text before it
// reaches a user-facing output path. It removes reply tags, MEDIA: lines, and
// collapses excessive blank lines.
func SanitizeOutgoingText(text string) string {
	if text == "" {
		return text
	}

	// Strip reply tags (reuses the package-level compiled regex)
	cleaned := ReplyTagRe.ReplaceAllString(text, "")

	// Strip MEDIA: lines
	cleaned = MediaLineRe.ReplaceAllString(cleaned, "")

	// Collapse three-or-more consecutive newlines down to two
	cleaned = excessiveNewlines.ReplaceAllString(cleaned, "\n\n")

	// Strip trailing silent tokens (NO_REPLY, HEARTBEAT_OK)
	cleaned = stripTrailingSilentTokens(cleaned)

	return strings.TrimSpace(cleaned)
}

// IsSilentResponse returns true if the content is a silent token
// (NO_REPLY or HEARTBEAT_OK), meaning the response should not be
// delivered to the user. Exact match after trimming, or contains-match
// only for short responses (≤40 chars) to tolerate minor LLM wrapping
// like "OK. NO_REPLY". Long responses that merely reference the token
// are not suppressed.
func IsSilentResponse(content string) bool {
	upper := strings.ToUpper(strings.TrimSpace(content))
	if upper == constants.SilentReplyToken || upper == constants.HeartbeatOKToken {
		return true
	}
	// Allow short wrapped responses like "OK. NO_REPLY" but not long
	// responses that merely reference the token.
	if len(upper) <= 40 {
		return strings.Contains(upper, constants.SilentReplyToken) || strings.Contains(upper, constants.HeartbeatOKToken)
	}
	return false
}

// stripTrailingSilentTokens removes trailing NO_REPLY or HEARTBEAT_OK tokens
// from text, including any leading whitespace before the token.
func stripTrailingSilentTokens(text string) string {
	return trailingSilentTokenRe.ReplaceAllString(text, "")
}
