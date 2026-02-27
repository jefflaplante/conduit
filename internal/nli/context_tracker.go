package nli

import (
	"strings"
	"sync"
	"time"
)

const defaultWindowSize = 10

// ConversationTurn records a single exchange in the conversation.
type ConversationTurn struct {
	Message   string    `json:"message"`
	Response  string    `json:"response"`
	Entities  []Entity  `json:"entities"`
	Timestamp time.Time `json:"timestamp"`
}

// ContextTracker maintains conversation state across turns, providing entity
// resolution and pronoun/reference tracking. It is safe for concurrent use.
type ContextTracker struct {
	mu         sync.RWMutex
	turns      []ConversationTurn
	windowSize int
	parser     *IntentParser
}

// NewContextTracker creates a ContextTracker with the default sliding window (10 turns).
func NewContextTracker() *ContextTracker {
	return &ContextTracker{
		turns:      make([]ConversationTurn, 0, defaultWindowSize),
		windowSize: defaultWindowSize,
		parser:     NewIntentParser(),
	}
}

// NewContextTrackerWithWindow creates a ContextTracker with a custom window size.
func NewContextTrackerWithWindow(size int) *ContextTracker {
	if size < 1 {
		size = 1
	}
	return &ContextTracker{
		turns:      make([]ConversationTurn, 0, size),
		windowSize: size,
		parser:     NewIntentParser(),
	}
}

// UpdateContext records a new conversation turn and extracts entities from both
// the user message and the assistant response.
func (ct *ContextTracker) UpdateContext(message string, response string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	// Extract entities from both sides of the conversation.
	msgEntities := ct.parser.extractEntities(message)
	respEntities := ct.parser.extractEntities(response)

	entities := make([]Entity, 0, len(msgEntities)+len(respEntities))
	entities = append(entities, msgEntities...)
	entities = append(entities, respEntities...)

	turn := ConversationTurn{
		Message:   message,
		Response:  response,
		Entities:  entities,
		Timestamp: time.Now(),
	}

	ct.turns = append(ct.turns, turn)

	// Trim to window size.
	if len(ct.turns) > ct.windowSize {
		ct.turns = ct.turns[len(ct.turns)-ct.windowSize:]
	}
}

// ResolveReference resolves a pronoun or anaphoric reference ("it", "that",
// "the file", "the url", etc.) to the most recent matching entity.
func (ct *ContextTracker) ResolveReference(ref string) string {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	ref = strings.TrimSpace(ref)
	lower := strings.ToLower(ref)

	// Map common references to entity types for targeted lookup.
	typeHints := referenceTypeHints(lower)

	// Search turns in reverse order (most recent first) for matching entities.
	for i := len(ct.turns) - 1; i >= 0; i-- {
		turn := ct.turns[i]
		for j := len(turn.Entities) - 1; j >= 0; j-- {
			ent := turn.Entities[j]
			if matchesHints(ent.Type, typeHints) {
				return ent.Value
			}
		}
	}

	// If no typed hint matched, try to return the most recent entity of any kind.
	if isGenericPronoun(lower) {
		for i := len(ct.turns) - 1; i >= 0; i-- {
			turn := ct.turns[i]
			if len(turn.Entities) > 0 {
				return turn.Entities[len(turn.Entities)-1].Value
			}
		}
	}

	return ""
}

// GetActiveEntities returns the distinct entities currently in the sliding window,
// ordered from most recent to least recent.
func (ct *ContextTracker) GetActiveEntities() []Entity {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	seen := make(map[string]bool)
	var result []Entity

	// Iterate newest-first.
	for i := len(ct.turns) - 1; i >= 0; i-- {
		for j := len(ct.turns[i].Entities) - 1; j >= 0; j-- {
			ent := ct.turns[i].Entities[j]
			key := string(ent.Type) + ":" + ent.Value
			if !seen[key] {
				seen[key] = true
				result = append(result, ent)
			}
		}
	}

	return result
}

// GetRecentTurns returns the most recent n conversation turns.
func (ct *ContextTracker) GetRecentTurns(n int) []ConversationTurn {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	if n <= 0 {
		return nil
	}
	if n > len(ct.turns) {
		n = len(ct.turns)
	}

	result := make([]ConversationTurn, n)
	copy(result, ct.turns[len(ct.turns)-n:])
	return result
}

// Clear resets all conversation state.
func (ct *ContextTracker) Clear() {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.turns = ct.turns[:0]
}

// TurnCount returns the number of tracked turns.
func (ct *ContextTracker) TurnCount() int {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return len(ct.turns)
}

// referenceTypeHints returns the entity types that a reference phrase might refer to.
func referenceTypeHints(lower string) []EntityType {
	switch {
	case strings.Contains(lower, "file") || strings.Contains(lower, "path"):
		return []EntityType{EntityFilePath}
	case strings.Contains(lower, "url") || strings.Contains(lower, "link") || strings.Contains(lower, "page") || strings.Contains(lower, "site"):
		return []EntityType{EntityURL}
	case strings.Contains(lower, "model"):
		return []EntityType{EntityModelName}
	case strings.Contains(lower, "session"):
		return []EntityType{EntitySession}
	case strings.Contains(lower, "tool"):
		return []EntityType{EntityToolName}
	case strings.Contains(lower, "channel"):
		return []EntityType{EntityChannel}
	default:
		return nil
	}
}

// matchesHints returns true if the entity type is in the hint list,
// or if the hint list is empty (match any).
func matchesHints(et EntityType, hints []EntityType) bool {
	if len(hints) == 0 {
		return false
	}
	for _, h := range hints {
		if et == h {
			return true
		}
	}
	return false
}

// isGenericPronoun returns true for pronouns that do not hint at a specific entity type.
func isGenericPronoun(lower string) bool {
	generics := []string{"it", "that", "this", "them", "those", "the result", "the output", "the response"}
	for _, g := range generics {
		if lower == g {
			return true
		}
	}
	return false
}
