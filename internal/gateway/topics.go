package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"conduit/internal/sessions"
	"conduit/internal/topics"
)

// Topic switcher (conduit-43vk): chat → active-topic pointer → session.
// Each topic owns a session (own transcript, compaction and token budget);
// home is the chat's ordinary session and is not a topic row.

// Session-context keys written on topic sessions.
const (
	// topicCtxSummary holds the final summary written by close.
	topicCtxSummary = "topic_summary"
	// topicCtxStitched holds context stitched in from other topics; it is
	// prepended to the topic prefix until cleared.
	topicCtxStitched = "topic_stitched"
	// topicCtxSummaryCount is the message count the stored summary covers,
	// so unchanged topics are not re-summarized.
	topicCtxSummaryCount = "topic_summary_msgs"
)

// Bounds that keep the injected prefix small: the point of topics is leaner
// LLM calls, so the prefix never carries a transcript.
const (
	topicSummaryMaxChars  = 600
	topicStitchMaxChars   = 1200
	topicPersonaMaxChars  = 1000
	topicSummaryTimeout   = 15 * time.Second
	topicSummaryMaxMsgs   = 40
	topicSummaryMsgChars  = 500
	topicSummaryInputMax  = 8000
	topicStaleAfter       = 7 * 24 * time.Hour
	topicTemplateLastUser = 160
)

// topicStore returns the topic store over the gateway DB, or nil when the
// gateway has no session store.
func (g *Gateway) topicStore() *topics.Store {
	g.topicsOnce.Do(func() {
		if g.sessions != nil {
			g.topics = topics.NewStore(g.sessions.DB())
		}
	})
	return g.topics
}

// topicChatKey identifies a chat for its topic pointer: the channel adapter
// plus the channel's chat/user ID ("telegram:42").
func topicChatKey(channelID, userID string) string {
	if channelID == "" || userID == "" {
		return ""
	}
	return channelID + ":" + userID
}

// resolveSessionForChat is the single choke point mapping an incoming chat
// message to its session. With no pointer (or a home pointer) it is exactly
// GetOrCreateSession. A pointer to an active topic returns that topic's
// session, created lazily under the topic's key. A pointer to a topic that
// is missing, parked or archived is reset to home and notice says so once.
func (g *Gateway) resolveSessionForChat(channelID, userID string) (*sessions.Session, string, error) {
	var notice string
	if store := g.topicStore(); store != nil {
		chat := topicChatKey(channelID, userID)
		tp, err := store.Active(chat)
		switch {
		case err == nil && tp == nil:
			// home
		case err == nil && tp.Status == topics.StatusActive:
			s, err := g.sessions.GetOrCreateSessionByKey(tp.SessionKey, userID, channelID)
			if err != nil {
				return nil, "", fmt.Errorf("topic %s session: %w", tp.Slug, err)
			}
			return s, "", nil
		case err == nil || errors.Is(err, topics.ErrNotFound):
			slug, _ := store.Pointer(chat)
			if perr := store.SetPointer(chat, ""); perr != nil {
				g.logger.Warn("topics: failed to reset stale pointer", "chat", chat, "error", perr)
			}
			notice = fmt.Sprintf("ℹ️ Topic %q is no longer active — you're back home.", slug)
		default:
			// Never let the topics table break plain messaging.
			g.logger.Warn("topics: pointer lookup failed, using home", "chat", chat, "error", err)
		}
	}
	s, err := g.sessions.GetOrCreateSession(userID, channelID)
	return s, notice, err
}

// isTopicSessionKey reports whether key belongs to a topic's session.
func isTopicSessionKey(key string) bool {
	return strings.HasPrefix(key, sessions.TopicSessionKeyPrefix)
}

// topicTagPrefix returns "[slug] " when sessionKey is a topic session and
// the destination is that session's chat, "" otherwise (home, other chats).
func (g *Gateway) topicTagPrefix(sessionKey, channelID, userID string) string {
	if !isTopicSessionKey(sessionKey) || g.topicStore() == nil {
		return ""
	}
	tp, err := g.topicStore().GetBySession(sessionKey)
	if err != nil {
		return ""
	}
	if s, err := g.sessions.GetSession(sessionKey); err != nil || s.ChannelID != channelID || s.UserID != userID {
		return ""
	}
	return "[" + tp.Slug + "] "
}

// topicTag prefixes text with the topic tag (see topicTagPrefix), never
// twice and never on empty text.
func (g *Gateway) topicTag(sessionKey, channelID, userID, text string) string {
	return applyTopicTag(g.topicTagPrefix(sessionKey, channelID, userID), text)
}

func applyTopicTag(tag, text string) string {
	if tag == "" || text == "" || strings.HasPrefix(text, tag) {
		return text
	}
	return tag + text
}

// topicPromptFor renders the prefix injected into a topic session's prompt:
// stitched context (prepended), then "Topic: <name> — <summary>. Last
// activity: <relative time>.", then the topic persona. Home sessions get "".
// session.UpdatedAt is read before the turn's user message is stored, so
// "last activity" is the time since the topic's previous exchange.
func (g *Gateway) topicPromptFor(session *sessions.Session) string {
	if session == nil || !isTopicSessionKey(session.Key) || g.topicStore() == nil {
		return ""
	}
	tp, err := g.topicStore().GetBySession(session.Key)
	if err != nil {
		return ""
	}
	var lines []string
	if st := strings.TrimSpace(session.Context[topicCtxStitched]); st != "" {
		lines = append(lines, truncateRunes(st, topicStitchMaxChars))
	}
	summary := strings.TrimRight(strings.TrimSpace(tp.Summary), ". ")
	if summary == "" {
		summary = "new"
	}
	lines = append(lines, fmt.Sprintf("Topic: %s — %s. Last activity: %s.",
		tp.Name, truncateRunes(summary, topicSummaryMaxChars), relativeTime(session.UpdatedAt)))
	if p := strings.TrimSpace(tp.Persona); p != "" {
		lines = append(lines, truncateRunes(p, topicPersonaMaxChars))
	}
	return strings.Join(lines, "\n")
}

// relativeTime renders t as "just now", "5m ago", "3h ago" or "4d ago".
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// summarizeTopic refreshes tp's stored summary when its session has new
// messages since the last one, and returns the current summary.
func (g *Gateway) summarizeTopic(ctx context.Context, tp *topics.Topic) string {
	sess, err := g.sessions.GetSession(tp.SessionKey)
	if err != nil {
		if tp.Summary == "" {
			return "No activity yet."
		}
		return tp.Summary
	}
	count := strconv.Itoa(sess.MessageCount)
	if tp.Summary != "" && sess.Context[topicCtxSummaryCount] == count {
		return tp.Summary
	}
	summary := truncateRunes(strings.Join(strings.Fields(g.generateTopicSummary(ctx, sess)), " "), topicSummaryMaxChars)
	if err := g.topicStore().SetSummary(tp.Slug, summary); err != nil {
		g.logger.Warn("topics: failed to store summary", "topic", tp.Slug, "error", err)
		return summary
	}
	_ = g.sessions.SetSessionContext(sess.Key, topicCtxSummaryCount, count)
	tp.Summary = summary
	return summary
}

// generateTopicSummary produces a 2-3 sentence state summary of a topic
// session. It follows the /reset reflection path (SPAR metrics when
// reflection is enabled, bounded by a timeout) and asks the model for the
// summary through a throwaway session, like the workspace summary adapter.
// Without AI, or on any failure, it falls back to a short template.
func (g *Gateway) generateTopicSummary(ctx context.Context, sess *sessions.Session) string {
	if g.topicSummarize != nil {
		return g.topicSummarize(ctx, sess)
	}
	msgs, _ := g.sessions.GetMessages(sess.Key, topicSummaryMaxMsgs)
	fallback := topicSummaryTemplate(msgs)
	if g.ai == nil || len(msgs) == 0 {
		return fallback
	}

	if g.cognition.ReflectionEnabled() && sess.MessageCount > 2 {
		reflCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		g.reflectHighConfidencePost(reflCtx, sess)
		cancel()
	}

	var b strings.Builder
	b.WriteString("Summarize the current state of this project conversation in 2-3 plain sentences: " +
		"what it is about, what was done, and what is next. Reply with the summary only.\n\n")
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		line := fmt.Sprintf("%s: %s\n", m.Role, truncateRunes(strings.TrimSpace(m.Content), topicSummaryMsgChars))
		if b.Len()+len(line) > topicSummaryInputMax {
			break
		}
		b.WriteString(line)
	}

	sumCtx, cancel := context.WithTimeout(ctx, topicSummaryTimeout)
	defer cancel()
	temp := &sessions.Session{Key: "summary_temp", Context: map[string]string{"model": sess.Context["model"]}}
	resp, err := g.ai.GenerateResponse(sumCtx, temp, b.String(), "")
	if err != nil || resp == nil || strings.TrimSpace(resp.Content) == "" {
		if err != nil {
			g.logger.Warn("topics: summary generation failed, using template", "session_key", sess.Key, "error", err)
		}
		return fallback
	}
	return strings.TrimSpace(resp.Content)
}

// topicSummaryTemplate is the no-AI summary: message count plus the last
// user request, truncated.
func topicSummaryTemplate(msgs []sessions.Message) string {
	if len(msgs) == 0 {
		return "No activity yet."
	}
	last := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			last = strings.Join(strings.Fields(msgs[i].Content), " ")
			break
		}
	}
	if last == "" {
		return fmt.Sprintf("%d messages so far.", len(msgs))
	}
	return fmt.Sprintf("%d messages so far. Last request: %q.", len(msgs), truncateRunes(last, topicTemplateLastUser))
}
