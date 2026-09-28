package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

// conduit-3kgo: Start removes pairing codes that expired while the adapter
// was down; pending and paired rows are kept.
func TestAdapterStart_CleansUpExpiredPairingCodes(t *testing.T) {
	pm, db := setupTestPairingManager(t)

	now := time.Now()
	insert := func(code, user string, expires time.Time, active bool) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO telegram_pairings (code, user_id, created_at, expires_at, is_active, metadata) VALUES (?, ?, ?, ?, ?, '{}')`,
			code, user, now.Add(-2*time.Hour), expires, active); err != nil {
			t.Fatal(err)
		}
	}
	insert("expired", "u1", now.Add(-time.Hour), true)
	insert("pending", "u2", now.Add(time.Hour), true)
	insert("paired", "u3", now.Add(-time.Hour), false)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()

	a := &Adapter{
		name:       "tg",
		config:     TelegramConfig{BotToken: fakeBotToken},
		pairingMgr: pm,
		extraBotOptions: []bot.Option{
			bot.WithSkipGetMe(),
			bot.WithServerURL(srv.URL),
		},
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Stop() }()

	var codes []string
	rows, err := db.Query(`SELECT code FROM telegram_pairings ORDER BY code`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		codes = append(codes, c)
	}
	if len(codes) != 2 || codes[0] != "paired" || codes[1] != "pending" {
		t.Fatalf("codes after Start = %v, want [paired pending]", codes)
	}
}
