package agent

import "strings"

// buildTopicSection renders the topic prefix the gateway attached to the
// turn ctx (types.TopicPrompt): stitched context, the "Topic: ..." line and
// the topic persona, in that order (conduit-43vk). Home turns carry no
// prompt and the section vanishes. It is a dynamic section so switching
// topics never invalidates the cached static prompt.
func buildTopicSection(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	return "## Topic\n" + prompt
}
