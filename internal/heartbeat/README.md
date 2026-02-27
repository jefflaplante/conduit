# HEARTBEAT.md Task Execution Framework

This package implements **OCGO-020**, a comprehensive framework for executing HEARTBEAT.md tasks through the existing Conduit Go Gateway cron system.

## Overview

The heartbeat framework allows the gateway to:

1. **Dynamically read** HEARTBEAT.md from the workspace directory
2. **Parse tasks** and convert them into executable AI prompts
3. **Execute prompts** using the existing AI infrastructure
4. **Process results** to determine if it's HEARTBEAT_OK or requires action/delivery
5. **Integrate** with existing Telegram delivery and session management

## Architecture

```
HEARTBEAT.md → TaskInterpreter → JobExecutor → ResultProcessor → Actions
     ↓              ↓              ↓              ↓              ↓
  Raw tasks   →  AI prompts  →  AI response  →  Parsed result  →  Delivery
```

### Components

- **`TaskInterpreter`** - Reads and parses HEARTBEAT.md into structured tasks
- **`JobExecutor`** - Executes heartbeat tasks with AI integration and retry logic
- **`ResultProcessor`** - Analyzes AI responses and extracts actionable items
- **`GatewayIntegration`** - Bridges heartbeat execution with gateway cron system

## Usage

### Basic Integration

```go
// Initialize heartbeat integration in gateway
integration := heartbeat.NewGatewayIntegration(
    workspaceDir, 
    sessionsStore, 
    aiRouter, 
    scheduler, 
    channelSender,
)

// Gateway automatically routes heartbeat jobs
func (g *Gateway) executeScheduledJob(ctx context.Context, job *scheduler.Job) error {
    if heartbeat.IsHeartbeatJob(job) {
        return g.heartbeatIntegration.ExecuteHeartbeat(ctx, job)
    }
    // ... regular cron job logic
}
```

### Scheduling Heartbeat Jobs

```go
// Schedule a heartbeat job to run every 5 minutes
err := gateway.ScheduleHeartbeatJob(
    "*/5 * * * *",           // Cron schedule
    "telegram:123456789",    // Target for delivery
    "claude-sonnet-4",       // AI model
    true,                    // Enabled
)
```

### Manual Execution

```go
config := heartbeat.DefaultExecutorConfig()
executor := heartbeat.NewJobExecutor(workspaceDir, sessionsStore, config)

result, err := executor.ExecuteHeartbeatJob(ctx, aiExecutor)
if err != nil {
    log.Printf("Heartbeat execution failed: %v", err)
    return
}

if result.IsHeartbeatOK() {
    log.Println("Heartbeat OK - no action needed")
} else if result.HasAlerts() {
    log.Printf("Alerts found: %d actions", len(result.Actions))
}
```

## HEARTBEAT.md Format

The framework parses standard Markdown with specific patterns:

```markdown
# HEARTBEAT.md

## Check shared alert queue
Read `memory/alerts/pending.json`. If it contains any alerts:
- **critical** severity: Deliver to Jeff immediately via Telegram
- **warning** severity: Deliver to Jeff if he's likely awake (8 AM - 10 PM PT)
- **info** severity: Skip — save for the next briefing

After delivering, clear the queue:
```bash
python3 -c "import os; open(os.path.expanduser('~/conduit/alerts/pending.json'), 'w').write('[]')"
```

If no alerts (or only info-level), reply HEARTBEAT_OK.

## Check system status
Monitor critical systems and services:
- Database connectivity
- API endpoints health  
- Disk space usage

Report any issues immediately.
```

### Parsing Rules

- **Section headers** (`## Task Name`) define individual tasks
- **Bullet points** are parsed as instructions
- **Code blocks** are extracted for potential execution
- **Keywords** influence task type and priority:
  - `critical`, `urgent` → Critical priority
  - `alert`, `warning` → High priority  
  - `info`, `routine` → Low priority
  - `immediate` → Skip quiet hours
  - `awake`, `quiet hours` → Respect quiet hours

## Result Processing

The `ResultProcessor` analyzes AI responses using pattern matching:

### HEARTBEAT_OK Detection

Responses are considered "OK" if they contain:
- Explicit `HEARTBEAT_OK` 
- Phrases like "no alerts", "nothing needs attention", "all clear"
- Short responses with "no issues", "all good", etc.

### Action Extraction

Actions are extracted based on patterns:
- **Alerts**: `critical`, `urgent`, `warning`, `immediate`
- **Deliveries**: `deliver to`, `send to`, `notify`, `telegram`
- **Commands**: Code blocks, maintenance patterns

### Priority Inference

Content analysis determines action priority:
- **Critical**: `critical`, `failed`, `down`, `cannot`
- **High**: `warning`, `alert`, `important`, `issue`
- **Normal**: Default for unmatched content
- **Low**: `info`, `routine`, `maintenance`

## Action Types

### ActionTypeAlert
High-priority notifications requiring immediate attention:
```go
HeartbeatAction{
    Type:     ActionTypeAlert,
    Target:   "telegram",
    Content:  "🚨 CRITICAL: Database connection failed!",
    Priority: TaskPriorityCritical,
}
```

### ActionTypeDelivery  
Messages that may respect quiet hours:
```go
HeartbeatAction{
    Type:     ActionTypeDelivery,
    Target:   "telegram", 
    Content:  "Found 3 warning alerts for Jeff",
    Priority: TaskPriorityHigh,
    Metadata: map[string]interface{}{
        "quiet_aware": true,
    },
}
```

### ActionTypeNotification
General notifications:
```go
HeartbeatAction{
    Type:     ActionTypeNotification,
    Target:   "telegram",
    Content:  "💡 System backup completed successfully",
    Priority: TaskPriorityNormal,
}
```

### ActionTypeCommand
System commands or maintenance tasks:
```go
HeartbeatAction{
    Type:     ActionTypeCommand,
    Target:   "system",
    Content:  "Clear alert queue: python3 -c \"...\"",
    Priority: TaskPriorityLow,
    Metadata: map[string]interface{}{
        "command": "python3 -c \"...\""
    },
}
```

## Configuration

### ExecutorConfig

```go
config := heartbeat.ExecutorConfig{
    TimeoutSeconds:    60,                    // Task execution timeout
    SessionPrefix:     "heartbeat",           // Session naming prefix
    DefaultModel:      "claude-sonnet-4",     // Default AI model
    MaxRetries:        3,                     // Retry attempts
    RetryDelaySeconds: 5,                     // Delay between retries
}
```

### Quiet Hours Integration

The framework integrates with existing `AlertSeverityRouter` patterns:

```go
// Actions are categorized by quiet hours awareness
immediate, delayed := processor.FilterActionsByQuietHours(actions, isQuietHours)

// Critical/high priority actions are always immediate
// Other actions respect quiet hours if marked as quiet_aware
```

## Error Handling

### Task Reading Errors
```go
if _, err := os.Stat(heartbeatPath); os.IsNotExist(err) {
    return nil, fmt.Errorf("HEARTBEAT.md not found in workspace: %s", heartbeatPath)
}
```

### AI Execution Errors
```go
// Automatic retry logic with exponential backoff
for attempt := 0; attempt <= maxRetries; attempt++ {
    response, err := aiExecutor.ExecutePrompt(ctx, session, prompt, model)
    if err == nil {
        break
    }
    // Wait before retry...
}
```

### Validation Errors
```go
if err := result.Validate(); err != nil {
    return fmt.Errorf("invalid heartbeat result: %w", err)
}
```

## Testing

### Unit Tests
- `tasks_test.go` - Task parsing and interpretation
- `result_processor_test.go` - Response analysis and action extraction

### Integration Tests
- `integration_test.go` - End-to-end workflow testing with mocks

### Test Coverage
```bash
go test -cover ./internal/heartbeat
```

## Monitoring & Logging

The framework provides comprehensive logging:

```
[Heartbeat] Starting heartbeat task execution
[Heartbeat] Generated prompt for 2 tasks (1247 chars)
[Heartbeat] Completed execution: status=ok, action_count=0
[HeartbeatIntegration] Heartbeat OK - no action needed
```

### Metrics Integration
- Task execution count
- Success/failure rates  
- Response analysis results
- Action delivery status

## Security Considerations

### Command Execution
For safety, the framework logs commands but doesn't execute them directly:
```go
// Extract command from metadata if available
if command, ok := action.Metadata["command"].(string); ok {
    log.Printf("[HeartbeatIntegration] Extracted command: %s", command)
    // Command execution would require explicit allowlist
}
```

### Input Validation
- HEARTBEAT.md content is parsed safely
- Task validation prevents malformed data
- AI prompts are constructed with proper escaping

## Best Practices

### HEARTBEAT.md Structure
1. Use clear section headers for tasks
2. Include explicit conditional logic (`if alerts found...`)
3. Specify delivery targets and quiet hours behavior
4. End with clear success criteria (`reply HEARTBEAT_OK`)

### Scheduling
1. Use appropriate cron schedules (typically 5-15 minutes)
2. Configure reasonable timeouts (30-120 seconds)
3. Set proper retry limits (2-5 attempts)
4. Use appropriate AI models for task complexity

### Error Recovery
1. Implement proper retry logic for transient failures
2. Log detailed error information for debugging
3. Provide fallback behavior for critical tasks
4. Monitor execution patterns for anomalies

## Integration with Existing Systems

### Cron System
```go
// Heartbeat jobs are identified by metadata
func IsHeartbeatJob(job *scheduler.Job) bool {
    return job.Command == "heartbeat" || 
           (job.Metadata["heartbeat"] == true)
}
```

### Alert Processing
```go
// Leverages existing AlertSeverityRouter
// Respects Pacific timezone quiet hours (22:00-08:00)
// Integrates with SharedAlertQueue
```

### Session Management
```go
// Uses existing session store for AI context
sessionKey := fmt.Sprintf("heartbeat_%d", time.Now().UnixNano())
session, err := sessionsStore.GetOrCreateSession("heartbeat", sessionKey)
```

### Channel Delivery
```go
// Uses existing channel manager for message delivery
err := channelSender.SendMessage(ctx, channelID, userID, content)
```

This framework provides a robust, testable, and maintainable solution for executing HEARTBEAT.md tasks while leveraging all existing Conduit Go Gateway infrastructure.