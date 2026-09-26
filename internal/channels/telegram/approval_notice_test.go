package telegram

import (
	"encoding/json"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"conduit/internal/approval"
	"conduit/internal/protocol"
)

// conduit-31jg.43: approval notices render verbatim (the model-output
// sanitizer would strip "<addr>" and could hide a recipient) with
// Approve/Deny inline buttons whose callback data is the typed reply.
func TestSendMessage_ApprovalNoticeVerbatimWithButtons(t *testing.T) {
	mb := &mockBot{}
	a := newTestAdapter(mb)
	defer a.cancel()

	choices, _ := json.Marshal([]approval.Choice{
		{Label: "Approve", Reply: "YES ABC234"},
		{Label: "Deny", Reply: "NO ABC234"},
	})
	text := "APPROVAL NEEDED [ABC234]\nTo: Boss <boss@corp.com>, <attacker@evil.com>\nSubject: *not bold*"
	err := a.SendMessage(&protocol.OutgoingMessage{
		UserID: "42",
		Text:   text,
		Metadata: map[string]string{
			approval.MetaNotice:  "1",
			approval.MetaChoices: string(choices),
		},
	})
	require.NoError(t, err)
	require.Len(t, mb.sendMessageCalls, 1)
	p := mb.sendMessageCalls[0]
	assert.Equal(t, text, p.Text, "notice must not be sanitized or markdown-converted")
	assert.Equal(t, models.ParseMode(""), p.ParseMode)
	kb, ok := p.ReplyMarkup.(*models.InlineKeyboardMarkup)
	require.True(t, ok, "expected inline keyboard")
	require.Len(t, kb.InlineKeyboard, 1)
	require.Len(t, kb.InlineKeyboard[0], 2)
	assert.Equal(t, "YES ABC234", kb.InlineKeyboard[0][0].CallbackData)
	assert.Equal(t, "NO ABC234", kb.InlineKeyboard[0][1].CallbackData)
}
