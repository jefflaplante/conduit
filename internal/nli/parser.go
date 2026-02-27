package nli

import (
	"regexp"
	"strings"
)

// ActionType represents the kind of operation the user intends.
type ActionType string

const (
	ActionQuery   ActionType = "query"
	ActionCommand ActionType = "command"
	ActionCreate  ActionType = "create"
	ActionModify  ActionType = "modify"
	ActionDelete  ActionType = "delete"
)

// EntityType classifies extracted entities.
type EntityType string

const (
	EntityFilePath   EntityType = "file_path"
	EntityURL        EntityType = "url"
	EntityModelName  EntityType = "model_name"
	EntitySession    EntityType = "session"
	EntityToolName   EntityType = "tool_name"
	EntitySearchTerm EntityType = "search_term"
	EntityChannel    EntityType = "channel"
	EntityGeneric    EntityType = "generic"
)

// Entity represents a recognized entity extracted from natural language.
type Entity struct {
	Type       EntityType `json:"type"`
	Value      string     `json:"value"`
	StartIndex int        `json:"start_index"`
	EndIndex   int        `json:"end_index"`
}

// Intent represents a parsed user intent from a natural language message.
type Intent struct {
	Action     ActionType             `json:"action"`
	Target     string                 `json:"target"`
	Parameters map[string]interface{} `json:"parameters"`
	Entities   []Entity               `json:"entities"`
	Confidence float64                `json:"confidence"`
	RawText    string                 `json:"raw_text"`
}

// IntentParser parses user messages into structured intents using regex and
// keyword analysis. No external NLP dependencies are required.
type IntentParser struct {
	actionKeywords map[ActionType][]string
	entityPatterns map[EntityType]*regexp.Regexp
	toolNames      map[string]bool
	modelNames     map[string]bool
	splitPatterns  []*regexp.Regexp
	conjunctionPat *regexp.Regexp
	sequentialPat  *regexp.Regexp
}

// NewIntentParser creates a new IntentParser with default patterns.
func NewIntentParser() *IntentParser {
	p := &IntentParser{
		actionKeywords: map[ActionType][]string{
			ActionQuery: {
				"search", "find", "look up", "lookup", "what", "who", "where",
				"when", "how", "show", "list", "get", "fetch", "retrieve",
				"display", "check", "tell me", "describe", "explain",
			},
			ActionCommand: {
				"run", "execute", "start", "stop", "restart", "enable",
				"disable", "schedule", "cancel", "send", "trigger",
				"deploy", "build", "test", "backup", "restore",
			},
			ActionCreate: {
				"create", "make", "add", "new", "generate", "write",
				"compose", "set up", "setup", "initialize", "init",
			},
			ActionModify: {
				"update", "change", "modify", "edit", "rename", "move",
				"replace", "configure", "adjust", "set", "fix", "patch",
			},
			ActionDelete: {
				"delete", "remove", "clear", "purge", "drop", "destroy",
				"clean", "erase", "unset",
			},
		},
		entityPatterns: map[EntityType]*regexp.Regexp{
			EntityFilePath:  regexp.MustCompile(`(?:^|[\s"'` + "`" + `])(/[^\s"'` + "`" + `]+|[a-zA-Z]:\\[^\s"'` + "`" + `]+|\.{1,2}/[^\s"'` + "`" + `]+)`),
			EntityURL:       regexp.MustCompile(`https?://[^\s"'` + "`" + `<>]+`),
			EntityModelName: regexp.MustCompile(`\b(claude-(?:opus|sonnet|haiku)[-\w]*|gpt-[\w.-]+|gemini[-\w]*)\b`),
			EntitySession:   regexp.MustCompile(`\bsession\s*[:=]\s*([a-zA-Z0-9_-]+)\b`),
		},
		toolNames: map[string]bool{
			"web_search": true, "web_fetch": true, "memory_search": true,
			"read_file": true, "write_file": true, "list_files": true,
			"edit_file": true, "exec": true, "send_message": true,
			"schedule_job": true, "image_analysis": true, "gateway_control": true,
			"context_management": true, "session_manager": true,
		},
		modelNames: map[string]bool{
			"claude-opus-4": true, "claude-sonnet-4": true, "claude-haiku": true,
			"gpt-4": true, "gpt-4o": true, "gemini-pro": true,
		},
	}

	// Compile split patterns for multi-step detection.
	p.splitPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bthen\b`),
		regexp.MustCompile(`(?i)\bafter that\b`),
		regexp.MustCompile(`(?i)\bnext\b`),
		regexp.MustCompile(`(?i)\bfollowed by\b`),
		regexp.MustCompile(`(?i)\bfinally\b`),
		regexp.MustCompile(`(?i)\bfirst\b.*?\bthen\b`),
	}
	p.conjunctionPat = regexp.MustCompile(`(?i)\b(and also|and then|and)\b`)
	p.sequentialPat = regexp.MustCompile(`(?i)^\s*\d+[.)]\s+`)

	return p
}

// Parse extracts a single Intent from a user message.
func (p *IntentParser) Parse(message string) *Intent {
	message = strings.TrimSpace(message)
	if message == "" {
		return &Intent{
			Action:     ActionQuery,
			Confidence: 0,
			RawText:    message,
			Parameters: make(map[string]interface{}),
		}
	}

	action, actionConfidence := p.detectAction(message)
	entities := p.extractEntities(message)
	target := p.inferTarget(message, entities)
	params := p.extractParameters(message, entities)

	return &Intent{
		Action:     action,
		Target:     target,
		Parameters: params,
		Entities:   entities,
		Confidence: actionConfidence,
		RawText:    message,
	}
}

// ParseMultiStep breaks a compound request into multiple sequential intents.
func (p *IntentParser) ParseMultiStep(message string) []Intent {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}

	parts := p.splitCompoundMessage(message)
	if len(parts) == 0 {
		intent := p.Parse(message)
		return []Intent{*intent}
	}

	intents := make([]Intent, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		intent := p.Parse(part)
		intents = append(intents, *intent)
	}

	if len(intents) == 0 {
		intent := p.Parse(message)
		return []Intent{*intent}
	}

	return intents
}

// detectAction determines the ActionType and a confidence score.
func (p *IntentParser) detectAction(message string) (ActionType, float64) {
	lower := strings.ToLower(message)

	bestAction := ActionQuery
	bestScore := 0.0

	for action, keywords := range p.actionKeywords {
		for _, kw := range keywords {
			idx := strings.Index(lower, kw)
			if idx == -1 {
				continue
			}
			// Compute a score: exact word boundary matches score higher,
			// earlier position scores higher.
			score := p.keywordScore(lower, kw, idx)
			if score > bestScore {
				bestScore = score
				bestAction = action
			}
		}
	}

	// Clamp confidence to [0, 1].
	confidence := bestScore
	if confidence > 1.0 {
		confidence = 1.0
	}
	// If nothing matched, return query with low confidence.
	if bestScore == 0 {
		// Check for question marks as a query signal.
		if strings.Contains(message, "?") {
			return ActionQuery, 0.5
		}
		return ActionQuery, 0.3
	}

	return bestAction, confidence
}

// keywordScore produces a confidence value for a keyword match.
func (p *IntentParser) keywordScore(lower, keyword string, idx int) float64 {
	score := 0.6 // base score for any keyword match

	// Word boundary bonus: check characters before and after.
	if idx == 0 || !isAlpha(lower[idx-1]) {
		if end := idx + len(keyword); end >= len(lower) || !isAlpha(lower[end]) {
			score += 0.2
		}
	}

	// Position bonus: keywords near the very start are more indicative.
	if idx < 5 {
		score += 0.15
	} else if idx < 20 {
		score += 0.10
	} else if idx < 50 {
		score += 0.05
	}

	return score
}

// isAlpha returns true if b is an ASCII letter.
func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isAlphaNum returns true if b is an ASCII letter or digit.
func isAlphaNum(b byte) bool {
	return isAlpha(b) || (b >= '0' && b <= '9')
}

// extractEntities finds all entities in the message.
func (p *IntentParser) extractEntities(message string) []Entity {
	var entities []Entity

	// Extract entities from regex patterns.
	for entityType, pattern := range p.entityPatterns {
		matches := pattern.FindAllStringSubmatchIndex(message, -1)
		for _, loc := range matches {
			var value string
			var start, end int
			if len(loc) >= 4 && loc[2] >= 0 {
				// Use first capture group.
				value = message[loc[2]:loc[3]]
				start = loc[2]
				end = loc[3]
			} else {
				value = message[loc[0]:loc[1]]
				start = loc[0]
				end = loc[1]
			}
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			entities = append(entities, Entity{
				Type:       entityType,
				Value:      value,
				StartIndex: start,
				EndIndex:   end,
			})
		}
	}

	// Detect tool name references with word boundary checks.
	lower := strings.ToLower(message)
	for toolName := range p.toolNames {
		idx := strings.Index(lower, toolName)
		if idx >= 0 {
			end := idx + len(toolName)
			// Verify word boundaries: tool names contain underscores so we check
			// that the surrounding characters are not alphanumeric.
			beforeOK := idx == 0 || !isAlphaNum(lower[idx-1])
			afterOK := end >= len(lower) || !isAlphaNum(lower[end])
			if beforeOK && afterOK {
				entities = append(entities, Entity{
					Type:       EntityToolName,
					Value:      toolName,
					StartIndex: idx,
					EndIndex:   end,
				})
			}
		}
	}

	// Detect channel references (e.g., "#general" or "channel general").
	channelPat := regexp.MustCompile(`(?:(?:\bchannel\s+)|#)([a-zA-Z0-9_-]+)`)
	channelMatches := channelPat.FindAllStringSubmatchIndex(message, -1)
	for _, loc := range channelMatches {
		if len(loc) >= 4 && loc[2] >= 0 {
			entities = append(entities, Entity{
				Type:       EntityChannel,
				Value:      message[loc[2]:loc[3]],
				StartIndex: loc[2],
				EndIndex:   loc[3],
			})
		}
	}

	return entities
}

// inferTarget derives the target entity or topic from the message and entities.
func (p *IntentParser) inferTarget(message string, entities []Entity) string {
	// Prefer entities in priority order: tool, file path, URL, session, channel.
	priorityOrder := []EntityType{
		EntityToolName,
		EntityFilePath,
		EntityURL,
		EntitySession,
		EntityChannel,
		EntityModelName,
	}

	for _, et := range priorityOrder {
		for _, e := range entities {
			if e.Type == et {
				return e.Value
			}
		}
	}

	// Fallback: extract a noun phrase after the main verb.
	return p.extractTargetPhrase(message)
}

// extractTargetPhrase extracts a likely target noun phrase from a message.
func (p *IntentParser) extractTargetPhrase(message string) string {
	lower := strings.ToLower(message)

	// Look for common patterns: "verb the <target>" or "verb <target>"
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:search|find|get|show|list|check|run|create|delete|update|modify|send|enable|disable|schedule|cancel)\s+(?:the\s+)?(.+?)(?:\s+(?:and|then|after|from|to|in|on|with|for)\b|[.,;!?]|$)`),
	}

	for _, pat := range patterns {
		match := pat.FindStringSubmatch(lower)
		if len(match) >= 2 {
			target := strings.TrimSpace(match[1])
			if len(target) > 0 && len(target) < 100 {
				return target
			}
		}
	}

	return ""
}

// extractParameters builds a parameter map from the entities and message text.
func (p *IntentParser) extractParameters(message string, entities []Entity) map[string]interface{} {
	params := make(map[string]interface{})

	for _, e := range entities {
		switch e.Type {
		case EntityFilePath:
			if _, exists := params["path"]; !exists {
				params["path"] = e.Value
			}
		case EntityURL:
			if _, exists := params["url"]; !exists {
				params["url"] = e.Value
			}
		case EntityModelName:
			if _, exists := params["model"]; !exists {
				params["model"] = e.Value
			}
		case EntitySession:
			if _, exists := params["session"]; !exists {
				params["session"] = e.Value
			}
		case EntityToolName:
			if _, exists := params["tool"]; !exists {
				params["tool"] = e.Value
			}
		case EntityChannel:
			if _, exists := params["channel"]; !exists {
				params["channel"] = e.Value
			}
		}
	}

	// Extract quoted strings as potential argument values.
	quotePat := regexp.MustCompile(`"([^"]+)"|'([^']+)'`)
	quoteMatches := quotePat.FindAllStringSubmatch(message, -1)
	quotedValues := make([]string, 0, len(quoteMatches))
	for _, m := range quoteMatches {
		val := m[1]
		if val == "" {
			val = m[2]
		}
		if val != "" {
			quotedValues = append(quotedValues, val)
		}
	}
	if len(quotedValues) > 0 {
		if len(quotedValues) == 1 {
			params["query"] = quotedValues[0]
		} else {
			params["queries"] = quotedValues
		}
	}

	return params
}

// splitCompoundMessage splits a compound message into individual parts.
func (p *IntentParser) splitCompoundMessage(message string) []string {
	// Check for numbered list items first (e.g., "1. do X  2. do Y").
	numberedParts := p.splitNumberedList(message)
	if len(numberedParts) > 1 {
		return numberedParts
	}

	// Check for semicolon separation.
	if strings.Contains(message, ";") {
		parts := strings.Split(message, ";")
		if len(parts) > 1 {
			result := make([]string, 0, len(parts))
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part != "" {
					result = append(result, part)
				}
			}
			if len(result) > 1 {
				return result
			}
		}
	}

	// Try sequential keywords: "first ... then ... finally ..."
	seqParts := p.splitBySequentialKeywords(message)
	if len(seqParts) > 1 {
		return seqParts
	}

	// Try "and then" / "and also" conjunctions.
	conjParts := p.conjunctionPat.Split(message, -1)
	if len(conjParts) > 1 {
		result := make([]string, 0, len(conjParts))
		for _, part := range conjParts {
			part = strings.TrimSpace(part)
			if part != "" {
				result = append(result, part)
			}
		}
		if len(result) > 1 {
			return result
		}
	}

	return nil
}

// splitNumberedList splits messages containing numbered items.
func (p *IntentParser) splitNumberedList(message string) []string {
	// Match patterns like "1. ...", "1) ...", or "1: ..."
	pat := regexp.MustCompile(`(?m)(?:^|\s)\d+[.):][ \t]+`)
	locs := pat.FindAllStringIndex(message, -1)
	if len(locs) < 2 {
		return nil
	}

	parts := make([]string, 0, len(locs))
	for i, loc := range locs {
		start := loc[1] // after the number prefix
		var end int
		if i+1 < len(locs) {
			end = locs[i+1][0]
		} else {
			end = len(message)
		}
		part := strings.TrimSpace(message[start:end])
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

// splitBySequentialKeywords splits on "first", "then", "next", "finally".
func (p *IntentParser) splitBySequentialKeywords(message string) []string {
	lower := strings.ToLower(message)

	// Build a list of keyword positions.
	type kwPos struct {
		pos     int
		keyword string
		length  int
	}

	keywords := []string{"first", "then", "next", "after that", "followed by", "finally"}
	var positions []kwPos

	for _, kw := range keywords {
		idx := strings.Index(lower, kw)
		for idx >= 0 {
			// Check word boundary.
			ok := true
			if idx > 0 && isAlpha(lower[idx-1]) {
				ok = false
			}
			end := idx + len(kw)
			if end < len(lower) && isAlpha(lower[end]) {
				ok = false
			}
			if ok {
				positions = append(positions, kwPos{pos: idx, keyword: kw, length: len(kw)})
			}
			next := strings.Index(lower[idx+1:], kw)
			if next >= 0 {
				idx = idx + 1 + next
			} else {
				break
			}
		}
	}

	if len(positions) < 2 {
		return nil
	}

	// Sort positions.
	for i := 0; i < len(positions); i++ {
		for j := i + 1; j < len(positions); j++ {
			if positions[j].pos < positions[i].pos {
				positions[i], positions[j] = positions[j], positions[i]
			}
		}
	}

	// Extract text between keywords.
	parts := make([]string, 0, len(positions))
	for i, kp := range positions {
		start := kp.pos + kp.length
		var end int
		if i+1 < len(positions) {
			end = positions[i+1].pos
		} else {
			end = len(message)
		}
		// Skip comma/whitespace after keyword.
		part := strings.TrimSpace(message[start:end])
		part = strings.TrimLeft(part, ",: ")
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}
