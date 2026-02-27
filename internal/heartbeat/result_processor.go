package heartbeat

import (
	"fmt"
	"regexp"
	"strings"
)

// ResultProcessor processes AI responses and determines appropriate actions
type ResultProcessor struct {
	// Patterns for detecting HEARTBEAT_OK responses
	heartbeatOKPatterns []*regexp.Regexp

	// Patterns for detecting different types of alerts/actions
	alertPatterns       []*regexp.Regexp
	deliveryPatterns    []*regexp.Regexp
	maintenancePatterns []*regexp.Regexp
}

// NewResultProcessor creates a new result processor with default patterns
func NewResultProcessor() *ResultProcessor {
	return &ResultProcessor{
		heartbeatOKPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\bHEARTBEAT_OK\b`),
			regexp.MustCompile(`(?i)no\s+alerts?`),
			regexp.MustCompile(`(?i)nothing\s+needs?\s+attention`),
			regexp.MustCompile(`(?i)no\s+action\s+needed`),
			regexp.MustCompile(`(?i)all\s+clear`),
			regexp.MustCompile(`(?i)only\s+info[-\s]level`),
		},
		alertPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\bcritical\b`),
			regexp.MustCompile(`(?i)\burgent\b`),
			regexp.MustCompile(`(?i)\balert\b`),
			regexp.MustCompile(`(?i)\bwarning\b`),
			regexp.MustCompile(`(?i)deliver.*immediately`),
			regexp.MustCompile(`(?i)needs?\s+immediate\s+attention`),
		},
		deliveryPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)deliver\s+to\s+\w+`),
			regexp.MustCompile(`(?i)send\s+to\s+\w+`),
			regexp.MustCompile(`(?i)notify\s+\w+`),
			regexp.MustCompile(`(?i)message\s+\w+`),
			regexp.MustCompile(`(?i)telegram`),
		},
		maintenancePatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)clear\s+the\s+queue`),
			regexp.MustCompile(`(?i)cleanup\b`),
			regexp.MustCompile(`(?i)maintenance\b`),
			regexp.MustCompile(`(?i)python3.*-c`),
		},
	}
}

// ProcessResponse analyzes an AI response and determines the appropriate result status and actions
func (p *ResultProcessor) ProcessResponse(response AIResponse, tasks []ParsedHeartbeatTask) (*HeartbeatResult, error) {
	content := response.GetContent()
	if content == "" {
		return nil, fmt.Errorf("empty response content")
	}

	result := &HeartbeatResult{
		Message:        content,
		TasksProcessed: len(tasks),
		Metadata: map[string]interface{}{
			"usage": response.GetUsage(),
		},
	}

	// Check if this is a HEARTBEAT_OK response
	if p.isHeartbeatOK(content) {
		result.Status = ResultStatusOK
		return result, nil
	}

	// Analyze content for different types of actions
	actions := p.extractActions(content, tasks)
	result.Actions = actions

	// Determine overall status based on actions
	if len(actions) == 0 {
		result.Status = ResultStatusOK
	} else if p.hasAlertActions(actions) {
		result.Status = ResultStatusAlert
	} else {
		result.Status = ResultStatusAction
	}

	return result, nil
}

// isHeartbeatOK checks if the response indicates a HEARTBEAT_OK status
func (p *ResultProcessor) isHeartbeatOK(content string) bool {
	// Check for explicit HEARTBEAT_OK patterns
	for _, pattern := range p.heartbeatOKPatterns {
		if pattern.MatchString(content) {
			return true
		}
	}

	// Additional heuristics for HEARTBEAT_OK
	lowerContent := strings.ToLower(content)

	// Short responses that indicate no action needed
	if len(strings.TrimSpace(content)) < 50 {
		noActionIndicators := []string{
			"nothing to report",
			"all good",
			"no issues",
			"empty queue",
			"no new alerts",
		}

		for _, indicator := range noActionIndicators {
			if strings.Contains(lowerContent, indicator) {
				return true
			}
		}
	}

	return false
}

// extractActions analyzes the content and extracts actionable items
func (p *ResultProcessor) extractActions(content string, tasks []ParsedHeartbeatTask) []HeartbeatAction {
	var actions []HeartbeatAction

	// Split content into sentences for analysis
	sentences := p.splitIntoSentences(content)

	for _, sentence := range sentences {
		if action := p.analyzeSentenceForAction(sentence, tasks); action != nil {
			actions = append(actions, *action)
		}
	}

	// If no specific actions found but content doesn't indicate HEARTBEAT_OK,
	// create a general delivery action
	if len(actions) == 0 && !p.isHeartbeatOK(content) {
		actions = append(actions, HeartbeatAction{
			Type:     ActionTypeNotification,
			Target:   "telegram",
			Content:  content,
			Priority: TaskPriorityNormal,
		})
	}

	return actions
}

// analyzeSentenceForAction analyzes a single sentence for actionable content
func (p *ResultProcessor) analyzeSentenceForAction(sentence string, tasks []ParsedHeartbeatTask) *HeartbeatAction {
	sentence = strings.TrimSpace(sentence)
	if sentence == "" {
		return nil
	}

	// Check for alert patterns
	for _, pattern := range p.alertPatterns {
		if pattern.MatchString(sentence) {
			return &HeartbeatAction{
				Type:     ActionTypeAlert,
				Target:   "telegram",
				Content:  sentence,
				Priority: p.inferPriorityFromContent(sentence),
				Metadata: map[string]interface{}{
					"detected_pattern": "alert",
					"immediate":        p.isImmediateAction(sentence),
				},
			}
		}
	}

	// Check for delivery patterns
	for _, pattern := range p.deliveryPatterns {
		if pattern.MatchString(sentence) {
			return &HeartbeatAction{
				Type:     ActionTypeDelivery,
				Target:   p.extractDeliveryTarget(sentence),
				Content:  sentence,
				Priority: p.inferPriorityFromContent(sentence),
				Metadata: map[string]interface{}{
					"detected_pattern": "delivery",
					"quiet_aware":      p.isQuietAwareAction(sentence),
				},
			}
		}
	}

	// Check for maintenance patterns
	for _, pattern := range p.maintenancePatterns {
		if pattern.MatchString(sentence) {
			return &HeartbeatAction{
				Type:     ActionTypeCommand,
				Target:   "system",
				Content:  sentence,
				Priority: TaskPriorityLow,
				Metadata: map[string]interface{}{
					"detected_pattern": "maintenance",
					"command":          p.extractCommand(sentence),
				},
			}
		}
	}

	return nil
}

// splitIntoSentences splits content into sentences for analysis
func (p *ResultProcessor) splitIntoSentences(content string) []string {
	// Simple sentence splitting - could be enhanced with more sophisticated NLP
	sentences := regexp.MustCompile(`[.!?]+\s+`).Split(content, -1)

	var result []string
	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		if sentence != "" {
			result = append(result, sentence)
		}
	}

	// If no sentence breaks found, treat entire content as one sentence
	if len(result) == 0 && strings.TrimSpace(content) != "" {
		result = append(result, strings.TrimSpace(content))
	}

	return result
}

// inferPriorityFromContent determines the priority based on content keywords
func (p *ResultProcessor) inferPriorityFromContent(content string) TaskPriority {
	lowerContent := strings.ToLower(content)

	// Critical priority indicators
	criticalKeywords := []string{
		"critical", "urgent", "immediate", "emergency", "severe",
		"down", "failed", "error", "cannot", "unable",
	}

	for _, keyword := range criticalKeywords {
		if strings.Contains(lowerContent, keyword) {
			return TaskPriorityCritical
		}
	}

	// High priority indicators
	highKeywords := []string{
		"warning", "alert", "attention", "important", "significant",
		"issue", "problem", "concern",
	}

	for _, keyword := range highKeywords {
		if strings.Contains(lowerContent, keyword) {
			return TaskPriorityHigh
		}
	}

	// Low priority indicators
	lowKeywords := []string{
		"info", "information", "note", "routine", "maintenance",
		"cleanup", "summary",
	}

	for _, keyword := range lowKeywords {
		if strings.Contains(lowerContent, keyword) {
			return TaskPriorityLow
		}
	}

	return TaskPriorityNormal
}

// isImmediateAction checks if the action needs immediate execution
func (p *ResultProcessor) isImmediateAction(content string) bool {
	lowerContent := strings.ToLower(content)

	immediateKeywords := []string{
		"immediately", "now", "urgent", "critical", "asap",
		"right away", "at once",
	}

	for _, keyword := range immediateKeywords {
		if strings.Contains(lowerContent, keyword) {
			return true
		}
	}

	return false
}

// isQuietAwareAction checks if the action should respect quiet hours
func (p *ResultProcessor) isQuietAwareAction(content string) bool {
	lowerContent := strings.ToLower(content)

	quietAwareKeywords := []string{
		"awake", "quiet", "hours", "8 am", "10 pm", "pt",
		"likely awake", "if awake",
	}

	for _, keyword := range quietAwareKeywords {
		if strings.Contains(lowerContent, keyword) {
			return true
		}
	}

	return false
}

// extractDeliveryTarget extracts the target for delivery from content
func (p *ResultProcessor) extractDeliveryTarget(content string) string {
	// Look for common delivery targets, ordered by specificity:
	// channels first, then person identifiers
	targets := []string{"telegram", "email", "sms", "jeff", "user", "admin"}

	lowerContent := strings.ToLower(content)

	for _, target := range targets {
		if strings.Contains(lowerContent, target) {
			return target
		}
	}

	// Default to telegram for most heartbeat deliveries
	return "telegram"
}

// extractCommand extracts command text from maintenance actions
func (p *ResultProcessor) extractCommand(content string) string {
	// Look for code blocks with any language tag (bash, python, shell, etc.)
	codeBlockPattern := regexp.MustCompile("(?s)```\\w*\\n?(.+?)\\n?```")
	if matches := codeBlockPattern.FindStringSubmatch(content); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}

	// Look for inline commands
	inlineCommandPattern := regexp.MustCompile("`([^`]+)`")
	if matches := inlineCommandPattern.FindStringSubmatch(content); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}

	// Look for python commands (capture entire command including quoted args)
	pythonPattern := regexp.MustCompile(`(python3\s+-c\s+.+)`)
	if matches := pythonPattern.FindStringSubmatch(content); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}

	return ""
}

// hasAlertActions checks if any actions are alerts requiring immediate attention
func (p *ResultProcessor) hasAlertActions(actions []HeartbeatAction) bool {
	for _, action := range actions {
		if action.Type == ActionTypeAlert || action.Priority == TaskPriorityCritical {
			return true
		}
	}
	return false
}

// FilterActionsByQuietHours filters actions based on whether quiet hours should be respected
func (p *ResultProcessor) FilterActionsByQuietHours(actions []HeartbeatAction, isQuietHours bool) (immediate []HeartbeatAction, delayed []HeartbeatAction) {
	for _, action := range actions {
		// Always execute critical alerts immediately
		if action.Type == ActionTypeAlert || action.Priority == TaskPriorityCritical {
			immediate = append(immediate, action)
			continue
		}

		// Check if action respects quiet hours
		if metadata, ok := action.Metadata["quiet_aware"].(bool); ok && metadata && isQuietHours {
			delayed = append(delayed, action)
			continue
		}

		// Default to immediate for non-quiet-aware actions
		immediate = append(immediate, action)
	}

	return immediate, delayed
}

// MergeActions combines multiple actions into a single action when appropriate
func (p *ResultProcessor) MergeActions(actions []HeartbeatAction) []HeartbeatAction {
	if len(actions) <= 1 {
		return actions
	}

	// Group actions by type and target
	groups := make(map[string][]HeartbeatAction)

	for _, action := range actions {
		key := fmt.Sprintf("%s:%s", action.Type, action.Target)
		groups[key] = append(groups[key], action)
	}

	var merged []HeartbeatAction

	for _, group := range groups {
		if len(group) == 1 {
			merged = append(merged, group[0])
			continue
		}

		// Merge multiple actions of the same type and target
		mergedAction := group[0] // Start with first action
		var contentParts []string

		highestPriority := TaskPriorityLow
		for _, action := range group {
			contentParts = append(contentParts, action.Content)
			if action.Priority > highestPriority {
				highestPriority = action.Priority
			}
		}

		mergedAction.Content = strings.Join(contentParts, "\n\n")
		mergedAction.Priority = highestPriority
		if mergedAction.Metadata == nil {
			mergedAction.Metadata = make(map[string]interface{})
		}
		mergedAction.Metadata["merged_count"] = len(group)

		merged = append(merged, mergedAction)
	}

	return merged
}

// ValidateActions validates a list of actions for consistency and completeness
func (p *ResultProcessor) ValidateActions(actions []HeartbeatAction) error {
	for i, action := range actions {
		if action.Type == "" {
			return fmt.Errorf("action %d: type cannot be empty", i)
		}

		if action.Target == "" {
			return fmt.Errorf("action %d: target cannot be empty", i)
		}

		if action.Content == "" {
			return fmt.Errorf("action %d: content cannot be empty", i)
		}

		// Validate action type
		validTypes := map[ActionType]bool{
			ActionTypeAlert:        true,
			ActionTypeNotification: true,
			ActionTypeCommand:      true,
			ActionTypeDelivery:     true,
		}

		if !validTypes[action.Type] {
			return fmt.Errorf("action %d: invalid type %s", i, action.Type)
		}

		// Validate priority
		if !action.Priority.IsValid() {
			return fmt.Errorf("action %d: invalid priority %d", i, action.Priority)
		}
	}

	return nil
}

// GetActionSummary returns a human-readable summary of actions
func (p *ResultProcessor) GetActionSummary(actions []HeartbeatAction) string {
	if len(actions) == 0 {
		return "No actions required"
	}

	counts := make(map[ActionType]int)
	priorities := make(map[TaskPriority]int)

	for _, action := range actions {
		counts[action.Type]++
		priorities[action.Priority]++
	}

	var parts []string

	// Summarize by type
	for actionType, count := range counts {
		if count == 1 {
			parts = append(parts, fmt.Sprintf("1 %s", actionType))
		} else {
			parts = append(parts, fmt.Sprintf("%d %ss", count, actionType))
		}
	}

	summary := strings.Join(parts, ", ")

	// Add priority information if there are critical items
	if priorities[TaskPriorityCritical] > 0 {
		summary += fmt.Sprintf(" (%d critical)", priorities[TaskPriorityCritical])
	} else if priorities[TaskPriorityHigh] > 0 {
		summary += fmt.Sprintf(" (%d high priority)", priorities[TaskPriorityHigh])
	}

	return summary
}
