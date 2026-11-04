package gateway

import (
	"context"
	"strings"

	"conduit/internal/tools/core"
	"conduit/internal/tools/types"
)

const topicUsage = `*Usage — topic commands*
/topic — list topics (★ = current)
/topic <slug> — switch (creates it if new)
/topic home — back to home (same as /home)
/topic park <slug> — summarize and park
/topic close <slug> — final summary and archive
/topic rename <slug> <new name or slug>
/topic persona <slug> [text] — set; empty clears
/topic stitch <from> <to> — hand <from>'s summary to <to>`

// topicCommand handles /topic and /home (conduit-43vk). It returns the reply,
// always ending with the chat's current topic state, and whether text was a
// topic command at all. Unless ctx carries the owner's access
// (withTopicOwnerAccess) it acts only on the chat's own topics (H1).
func (g *Gateway) topicCommand(ctx context.Context, channelID, userID, text string) (string, bool) {
	text = strings.TrimSpace(text)
	fields := strings.Fields(text)
	if len(fields) == 0 || (fields[0] != "/topic" && fields[0] != "/home") {
		return "", false
	}

	reply := func(body string, err error) (string, bool) {
		if err != nil {
			body = "❌ " + err.Error()
		}
		return body + "\n\n" + g.topicStateLine(channelID, userID), true
	}
	usage := func() (string, bool) { return reply(topicUsage, nil) }
	sc := commandTopicScope(ctx, channelID, userID)

	if fields[0] == "/home" {
		return reply(g.topicGoHome(ctx, sc))
	}
	if len(fields) == 1 {
		return reply(g.topicList(sc))
	}

	args := fields[2:]
	switch strings.ToLower(fields[1]) {
	case "home":
		return reply(g.topicGoHome(ctx, sc))
	case "list":
		return reply(g.topicList(sc))
	case "help":
		return usage()
	case "park":
		if len(args) != 1 {
			return usage()
		}
		return reply(g.topicPark(ctx, sc, args[0]))
	case "close":
		if len(args) != 1 {
			return usage()
		}
		return reply(g.topicClose(ctx, sc, args[0]))
	case "rename":
		if len(args) < 2 {
			return usage()
		}
		return reply(g.topicRename(sc, args[0], textAfterFields(text, 3), ""))
	case "persona":
		if len(args) < 1 {
			return usage()
		}
		return reply(g.topicPersona(sc, args[0], textAfterFields(text, 3)))
	case "stitch":
		if len(args) != 2 {
			return usage()
		}
		return reply(g.topicStitch(ctx, sc, args[0], args[1]))
	}
	if len(fields) != 2 {
		return usage() // unknown subcommand
	}
	return reply(g.topicSwitch(ctx, sc, fields[1]))
}

// textAfterFields returns text after its first n whitespace-separated
// fields, with the remainder's inner spacing preserved.
func textAfterFields(text string, n int) string {
	rest := strings.TrimSpace(text)
	for i := 0; i < n && rest != ""; i++ {
		if j := strings.IndexAny(rest, " \t\n"); j >= 0 {
			rest = strings.TrimSpace(rest[j:])
		} else {
			rest = ""
		}
	}
	return rest
}

// The topic_* tools call these (core.TopicManager). They act on the chat in
// the request context and end with its current topic state, like the
// command replies; failures are returned as errors. Interactive turns act
// as the owner; any other turn only on its chat's own topics (H1).
var _ core.TopicManager = (*Gateway)(nil)

func (g *Gateway) withTopicState(ctx context.Context, body string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return body + "\n\n" + g.topicStateLine(types.RequestChannelID(ctx), types.RequestUserID(ctx)), nil
}

// TopicList implements core.TopicManager.
func (g *Gateway) TopicList(ctx context.Context) (string, error) {
	body, err := g.topicList(toolTopicScope(ctx))
	return g.withTopicState(ctx, body, err)
}

// TopicCreate implements core.TopicManager.
func (g *Gateway) TopicCreate(ctx context.Context, slug, name, persona string) (string, error) {
	tp, err := g.topicCreate(toolTopicScope(ctx), slug, name, persona)
	if err != nil {
		return "", err
	}
	return g.withTopicState(ctx, "🧵 Created topic "+topicSlugRef(tp.Slug)+" ("+tp.Name+"). Use topic_switch to move the chat there.", nil)
}

// TopicSwitch implements core.TopicManager.
func (g *Gateway) TopicSwitch(ctx context.Context, slug string) (string, error) {
	body, err := g.topicSwitch(ctx, toolTopicScope(ctx), slug)
	return g.withTopicState(ctx, body, err)
}

// TopicRename implements core.TopicManager.
func (g *Gateway) TopicRename(ctx context.Context, slug, newName, newSlug string) (string, error) {
	body, err := g.topicRename(toolTopicScope(ctx), slug, newName, newSlug)
	return g.withTopicState(ctx, body, err)
}

// TopicPark implements core.TopicManager.
func (g *Gateway) TopicPark(ctx context.Context, slug string) (string, error) {
	body, err := g.topicPark(ctx, toolTopicScope(ctx), slug)
	return g.withTopicState(ctx, body, err)
}

// TopicClose implements core.TopicManager.
func (g *Gateway) TopicClose(ctx context.Context, slug string) (string, error) {
	body, err := g.topicClose(ctx, toolTopicScope(ctx), slug)
	return g.withTopicState(ctx, body, err)
}

// TopicStitch implements core.TopicManager.
func (g *Gateway) TopicStitch(ctx context.Context, from, to string) (string, error) {
	body, err := g.topicStitch(ctx, toolTopicScope(ctx), from, to)
	return g.withTopicState(ctx, body, err)
}

// TopicPersona implements core.TopicManager.
func (g *Gateway) TopicPersona(ctx context.Context, slug, persona string) (string, error) {
	body, err := g.topicPersona(toolTopicScope(ctx), slug, persona)
	return g.withTopicState(ctx, body, err)
}

// TopicProfile implements core.TopicManager.
func (g *Gateway) TopicProfile(ctx context.Context, slug, profile string) (string, error) {
	body, err := g.topicProfile(toolTopicScope(ctx), slug, profile)
	return g.withTopicState(ctx, body, err)
}
