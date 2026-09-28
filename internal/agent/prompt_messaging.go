package agent

import (
	"fmt"
	"strings"
)

// buildMessagingSection returns messaging tool instructions
func buildMessagingSection(params *SectionParams) string {
	if params.IsMinimal {
		return ""
	}

	var builder strings.Builder
	builder.WriteString(`## Messaging
- Reply in current session → automatically routes to the source channel (Signal, Telegram, etc.)
- Cross-session messaging → use sessions_send(sessionKey, message)
- Never use exec/curl for provider messaging; Conduit handles all routing internally.
`)

	if params.AvailableTools["StatusUpdate"] {
		builder.WriteString(`
### Progress Updates
During multi-step or long-running tasks, use StatusUpdate to keep the user informed:
- At major phases: "Searching codebase for authentication files..."
- After findings: "Found 5 matching files, analyzing patterns..."
- When pivoting: "Initial search found nothing, trying broader query..."
Keep updates concise (1 sentence). Don't spam — roughly every 3-5 tool calls or when something meaningful happens.
`)
	}

	if params.AvailableTools["Message"] {
		channelOptions := strings.Join(SupportedChannels, "|")
		if len(params.MessageChannels) > 0 {
			channelOptions = strings.Join(params.MessageChannels, "|")
		}

		builder.WriteString(fmt.Sprintf(`
### message tool
- Use %cmessage%c for proactive sends + channel actions (polls, reactions, etc.).
- For %caction=send%c, include %ctarget%c and %cmessage%c.
- If multiple channels are configured, pass %cchannel%c (%s).
- If you use %cmessage%c (%caction=send%c) to deliver your user-visible reply, respond with ONLY: %s (avoid duplicate replies).
`, '`', '`', '`', '`', '`', '`', '`', '`', '`', '`', channelOptions, '`', '`', '`', '`', SILENT_REPLY_TOKEN))

		if params.InlineButtons {
			builder.WriteString("- Inline buttons supported. Use `action=send` with `buttons=[[{text,callback_data}]]` (callback_data routes back as a user message).\n")
		}
	}

	builder.WriteString("\n")
	return builder.String()
}

// buildVoiceSection returns TTS instructions if enabled
func buildVoiceSection(params *SectionParams) string {
	if params.IsMinimal || !params.TTSEnabled {
		return ""
	}

	voice := params.TTSVoice
	if voice == "" {
		voice = "default voice"
	}

	return fmt.Sprintf(`## Voice (TTS)
TTS is available via the tts tool. Voice: %s.
Use for audio responses when requested or when TTS mode is enabled.
Copy the MEDIA line exactly when returning audio.
`, voice)
}

// buildReplyTagsSection returns reply tag instructions
func buildReplyTagsSection(isMinimal bool) string {
	if isMinimal {
		return ""
	}

	return `## Reply Tags
To request a native reply/quote on supported surfaces, include one tag in your reply:
- [[reply_to_current]] replies to the triggering message.
- [[reply_to:<id>]] replies to a specific message id when you have it.
Whitespace inside the tag is allowed (e.g. [[ reply_to_current ]] / [[ reply_to: 123 ]]).
Tags are stripped before sending; support depends on the current channel config.
`
}

// buildSilentRepliesSection returns detailed silent reply instructions
func buildSilentRepliesSection(isMinimal bool) string {
	if isMinimal {
		return ""
	}

	return fmt.Sprintf(`## Silent Replies
When you have nothing to say, respond with ONLY: %s
It must be your entire message — no other text, no markdown wrapping, no code blocks.
`, SILENT_REPLY_TOKEN)
}

// buildReactionsSection returns reaction guidelines if enabled
func buildReactionsSection(params *SectionParams) string {
	if params.IsMinimal || !params.ReactionsEnabled {
		return ""
	}

	mode := params.ReactionsMode
	if mode == "" {
		mode = "MINIMAL"
	}

	var guidance string
	switch strings.ToUpper(mode) {
	case "ALWAYS":
		guidance = "React freely when appropriate."
	case "MINIMAL":
		guidance = `React ONLY when truly relevant:
- Acknowledge important user requests or confirmations
- Express genuine sentiment (humor, appreciation) sparingly
- Avoid reacting to routine messages or your own replies
Guideline: at most 1 reaction per 5-10 exchanges.`
	default:
		guidance = "React when it feels natural."
	}

	return fmt.Sprintf(`## Reactions
Reactions are enabled for %s in %s mode.
%s
`, params.RuntimeChannel, mode, guidance)
}

// buildCronDeliverySection returns instructions for cron/scheduled job output delivery.
// This is injected only for sessions with a "cron_" prefix to ensure scheduled jobs
// use the Message tool and do not attempt shell-based delivery.
func buildCronDeliverySection(params *SectionParams) string {
	if !params.AvailableTools["Message"] {
		return ""
	}

	return `## Cron Delivery
You are running as a scheduled job with no interactive session.
Deliver output ONLY via Message(action="send", target="<chat_id>"). Shell commands cannot reach the user.
If nothing to report, do not send a message.
`
}
