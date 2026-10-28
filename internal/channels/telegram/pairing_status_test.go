package telegram

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"

	"conduit/internal/database"
)

// expireCode moves code's expiry into the past, as if the user had waited
// longer than PairingCodeExpiration.
func expireCode(t *testing.T, db *sql.DB, code string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE telegram_pairings SET expires_at = ? WHERE code = ?`,
		database.FormatStoredTime(time.Now().Add(-time.Minute)), code); err != nil {
		t.Fatal(err)
	}
}

func mustNotBePaired(t *testing.T, pm *PairingManager, userID, when string) {
	t.Helper()
	paired, err := pm.IsUserPaired(userID)
	if err != nil {
		t.Fatalf("%s: IsUserPaired: %v", when, err)
	}
	if paired {
		t.Fatalf("%s: user %s is paired without admin approval", when, userID)
	}
}

// C1: a code superseded by a newer one is not an approval. Before the fix
// GeneratePairingCode set the old code to is_active=0, which IsUserPaired
// read as "approved".
func TestIsUserPaired_ExpiredThenRegeneratedIsNotPaired(t *testing.T) {
	pm, db := setupTestPairingManager(t)
	const user = "123456789"

	code, err := pm.GeneratePairingCode(user)
	if err != nil {
		t.Fatal(err)
	}
	mustNotBePaired(t, pm, user, "after first code")
	expireCode(t, db, code)
	mustNotBePaired(t, pm, user, "after expiry")
	if _, err := pm.GeneratePairingCode(user); err != nil {
		t.Fatal(err)
	}
	mustNotBePaired(t, pm, user, "after regenerate")

	// The superseded code can no longer be approved either.
	if err := pm.ApprovePairing(code); err == nil {
		t.Fatal("approving a superseded code succeeded")
	}
	mustNotBePaired(t, pm, user, "after approving the superseded code")
}

func TestIsUserPaired_RepeatedRegenerateNeverPairs(t *testing.T) {
	pm, _ := setupTestPairingManager(t)
	const user = "123456789"
	for i := 0; i < 5; i++ {
		if _, err := pm.GeneratePairingCode(user); err != nil {
			t.Fatal(err)
		}
		mustNotBePaired(t, pm, user, "regenerate")
	}
	stats, err := pm.GetPairingStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats["approved_pairings"] != 0 || stats["paired_users"] != 0 {
		t.Fatalf("stats count superseded codes as approvals: %v", stats)
	}
}

func TestIsUserPaired_ApprovedIsPaired(t *testing.T) {
	pm, _ := setupTestPairingManager(t)
	const user = "123456789"
	if _, err := pm.GeneratePairingCode(user); err != nil {
		t.Fatal(err)
	}
	code, err := pm.GeneratePairingCode(user)
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.ApprovePairing(code); err != nil {
		t.Fatal(err)
	}
	paired, err := pm.IsUserPaired(user)
	if err != nil || !paired {
		t.Fatalf("IsUserPaired after approval = %v, %v; want true", paired, err)
	}
	// Another user is unaffected.
	mustNotBePaired(t, pm, "987654321", "other user")
}

// The CLI approves through PairingStorage; that must also record approval.
func TestPairingStorage_ApproveMarksUserPaired(t *testing.T) {
	pm, db := setupTestPairingManager(t)
	const user = "123456789"
	old, err := pm.GeneratePairingCode(user)
	if err != nil {
		t.Fatal(err)
	}
	code, err := pm.GeneratePairingCode(user)
	if err != nil {
		t.Fatal(err)
	}
	storage := NewPairingStorage(db)
	if err := storage.ApprovePairing(old); err == nil {
		t.Fatal("CLI approved a superseded code")
	}
	mustNotBePaired(t, pm, user, "after CLI approve of superseded code")

	pending, err := storage.ListPendingPairings()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Code != code {
		t.Fatalf("pending = %+v, want only the newest code", pending)
	}
	if err := storage.ApprovePairing(code); err != nil {
		t.Fatal(err)
	}
	paired, err := pm.IsUserPaired(user)
	if err != nil || !paired {
		t.Fatalf("IsUserPaired after CLI approval = %v, %v; want true", paired, err)
	}
	got, err := storage.GetPairingByCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != PairingStatusApproved || got.IsActive {
		t.Fatalf("approved row = status %q active %v", got.Status, got.IsActive)
	}
}

// L13: codes are stored in the canonical UTC layout, so the CLI's SQL
// expiry filter (compared with a UTC "now") works in every zone.
func TestGeneratePairingCode_StoresCanonicalUTC(t *testing.T) {
	pm, db := setupTestPairingManager(t)
	code, err := pm.GeneratePairingCode("123456789")
	if err != nil {
		t.Fatal(err)
	}
	var created, expires string
	if err := db.QueryRow(`SELECT CAST(created_at AS TEXT), CAST(expires_at AS TEXT) FROM telegram_pairings WHERE code = ?`, code).Scan(&created, &expires); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{created, expires} {
		if _, err := time.Parse(database.StoredTimeLayout, v); err != nil {
			t.Fatalf("stored time %q is not in StoredTimeLayout: %v", v, err)
		}
	}
	pending, err := NewPairingStorage(db).ListPendingPairings()
	if err != nil || len(pending) != 1 || pending[0].Code != code {
		t.Fatalf("ListPendingPairings = %+v, %v; want the new code", pending, err)
	}
}

// fakeTelegram is a Bot API server that accepts sendMessage and records
// the texts sent.
func fakeTelegram(t *testing.T) (*bot.Bot, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			sent = append(sent, string(body))
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"private"}}}`))
	}))
	t.Cleanup(srv.Close)
	b, err := bot.New(fakeBotToken, bot.WithSkipGetMe(), bot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return b, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sent...)
	}
}

// End to end: a stranger messages, lets the code expire, then messages
// twice more. Before the fix the third message was let through.
func TestHandlePairingForUser_ExpiryDoesNotPair(t *testing.T) {
	pm, db := setupTestPairingManager(t)
	b, sent := fakeTelegram(t)
	ctx := context.Background()
	const user = "123456789"
	const chat int64 = 123456789

	for i := 1; i <= PairingRateLimit; i++ {
		if active, err := pm.GetActivePairingCode(user); err == nil {
			expireCode(t, db, active.Code)
		}
		ok, err := pm.HandlePairingForUser(ctx, b, user, chat)
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if ok {
			t.Fatalf("message %d: stranger was let through without approval", i)
		}
	}
	if n := len(sent()); n != PairingRateLimit {
		t.Fatalf("sent %d messages, want %d pairing-code messages", n, PairingRateLimit)
	}

	// Approval by the admin pairs the user.
	active, err := pm.GetActivePairingCode(user)
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.ApprovePairing(active.Code); err != nil {
		t.Fatal(err)
	}
	ok, err := pm.HandlePairingForUser(ctx, b, user, chat)
	if err != nil || !ok {
		t.Fatalf("after approval HandlePairingForUser = %v, %v; want true", ok, err)
	}
}
