package communication

import (
	"context"
	"strings"

	"conduit/internal/policy"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// ClassifyActions implements policy.Classifier (conduit-25lt.2). send and
// broadcast reach a chat; the other actions are not implemented and reach
// nobody. Targets resolve exactly as the send path does: "channel:id", or a
// bare channel name meaning the current turn's user.
func (t *MessageTool) ClassifyActions(ctx context.Context, args map[string]interface{}) []policy.Action {
	var targets []string
	switch toolargs.GetString(args, "action", "send") {
	case "send":
		if tgt := toolargs.GetString(args, "target", ""); tgt != "" {
			targets = []string{tgt}
		}
	case "broadcast":
		if list, ok := args["targets"].([]interface{}); ok {
			for _, v := range list {
				if s, ok := v.(string); ok && s != "" {
					targets = append(targets, s)
				}
			}
		}
	}
	actions := make([]policy.Action, 0, len(targets))
	for _, tgt := range targets {
		actions = append(actions, classifyMessageTarget(ctx, tgt))
	}
	return actions
}

// classifyMessageTarget maps one target to a message class. Telegram group
// and channel chats have negative IDs; a TUI chat is always the owner's own
// terminal. The engine turns a DM to one of the owner's targets into
// message.self.
func classifyMessageTarget(ctx context.Context, target string) policy.Action {
	channelID, userID := parseTarget(target)
	if userID == "" {
		userID = types.RequestUserID(ctx)
	}
	resolved := channelID + ":" + userID
	switch {
	case strings.HasPrefix(channelID, "tui"):
		return policy.Action{Class: policy.MessageSelf, Target: resolved}
	case strings.HasPrefix(userID, "-"):
		return policy.Action{Class: policy.MessageGroup, Target: resolved}
	default:
		return policy.Action{Class: policy.MessageDM, Target: resolved}
	}
}
