package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"conduit/internal/channels"
	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// restartBreadcrumbFile is written in DataDir by the draining instance and
// consumed by the next one.
const restartBreadcrumbFile = ".conduit-restart.json"

// restartBreadcrumbVersion 2 adds Turns (conduit-31jg.88). Version 0/1
// files (no "version", only active_sessions) are still read.
const restartBreadcrumbVersion = 2

// RestartBreadcrumb captures session state so the LLM can resume post-restart.
type RestartBreadcrumb struct {
	Version        int                 `json:"version,omitempty"`
	ActiveSessions []BreadcrumbSession `json:"active_sessions"`
	// Turns are the TurnRunner turns the drain saw, with their outcome
	// (conduit-31jg.88).
	Turns         []TurnSnapshot `json:"turns,omitempty"`
	TriggerAction string         `json:"trigger_action,omitempty"`
	Reason        string         `json:"reason"`
	Timestamp     time.Time      `json:"timestamp"`
}

// BreadcrumbSession is a session that gets the generic "restarted" note.
type BreadcrumbSession struct {
	SessionKey string `json:"session_key"`
	UserID     string `json:"user_id"`
	LastMsgID  string `json:"last_message_id,omitempty"`
	ChannelID  string `json:"channel_id,omitempty"`
	// Interrupted marks an entry mirrored from an interrupted turn so an
	// older binary still notes the restart there; newer ones handle it via
	// Turns instead (conduit-31jg.88).
	Interrupted bool `json:"interrupted,omitempty"`
}

func (g *Gateway) restartBreadcrumbPath() string {
	dataDir := ""
	if g.config != nil {
		dataDir = g.config.DataDir
	}
	if dataDir == "" {
		dataDir = "."
	}
	return filepath.Join(dataDir, restartBreadcrumbFile)
}

// needsRestartNotice reports whether an interrupted turn should be told
// about after the restart: only interactive turns (Telegram/channel, WS,
// TUI). Cron/heartbeat re-run on their own (conduit-31jg.77); sub-agents
// report the interruption to their parent (finishSubAgent). conduit-31jg.88
func needsRestartNotice(t TurnSnapshot) bool {
	if !t.Interactive() || t.SessionKey == "" {
		return false
	}
	return t.Outcome == TurnForceCancelled || t.Outcome == TurnDropped
}

// writeBreadcrumb records, after the drain resolved, the turns it saw and
// the WebSocket sessions connected at that point. Written atomically
// (temp file + rename), mode 0600.
func (sm *ShutdownManager) writeBreadcrumb(turns []TurnSnapshot) {
	gw := sm.gateway
	if gw == nil || gw.config == nil {
		return
	}

	var activeSessions []BreadcrumbSession
	seen := make(map[string]bool)
	if gw.ws != nil {
		gw.ws.ClientMu.RLock()
		for _, client := range gw.ws.Clients {
			sk := client.SessionKey() // conduit-31jg.25
			if sk != "" && !seen[sk] {
				seen[sk] = true
				activeSessions = append(activeSessions, BreadcrumbSession{
					SessionKey: sk,
					UserID:     client.UserID,
					ChannelID:  client.ID,
				})
			}
		}
		gw.ws.ClientMu.RUnlock()
	}
	interrupted := 0
	for _, t := range turns {
		if !needsRestartNotice(t) {
			continue
		}
		interrupted++
		if !seen[t.SessionKey] {
			seen[t.SessionKey] = true
			activeSessions = append(activeSessions, BreadcrumbSession{
				SessionKey:  t.SessionKey,
				UserID:      t.UserID,
				ChannelID:   t.ChannelID,
				LastMsgID:   t.UserMessageID,
				Interrupted: true,
			})
		} else {
			for i := range activeSessions {
				if activeSessions[i].SessionKey == t.SessionKey {
					activeSessions[i].Interrupted = true
				}
			}
		}
	}

	sm.mu.Lock()
	trigger := sm.triggerAction
	reason := sm.reason
	sm.mu.Unlock()

	breadcrumb := RestartBreadcrumb{
		Version:        restartBreadcrumbVersion,
		ActiveSessions: activeSessions,
		Turns:          turns,
		TriggerAction:  trigger,
		Reason:         reason,
		Timestamp:      time.Now(),
	}

	data, err := json.MarshalIndent(breadcrumb, "", "  ")
	if err != nil {
		sm.logger.Error("failed to marshal restart breadcrumb", "error", err)
		return
	}

	path := gw.restartBreadcrumbPath()
	if err := writeFileAtomic(path, data); err != nil {
		sm.logger.Error("failed to write restart breadcrumb", "error", err, "path", path)
		return
	}

	sm.logger.Info("restart breadcrumb written", "path", path,
		"sessions", len(activeSessions), "turns", len(turns), "interrupted", interrupted)
}

// writeFileAtomic writes data to path via a 0600 temp file in the same
// directory and a rename, so a reader never sees a partial breadcrumb.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// processRestartBreadcrumb reads the restart breadcrumb written by the
// previous gateway instance (if any). The file is removed BEFORE acting on
// it, so a crash loop can never repeat notices (conduit-31jg.88).
//
//   - Sessions in active_sessions (connected WebSocket clients; every entry
//     of an old-format file) get the generic "gateway restarted" note.
//   - Interactive turns the drain force-cancelled or dropped get, per
//     restart_resume: a notice on their channel (when an adapter can
//     deliver it) plus a matching transcript note ("notice", default), and
//     with "auto" a session wake that continues a force-cancelled turn.
func (g *Gateway) processRestartBreadcrumb() {
	path := g.restartBreadcrumbPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		// Could not consume it: acting now would repeat on every start.
		g.logger.Error("cannot remove restart breadcrumb; skipping restart notices", "path", path, "error", err)
		return
	}

	var breadcrumb RestartBreadcrumb
	if err := json.Unmarshal(data, &breadcrumb); err != nil {
		g.logger.Warn("failed to parse restart breadcrumb", "error", err, "path", path)
		return
	}

	resumed := 0
	for _, s := range breadcrumb.ActiveSessions {
		if s.Interrupted && breadcrumb.Version >= 2 {
			continue // handled from Turns below
		}
		session, err := g.sessions.GetSession(s.SessionKey)
		if err != nil || session == nil {
			g.logger.Debug("skipping stale session from breadcrumb", "session", s.SessionKey)
			continue
		}

		msg := fmt.Sprintf("Gateway restarted successfully at %s. Reason: %s. Previous sessions have been restored — you may continue where you left off.",
			breadcrumb.Timestamp.Format(time.RFC3339), breadcrumb.Reason)

		if _, err := g.sessions.AddMessage(s.SessionKey, "assistant", msg, nil); err != nil {
			g.logger.Warn("failed to inject restart resume message", "session", s.SessionKey, "error", err)
			continue
		}
		resumed++
	}

	notified := g.handleInterruptedTurns(breadcrumb.Turns)
	g.logger.Info("processed restart breadcrumb", "sessions_resumed", resumed,
		"interrupted_sessions_notified", notified, "reason", breadcrumb.Reason)
}

// Transcript metadata for restart notes (conduit-31jg.88).
const (
	restartNoticeSource    = "restart_notice"
	metaResumeInteractive  = "resume_interactive"
	restartResumeWakeNote  = "[System: The previous turn was interrupted by a gateway restart; continue where you left off. Interrupted request: %q]"
	restartNoticeNoPreview = "your last request"
)

// handleInterruptedTurns notifies (and, with restart_resume "auto",
// resumes) each session that had an interactive turn cut off by the
// drain. One notice per session. Returns the number of sessions handled.
func (g *Gateway) handleInterruptedTurns(turns []TurnSnapshot) int {
	mode := config.RestartResumeNotice
	if g.config != nil {
		mode = g.config.RestartResumeMode()
	}
	bySession := make(map[string][]TurnSnapshot)
	var order []string
	for _, t := range turns {
		if !needsRestartNotice(t) {
			continue
		}
		if _, ok := bySession[t.SessionKey]; !ok {
			order = append(order, t.SessionKey)
		}
		bySession[t.SessionKey] = append(bySession[t.SessionKey], t)
	}
	if len(order) == 0 {
		return 0
	}
	if mode == config.RestartResumeOff {
		g.logger.Info("restart_resume=off: not notifying interrupted turns", "sessions", len(order))
		return 0
	}

	handled := 0
	for _, key := range order {
		if g.notifyInterruptedSession(key, bySession[key], mode) {
			handled++
		}
	}
	return handled
}

func (g *Gateway) notifyInterruptedSession(key string, turns []TurnSnapshot, mode string) bool {
	session, err := g.sessions.GetSession(key)
	if err != nil || session == nil {
		g.logger.Debug("interrupted turn: session gone", "session", key)
		return false
	}

	var cut *TurnSnapshot
	var dropped []TurnSnapshot
	for i := range turns {
		if turns[i].Outcome == TurnForceCancelled && cut == nil {
			cut = &turns[i]
		} else {
			dropped = append(dropped, turns[i])
		}
	}
	resume := mode == config.RestartResumeAuto && cut != nil

	quote := func(t TurnSnapshot) string {
		if t.Preview == "" {
			return restartNoticeNoPreview
		}
		return fmt.Sprintf("%q", t.Preview)
	}
	var lines []string
	if cut != nil {
		line := "⚠️ I was restarted while working on: " + quote(*cut) + "."
		if resume {
			line += " Picking it back up now."
		} else {
			line += ` Reply "continue" to resume, or resend.`
		}
		lines = append(lines, line)
	}
	for _, t := range dropped {
		lines = append(lines, "⚠️ I restarted before I got to: "+quote(t)+". Please resend it.")
	}
	text := strings.Join(lines, "\n")

	// Transcript note: always (WS/TUI clients read it on reconnect).
	if _, err := g.sessions.AddMessage(key, "assistant", text, map[string]string{"source": restartNoticeSource}); err != nil {
		g.logger.Warn("interrupted turn: failed to record restart note", "session", key, "error", err)
	}

	// Channel notice: best-effort, only where an adapter can deliver it now
	// (Telegram; not WS/TUI clients that have not reconnected yet).
	ref := turns[0]
	if cut != nil {
		ref = *cut
	}
	if g.channelDeliverable(ref.ChannelID) && ref.UserID != "" {
		if err := g.SendMessage(context.Background(), ref.ChannelID, ref.UserID, channels.SanitizeOutgoingText(text), nil); err != nil {
			g.logger.Warn("interrupted turn: failed to send restart notice", "session", key, "channel", ref.ChannelID, "error", err)
		}
	}

	if resume {
		note := fmt.Sprintf(restartResumeWakeNote, cut.Preview)
		md := map[string]string{
			"source":              types.WakeSourceRestartResume,
			"wake_source":         types.WakeSourceRestartResume,
			metaResumeInteractive: fmt.Sprint(cut.Interactive()),
		}
		if _, err := g.sessions.AddMessage(key, "user", note, md); err != nil {
			g.logger.Warn("interrupted turn: failed to queue resume", "session", key, "error", err)
		} else {
			g.enqueueSessionWake(key)
		}
	}
	g.logger.Info("interrupted turn: restart notice recorded", "session", key,
		"force_cancelled", cut != nil, "dropped", len(dropped), "auto_resume", resume)
	return true
}

// channelDeliverable reports whether a channel adapter is registered for
// channelID (Telegram and other adapters; WebSocket/TUI sessions are not).
func (g *Gateway) channelDeliverable(channelID string) bool {
	if g.channelManager == nil || channelID == "" {
		return false
	}
	_, ok := g.channelManager.GetAdapter(channelID)
	return ok
}
