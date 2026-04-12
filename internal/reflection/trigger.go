package reflection

import (
	"strings"
	"unicode"
)

// TriggerType identifies what caused a session-end reflection to fire.
type TriggerType int

const (
	TriggerNone     TriggerType = iota // No trigger detected
	TriggerFarewell                    // Natural language farewell
	TriggerCommand                     // Slash command (/goodbye, /end, /reset)
)

// String returns a human-readable label for the trigger type.
func (t TriggerType) String() string {
	switch t {
	case TriggerFarewell:
		return "farewell"
	case TriggerCommand:
		return "command"
	default:
		return "none"
	}
}

// FarewellDetector performs conservative detection of farewell messages and
// session-end slash commands. It is safe for concurrent use.
type FarewellDetector struct {
	// farewellPhrases holds canonical farewell phrases sorted longest-first
	// so that multi-word phrases match before their single-word prefixes
	// (e.g. "good night" before "good").
	farewellPhrases []string

	// sessionEndCommands are the slash commands that trigger reflection.
	sessionEndCommands map[string]bool
}

// NewFarewellDetector creates a FarewellDetector pre-loaded with the
// canonical farewell patterns and session-end commands from the SPAR spec.
func NewFarewellDetector() *FarewellDetector {
	// Ordered longest-first so prefix matching prefers longer phrases.
	phrases := []string{
		"thank you that's all",
		"thanks that's it",
		"thanks i'm done",
		"see you later",
		"signing off",
		"logging off",
		"end session",
		"talk later",
		"good night",
		"goodnight",
		"all done",
		"we're done",
		"that's all",
		"that's it",
		"see ya",
		"goodbye",
		"bye",
	}

	commands := map[string]bool{
		"/goodbye": true,
		"/end":     true,
		"/reset":   true,
	}

	return &FarewellDetector{
		farewellPhrases:    phrases,
		sessionEndCommands: commands,
	}
}

// IsFarewell returns true when the message is primarily a farewell.
// Matching is conservative: the farewell phrase must appear at the very
// start of the (trimmed, lowercased) message, or the entire message must
// consist of only the farewell phrase (possibly followed by punctuation
// or a trailing clause like ", thanks for the help").
//
// Mid-sentence occurrences like "I need to say goodbye to the old API"
// do NOT match.
func (d *FarewellDetector) IsFarewell(message string) bool {
	normalized := stripTrailingPunctuation(strings.ToLower(strings.TrimSpace(message)))
	if normalized == "" {
		return false
	}

	for _, phrase := range d.farewellPhrases {
		if normalized == phrase {
			return true
		}
		// Check prefix: the farewell phrase must be at the very start of
		// the message and followed by a word boundary (space, comma,
		// exclamation mark, period, etc.) — not a letter.
		if strings.HasPrefix(normalized, phrase) {
			rest := normalized[len(phrase):]
			if len(rest) > 0 && isWordBoundary(rune(rest[0])) {
				return true
			}
		}
	}

	return false
}

// IsSessionEndCommand returns true when the message is a slash command
// that explicitly ends or resets the session (/goodbye, /end, /reset).
func (d *FarewellDetector) IsSessionEndCommand(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	return d.sessionEndCommands[normalized]
}

// ShouldTriggerReflection combines farewell and command detection.
// It returns whether reflection should fire and which trigger type matched.
// Command detection is checked first (higher confidence).
func (d *FarewellDetector) ShouldTriggerReflection(message string) (bool, TriggerType) {
	if d.IsSessionEndCommand(message) {
		return true, TriggerCommand
	}
	if d.IsFarewell(message) {
		return true, TriggerFarewell
	}
	return false, TriggerNone
}

// stripTrailingPunctuation removes trailing punctuation characters
// (periods, exclamation marks, question marks, ellipsis) from the
// already-lowercased string. This lets "Goodbye!" match "goodbye".
func stripTrailingPunctuation(s string) string {
	return strings.TrimRightFunc(s, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
}

// isWordBoundary returns true if the rune is not a letter or digit —
// i.e. it's whitespace, punctuation, or similar.
func isWordBoundary(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}
