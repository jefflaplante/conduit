package heartbeat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TaskInterpreter reads and interprets HEARTBEAT.md files into executable tasks
type TaskInterpreter struct {
	workspaceDir string
}

// NewTaskInterpreter creates a new task interpreter
func NewTaskInterpreter(workspaceDir string) *TaskInterpreter {
	return &TaskInterpreter{
		workspaceDir: workspaceDir,
	}
}

// ReadHeartbeatTasks reads the HEARTBEAT.md file and extracts tasks
func (t *TaskInterpreter) ReadHeartbeatTasks() ([]ParsedHeartbeatTask, error) {
	heartbeatPath := filepath.Join(t.workspaceDir, "HEARTBEAT.md")

	// Check if file exists
	if _, err := os.Stat(heartbeatPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("HEARTBEAT.md not found in workspace: %s", heartbeatPath)
		}
		return nil, fmt.Errorf("failed to access HEARTBEAT.md: %w", err)
	}

	// Read file content
	content, err := os.ReadFile(heartbeatPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read HEARTBEAT.md: %w", err)
	}

	// Parse content into tasks
	tasks, err := t.parseHeartbeatContent(string(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HEARTBEAT.md content: %w", err)
	}

	return tasks, nil
}

// GeneratePrompt creates an AI prompt from parsed heartbeat tasks
func (t *TaskInterpreter) GeneratePrompt(tasks []ParsedHeartbeatTask) (string, error) {
	if len(tasks) == 0 {
		return "", fmt.Errorf("no tasks to generate prompt from")
	}

	var promptBuilder strings.Builder

	// Start with the default heartbeat prompt as per AGENTS.md
	promptBuilder.WriteString("Read HEARTBEAT.md if it exists. Follow it strictly. Do not infer or repeat old tasks from prior chats. If nothing needs attention, reply HEARTBEAT_OK.\n\n")

	// Add the actual HEARTBEAT.md content as context
	promptBuilder.WriteString("HEARTBEAT.md contains the following instructions:\n\n")

	for i, task := range tasks {
		if i > 0 {
			promptBuilder.WriteString("\n")
		}

		promptBuilder.WriteString(fmt.Sprintf("## %s\n", task.Title))
		if task.Description != "" {
			promptBuilder.WriteString(fmt.Sprintf("%s\n", task.Description))
		}

		for _, instruction := range task.Instructions {
			promptBuilder.WriteString(fmt.Sprintf("- %s\n", instruction))
		}

		if len(task.CodeBlocks) > 0 {
			promptBuilder.WriteString("\nCode to execute:\n")
			for _, code := range task.CodeBlocks {
				promptBuilder.WriteString(fmt.Sprintf("```%s\n%s\n```\n", code.Language, code.Content))
			}
		}
	}

	// Add execution guidelines
	promptBuilder.WriteString("\n---\n\n")
	promptBuilder.WriteString("Execute these tasks according to their instructions. ")
	promptBuilder.WriteString("If all tasks result in no action needed or only info-level items, reply with HEARTBEAT_OK. ")
	promptBuilder.WriteString("If any task requires action or delivery, provide the specific details and actions to be taken.")

	return promptBuilder.String(), nil
}

// ParsedHeartbeatTask represents a task parsed from HEARTBEAT.md
type ParsedHeartbeatTask struct {
	// Task identification
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`

	// Task instructions
	Instructions []string    `json:"instructions"`
	CodeBlocks   []CodeBlock `json:"code_blocks,omitempty"`

	// Parsed metadata
	Type       TaskType     `json:"type"`
	Priority   TaskPriority `json:"priority"`
	Conditions []string     `json:"conditions,omitempty"`
	Actions    []string     `json:"actions,omitempty"`

	// Timing and scheduling hints
	IsImmediate    bool `json:"is_immediate,omitempty"`
	IsQuietAware   bool `json:"is_quiet_aware,omitempty"`
	HasConditional bool `json:"has_conditional,omitempty"`

	// Source information
	LineStart int `json:"line_start"`
	LineEnd   int `json:"line_end"`
}

// CodeBlock represents a code block within a heartbeat task
type CodeBlock struct {
	Language string `json:"language"`
	Content  string `json:"content"`
}

// parseHeartbeatContent parses the raw HEARTBEAT.md content into structured tasks
func (t *TaskInterpreter) parseHeartbeatContent(content string) ([]ParsedHeartbeatTask, error) {
	lines := strings.Split(content, "\n")
	var tasks []ParsedHeartbeatTask
	var currentTask *ParsedHeartbeatTask
	var inCodeBlock bool
	var currentCodeBlock *CodeBlock

	for lineNum, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// Skip empty lines and main title
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "# HEARTBEAT.md") {
			continue
		}

		// Handle code blocks
		if strings.HasPrefix(trimmedLine, "```") {
			if inCodeBlock {
				// End of code block
				if currentTask != nil && currentCodeBlock != nil {
					currentTask.CodeBlocks = append(currentTask.CodeBlocks, *currentCodeBlock)
					currentCodeBlock = nil
				}
				inCodeBlock = false
			} else {
				// Start of code block
				language := strings.TrimPrefix(trimmedLine, "```")
				currentCodeBlock = &CodeBlock{
					Language: language,
					Content:  "",
				}
				inCodeBlock = true
			}
			continue
		}

		// If we're in a code block, append to current code block
		if inCodeBlock && currentCodeBlock != nil {
			if currentCodeBlock.Content != "" {
				currentCodeBlock.Content += "\n"
			}
			currentCodeBlock.Content += line
			continue
		}

		// Handle section headers (## Task Name)
		if strings.HasPrefix(trimmedLine, "## ") {
			// Save previous task if exists
			if currentTask != nil {
				currentTask.LineEnd = lineNum - 1
				tasks = append(tasks, *currentTask)
			}

			// Start new task
			title := strings.TrimPrefix(trimmedLine, "## ")
			currentTask = &ParsedHeartbeatTask{
				Title:        title,
				LineStart:    lineNum,
				Type:         t.inferTaskType(title),
				Priority:     t.inferTaskPriority(title),
				IsImmediate:  t.isImmediateTask(title),
				IsQuietAware: t.isQuietAwareTask(title),
			}
			continue
		}

		// Handle bullet points and instructions
		if strings.HasPrefix(trimmedLine, "- ") || strings.HasPrefix(trimmedLine, "* ") {
			if currentTask != nil {
				instruction := strings.TrimPrefix(trimmedLine, "- ")
				instruction = strings.TrimPrefix(instruction, "* ")
				currentTask.Instructions = append(currentTask.Instructions, instruction)

				// Analyze instruction for metadata
				t.analyzeInstruction(currentTask, instruction)
			}
			continue
		}

		// Regular description text
		if currentTask != nil && trimmedLine != "" && !strings.HasPrefix(trimmedLine, "#") {
			if currentTask.Description != "" {
				currentTask.Description += " "
			}
			currentTask.Description += trimmedLine

			// Check description for immediacy and other metadata
			descLower := strings.ToLower(trimmedLine)
			if strings.Contains(descLower, "immediately") || strings.Contains(descLower, "urgent") {
				currentTask.IsImmediate = true
			}
		}
	}

	// Save last task if exists
	if currentTask != nil {
		currentTask.LineEnd = len(lines) - 1
		tasks = append(tasks, *currentTask)
	}

	return tasks, nil
}

// inferTaskType attempts to infer the task type from its title or content
func (t *TaskInterpreter) inferTaskType(title string) TaskType {
	titleLower := strings.ToLower(title)

	switch {
	case strings.Contains(titleLower, "alert"), strings.Contains(titleLower, "queue"):
		return TaskTypeAlerts
	case strings.Contains(titleLower, "report"), strings.Contains(titleLower, "summary"), strings.Contains(titleLower, "briefing"):
		return TaskTypeReports
	case strings.Contains(titleLower, "check"), strings.Contains(titleLower, "monitor"), strings.Contains(titleLower, "status"):
		return TaskTypeChecks
	case strings.Contains(titleLower, "maintain"), strings.Contains(titleLower, "cleanup"), strings.Contains(titleLower, "update"):
		return TaskTypeMaintenance
	default:
		return TaskTypeChecks // Default fallback
	}
}

// inferTaskPriority attempts to infer the task priority from its title or content
func (t *TaskInterpreter) inferTaskPriority(title string) TaskPriority {
	titleLower := strings.ToLower(title)

	switch {
	case strings.Contains(titleLower, "critical"), strings.Contains(titleLower, "urgent"), strings.Contains(titleLower, "immediate"):
		return TaskPriorityCritical
	case strings.Contains(titleLower, "alert"), strings.Contains(titleLower, "warning"):
		return TaskPriorityHigh
	case strings.Contains(titleLower, "info"), strings.Contains(titleLower, "routine"), strings.Contains(titleLower, "maintenance"):
		return TaskPriorityLow
	default:
		return TaskPriorityNormal
	}
}

// isImmediateTask checks if the task needs immediate execution
func (t *TaskInterpreter) isImmediateTask(title string) bool {
	titleLower := strings.ToLower(title)
	return strings.Contains(titleLower, "immediate") ||
		strings.Contains(titleLower, "critical") ||
		strings.Contains(titleLower, "urgent")
}

// isQuietAwareTask checks if the task should respect quiet hours
func (t *TaskInterpreter) isQuietAwareTask(title string) bool {
	titleLower := strings.ToLower(title)
	return strings.Contains(titleLower, "quiet") ||
		strings.Contains(titleLower, "awake") ||
		strings.Contains(titleLower, "alert")
}

// analyzeInstruction analyzes an instruction for metadata and conditions
func (t *TaskInterpreter) analyzeInstruction(task *ParsedHeartbeatTask, instruction string) {
	instructionLower := strings.ToLower(instruction)

	// Look for conditional statements
	if strings.Contains(instructionLower, "if ") {
		task.HasConditional = true
		// Extract condition
		if condition := t.extractCondition(instruction); condition != "" {
			task.Conditions = append(task.Conditions, condition)
		}
	}

	// Look for actions
	if strings.Contains(instructionLower, "deliver") ||
		strings.Contains(instructionLower, "send") ||
		strings.Contains(instructionLower, "notify") {
		task.Actions = append(task.Actions, instruction)
	}

	// Look for severity indicators
	if strings.Contains(instructionLower, "critical") {
		task.Priority = TaskPriorityCritical
		task.IsImmediate = true
	} else if strings.Contains(instructionLower, "warning") && task.Priority < TaskPriorityHigh {
		task.Priority = TaskPriorityHigh
	}

	// Look for immediate delivery indicators
	if strings.Contains(instructionLower, "immediately") || strings.Contains(instructionLower, "urgent") {
		task.IsImmediate = true
	}

	// Look for quiet hours awareness
	if strings.Contains(instructionLower, "awake") || strings.Contains(instructionLower, "quiet") {
		task.IsQuietAware = true
	}
}

// extractCondition extracts condition text from an instruction
func (t *TaskInterpreter) extractCondition(instruction string) string {
	// Look for "If it contains" or similar patterns
	lowerInstr := strings.ToLower(instruction)

	if idx := strings.Index(lowerInstr, "if "); idx >= 0 {
		conditionStart := idx + 3
		// Try to find the end of the condition (usually before a colon)
		conditionEnd := len(instruction)
		if colonIdx := strings.Index(instruction[conditionStart:], ":"); colonIdx >= 0 {
			conditionEnd = conditionStart + colonIdx
		}

		return strings.TrimSpace(instruction[conditionStart:conditionEnd])
	}

	return ""
}

// Validate validates a parsed heartbeat task
func (p *ParsedHeartbeatTask) Validate() error {
	if p.Title == "" {
		return fmt.Errorf("task title cannot be empty")
	}

	if !p.Type.IsValid() {
		return fmt.Errorf("invalid task type: %s", p.Type)
	}

	if !p.Priority.IsValid() {
		return fmt.Errorf("invalid task priority: %d", p.Priority)
	}

	if p.LineStart < 0 {
		return fmt.Errorf("line start cannot be negative")
	}

	if p.LineEnd < p.LineStart {
		return fmt.Errorf("line end cannot be before line start")
	}

	return nil
}

// ToHeartbeatTask converts a parsed task to a HeartbeatTask for execution
func (p *ParsedHeartbeatTask) ToHeartbeatTask() HeartbeatTask {
	now := time.Now()

	task := HeartbeatTask{
		ID:          fmt.Sprintf("heartbeat_%s_%d", strings.ToLower(strings.ReplaceAll(p.Title, " ", "_")), now.UnixNano()),
		Type:        p.Type,
		Name:        p.Title,
		Description: p.Description,
		CreatedAt:   now,
		ScheduledAt: now,
		Status:      TaskStatusPending,
		Priority:    p.Priority,
		MaxRetries:  3,
		Payload: map[string]interface{}{
			"instructions": p.Instructions,
			"code_blocks":  p.CodeBlocks,
			"conditions":   p.Conditions,
			"actions":      p.Actions,
		},
	}

	// Set timeout based on task type and priority
	timeoutDuration := 60 * time.Second
	if p.Priority == TaskPriorityCritical {
		timeoutDuration = 30 * time.Second
	} else if p.Priority == TaskPriorityLow {
		timeoutDuration = 120 * time.Second
	}
	task.TimeoutDuration = &timeoutDuration

	// Set quiet hours behavior
	task.SkipQuietHours = p.IsImmediate || p.Priority == TaskPriorityCritical

	// Add tags based on analysis
	if p.IsImmediate {
		task.Tags = append(task.Tags, "immediate")
	}
	if p.IsQuietAware {
		task.Tags = append(task.Tags, "quiet_aware")
	}
	if p.HasConditional {
		task.Tags = append(task.Tags, "conditional")
	}

	return task
}

// String returns a string representation of the parsed task
func (p *ParsedHeartbeatTask) String() string {
	return fmt.Sprintf("ParsedHeartbeatTask{Title: %s, Type: %s, Priority: %s, Instructions: %d, CodeBlocks: %d}",
		p.Title, p.Type, p.Priority, len(p.Instructions), len(p.CodeBlocks))
}
