package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"conduit/internal/sessions"
	"conduit/internal/topics"
)

// Topic operations shared by the /topic command and the topic_* tools
// (conduit-43vk). Each returns the reply body; callers append the chat's
// current topic state (topicStateLine).

// topicError is a user-facing topic error that still matches its sentinel
// (topics.ErrNotFound, ...) with errors.Is.
type topicError struct {
	msg string
	err error
}

func (e *topicError) Error() string { return e.msg }
func (e *topicError) Unwrap() error { return e.err }

func errNoTopic(slug string) error {
	return &topicError{msg: fmt.Sprintf("No topic %s — /topic lists them.", topicSlugRef(slug)), err: topics.ErrNotFound}
}

var errNoTopicStore = errors.New("topics are not available")

func (g *Gateway) requireTopicStore() (*topics.Store, error) {
	if s := g.topicStore(); s != nil {
		return s, nil
	}
	return nil, errNoTopicStore
}

// lookupTopic normalizes slug and loads the topic.
func (g *Gateway) lookupTopic(slug string) (*topics.Topic, error) {
	store, err := g.requireTopicStore()
	if err != nil {
		return nil, err
	}
	norm, err := topics.NormalizeSlug(slug)
	if err != nil {
		return nil, errNoTopic(slug)
	}
	tp, err := store.Get(norm)
	if errors.Is(err, topics.ErrNotFound) {
		return nil, errNoTopic(norm)
	}
	return tp, err
}

// topicSession returns tp's session, creating it for the chat if missing.
func (g *Gateway) topicSession(tp *topics.Topic, channelID, userID string) (*sessions.Session, error) {
	if s, err := g.sessions.GetSession(tp.SessionKey); err == nil {
		return s, nil
	}
	if channelID == "" || userID == "" {
		return nil, fmt.Errorf("topic %s has no session yet and no chat to create it for", tp.Slug)
	}
	return g.sessions.GetOrCreateSessionByKey(tp.SessionKey, userID, channelID)
}

// topicStateLine echoes where the chat is now.
func (g *Gateway) topicStateLine(channelID, userID string) string {
	cur := "home"
	if store := g.topicStore(); store != nil {
		if tp, err := store.Active(topicChatKey(channelID, userID)); err == nil && tp != nil && tp.Status == topics.StatusActive {
			cur = tp.Slug
		}
	}
	return "📍 Current: " + topicSlugRef(cur)
}

// topicCreate creates a topic without switching to it. Its session is
// created lazily on first use.
func (g *Gateway) topicCreate(slugIn, name, persona string) (*topics.Topic, error) {
	store, err := g.requireTopicStore()
	if err != nil {
		return nil, err
	}
	slug, err := topics.NormalizeSlug(slugIn)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = slug
	}
	key := sessions.TopicSessionKeyPrefix + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	tp, err := store.Create(slug, strings.TrimSpace(name), key)
	if err != nil {
		return nil, err
	}
	if persona = strings.TrimSpace(persona); persona != "" {
		if err := store.SetPersona(slug, persona); err != nil {
			return nil, err
		}
		tp.Persona = persona
	}
	return tp, nil
}

// topicSwitch points the chat at slug, creating the topic when unknown and
// unparking (or reopening) it otherwise. The topic being left gets its
// summary refreshed.
func (g *Gateway) topicSwitch(ctx context.Context, channelID, userID, slugIn string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(slugIn), "home") {
		return g.topicGoHome(ctx, channelID, userID)
	}
	store, err := g.requireTopicStore()
	if err != nil {
		return "", err
	}
	chat := topicChatKey(channelID, userID)
	if chat == "" {
		return "", errors.New("no chat in context to switch")
	}
	slug, err := topics.NormalizeSlug(slugIn)
	if err != nil {
		return "", err
	}

	tp, err := store.Get(slug)
	created := false
	if errors.Is(err, topics.ErrNotFound) {
		if tp, err = g.topicCreate(slug, "", ""); err != nil {
			return "", err
		}
		created = true
	} else if err != nil {
		return "", err
	}

	if prev, _ := store.Active(chat); prev != nil && prev.Slug != tp.Slug && prev.Status == topics.StatusActive {
		g.summarizeTopic(ctx, prev)
	}

	note := ""
	switch tp.Status {
	case topics.StatusParked:
		note = " (unparked)"
	case topics.StatusArchived:
		note = " (reopened)"
	}
	if tp.Status != topics.StatusActive {
		if err := store.SetStatus(tp.Slug, topics.StatusActive); err != nil {
			return "", err
		}
	}
	sess, err := g.topicSession(tp, channelID, userID)
	if err != nil {
		return "", err
	}
	if err := store.SetPointer(chat, tp.Slug); err != nil {
		return "", err
	}

	if created {
		return fmt.Sprintf("🧵 Topic: %s — new", topicSlugRef(tp.Slug)), nil
	}
	summary := strings.TrimRight(strings.TrimSpace(tp.Summary), ". ")
	if summary == "" {
		summary = "no summary yet"
	}
	return fmt.Sprintf("🧵 Topic: %s — %s. Last activity: %s.%s", topicSlugRef(tp.Slug), summary, relativeTime(sess.UpdatedAt), note), nil
}

// topicGoHome clears the chat's pointer.
func (g *Gateway) topicGoHome(ctx context.Context, channelID, userID string) (string, error) {
	store, err := g.requireTopicStore()
	if err != nil {
		return "", err
	}
	chat := topicChatKey(channelID, userID)
	if chat == "" {
		return "", errors.New("no chat in context to switch")
	}
	if prev, _ := store.Active(chat); prev != nil && prev.Status == topics.StatusActive {
		g.summarizeTopic(ctx, prev)
	}
	if err := store.SetPointer(chat, ""); err != nil {
		return "", err
	}
	return "🏠 Back home.", nil
}

// topicPark summarizes slug, marks it parked and sends chats pointing at it
// home.
func (g *Gateway) topicPark(ctx context.Context, slug string) (string, error) {
	tp, err := g.lookupTopic(slug)
	if err != nil {
		return "", err
	}
	summary := g.summarizeTopic(ctx, tp)
	if err := g.topicStore().SetStatus(tp.Slug, topics.StatusParked); err != nil {
		return "", err
	}
	if _, err := g.topicStore().ClearPointersTo(tp.Slug); err != nil {
		return "", err
	}
	return fmt.Sprintf("🅿️ Parked %s. Summary: %s", topicSlugRef(tp.Slug), summary), nil
}

// topicClose writes a final summary to the topic and its session context,
// archives it and sends chats pointing at it home.
func (g *Gateway) topicClose(ctx context.Context, slug string) (string, error) {
	tp, err := g.lookupTopic(slug)
	if err != nil {
		return "", err
	}
	summary := g.summarizeTopic(ctx, tp)
	if _, err := g.sessions.GetSession(tp.SessionKey); err == nil {
		if err := g.sessions.SetSessionContext(tp.SessionKey, topicCtxSummary, summary); err != nil {
			return "", err
		}
	}
	if err := g.topicStore().SetStatus(tp.Slug, topics.StatusArchived); err != nil {
		return "", err
	}
	if _, err := g.topicStore().ClearPointersTo(tp.Slug); err != nil {
		return "", err
	}
	return fmt.Sprintf("📦 Closed %s. Final summary: %s", topicSlugRef(tp.Slug), summary), nil
}

// topicRename changes slug's display name and/or slug. Without newSlugIn the
// slug is derived from newName; without newName a default name (equal to
// the slug) follows the new slug and a custom one is kept. The store moves
// chat pointers in the same transaction; nothing else references the slug.
func (g *Gateway) topicRename(slug, newName, newSlugIn string) (string, error) {
	tp, err := g.lookupTopic(slug)
	if err != nil {
		return "", err
	}
	newName = strings.TrimSpace(newName)
	src := newSlugIn
	if strings.TrimSpace(src) == "" {
		src = newName
	}
	if strings.TrimSpace(src) == "" {
		return "", errors.New("rename needs a new name or slug")
	}
	newSlug, err := topics.NormalizeSlug(src)
	if err != nil {
		return "", err
	}
	if newName == "" {
		newName = tp.Name
		if tp.Name == tp.Slug {
			newName = newSlug
		}
	}
	if err := g.topicStore().Rename(tp.Slug, newSlug, newName); err != nil {
		return "", err
	}
	if newSlug == tp.Slug {
		return fmt.Sprintf("✏️ Renamed %s: name is now %q.", topicSlugRef(tp.Slug), newName), nil
	}
	return fmt.Sprintf("✏️ Renamed %s → %s (%s). The old slug no longer resolves.", topicSlugRef(tp.Slug), topicSlugRef(newSlug), newName), nil
}

// topicPersona sets or (with empty text) clears slug's persona.
func (g *Gateway) topicPersona(slug, persona string) (string, error) {
	tp, err := g.lookupTopic(slug)
	if err != nil {
		return "", err
	}
	persona = strings.TrimSpace(persona)
	if err := g.topicStore().SetPersona(tp.Slug, persona); err != nil {
		return "", err
	}
	if persona == "" {
		return fmt.Sprintf("🎭 Persona cleared for %s.", topicSlugRef(tp.Slug)), nil
	}
	return fmt.Sprintf("🎭 Persona set for %s.", topicSlugRef(tp.Slug)), nil
}

// topicStitch prepends from's summary to to's prompt prefix (stored in to's
// session context, replacing an earlier stitch from the same topic and
// keeping the newest within topicStitchMaxChars). from is unchanged apart
// from a summary refresh.
func (g *Gateway) topicStitch(ctx context.Context, channelID, userID, fromSlug, toSlug string) (string, error) {
	from, err := g.lookupTopic(fromSlug)
	if err != nil {
		return "", err
	}
	to, err := g.lookupTopic(toSlug)
	if err != nil {
		return "", err
	}
	if from.Slug == to.Slug {
		return "", errors.New("cannot stitch a topic into itself")
	}
	summary := g.summarizeTopic(ctx, from)
	sess, err := g.topicSession(to, channelID, userID)
	if err != nil {
		return "", err
	}

	marker := fmt.Sprintf("From topic %s ", topicSlugRef(from.Slug))
	lines := []string{fmt.Sprintf("%s(%s): %s", marker, from.Name, summary)}
	for _, l := range strings.Split(sess.Context[topicCtxStitched], "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, marker) {
			lines = append(lines, l)
		}
	}
	// Newest first; drop the oldest beyond the bound.
	for len(lines) > 1 && len(strings.Join(lines, "\n")) > topicStitchMaxChars {
		lines = lines[:len(lines)-1]
	}
	// Prepend order: oldest stitch first, newest right before the topic line.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if err := g.sessions.SetSessionContext(sess.Key, topicCtxStitched, strings.Join(lines, "\n")); err != nil {
		return "", err
	}
	return fmt.Sprintf("🪡 Stitched %s → %s.", topicSlugRef(from.Slug), topicSlugRef(to.Slug)), nil
}

// topicList renders the topics with ★ on the chat's current one.
func (g *Gateway) topicList(channelID, userID string) (string, error) {
	store, err := g.requireTopicStore()
	if err != nil {
		return "", err
	}
	list, err := store.List()
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "No topics yet. Start one with /topic <slug>.", nil
	}
	current, _ := store.Pointer(topicChatKey(channelID, userID))
	var b strings.Builder
	b.WriteString("🧵 *Topics*\n")
	for _, tp := range list {
		mark := "  "
		if tp.Slug == current && tp.Status == topics.StatusActive {
			mark = "★ "
		}
		last, tokens := tp.UpdatedAt, ""
		if s, err := g.sessions.GetSession(tp.SessionKey); err == nil {
			last = s.UpdatedAt
			if t := s.Context["last_prompt_tokens"]; t != "" {
				tokens = " · ~" + t + " tokens"
			}
		}
		stale := ""
		if tp.Status == topics.StatusActive && !last.IsZero() && time.Since(last) > topicStaleAfter {
			stale = " · stale"
		}
		fmt.Fprintf(&b, "%s%s — %s [%s] · %s%s%s\n", mark, topicSlugRef(tp.Slug), tp.Name, tp.Status, relativeTime(last), tokens, stale)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
