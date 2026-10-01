package auth

import "testing"

func TestParseRole(t *testing.T) {
	for in, want := range map[string]string{"owner": RoleOwner, " Automation ": RoleAutomation} {
		if got, err := ParseRole(in); err != nil || got != want {
			t.Errorf("ParseRole(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "admin", "own"} {
		if _, err := ParseRole(bad); err == nil {
			t.Errorf("ParseRole(%q) should fail", bad)
		}
	}
}

// Tokens created before roles existed are owner (decision 2026-10-01) and
// reported as not explicit so the gateway can warn.
func TestTokenRole_DefaultForUntagged(t *testing.T) {
	for _, md := range []map[string]string{nil, {}, {"role": "bogus"}} {
		if role, explicit := TokenRole(md); role != RoleOwner || explicit {
			t.Errorf("TokenRole(%v) = %q, %v; want owner, not explicit", md, role, explicit)
		}
	}
	if role, explicit := TokenRole(map[string]string{"role": "automation"}); role != RoleAutomation || !explicit {
		t.Errorf("got %q, %v", role, explicit)
	}
}

func TestSetTokenRole_KeepsOtherMetadata(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ts := NewTokenStorage(db, "test-secret")

	resp, err := ts.CreateToken(CreateTokenRequest{ClientName: "bot", Metadata: map[string]string{"team": "ops"}})
	if err != nil {
		t.Fatal(err)
	}
	id := resp.TokenInfo.TokenID
	if err := ts.SetTokenRole(id, "Automation"); err != nil {
		t.Fatal(err)
	}
	info, err := ts.GetTokenInfo(id)
	if err != nil {
		t.Fatal(err)
	}
	if info.Metadata["role"] != RoleAutomation || info.Metadata["team"] != "ops" {
		t.Fatalf("metadata = %v", info.Metadata)
	}
	// The role reaches the authenticated token info the gateway reads.
	got, err := ts.ValidateToken(resp.Token)
	if err != nil {
		t.Fatal(err)
	}
	if role, explicit := TokenRole(got.Metadata); role != RoleAutomation || !explicit {
		t.Fatalf("validated token role = %q, %v", role, explicit)
	}

	if err := ts.SetTokenRole(id, "admin"); err == nil {
		t.Fatal("invalid role accepted")
	}
	if err := ts.SetTokenRole("no-such-token", RoleOwner); err == nil {
		t.Fatal("unknown token accepted")
	}
}

// conduit-3ryz: tokens are found by a prefix of their token ID (what `token
// list` shows), not by a made-up token prefix.
func TestFindTokenByPrefix_TokenID(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ts := NewTokenStorage(db, "test-secret")
	a, err := ts.CreateToken(CreateTokenRequest{ClientName: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.CreateToken(CreateTokenRequest{ClientName: "b"}); err != nil {
		t.Fatal(err)
	}

	id := a.TokenInfo.TokenID
	got, err := findTokenByPrefix(ts, displayID(id))
	if err != nil || got != id {
		t.Fatalf("findTokenByPrefix(%q) = %q, %v; want %q", displayID(id), got, err, id)
	}
	if _, err := findTokenByPrefix(ts, "claw_v1_"+id[:4]); err == nil {
		t.Fatal("the old fake display prefix must not match")
	}
	if _, err := findTokenByPrefix(ts, ""); err == nil {
		t.Fatal("empty prefix must not match every token")
	}
}
