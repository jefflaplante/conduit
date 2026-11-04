package gateway

import (
	"errors"
	"time"

	"conduit/internal/auth"
)

// Live-connection token re-validation (H2). A WS client's token and role
// are checked once at the upgrade, and `conduit token revoke` / `set-role`
// run in another process, so the in-process OnRevoke hook never fires for
// them: a revoked, demoted or expired token kept its connection (and owner
// powers) until a restart. WebSocketService.TokenCheck runs this on every
// TokenRecheckInterval tick; an error closes the connection with 4403.

var (
	errWSTokenInvalid     = errors.New("token revoked")
	errWSTokenExpired     = errors.New("token expired")
	errWSTokenRoleChanged = errors.New("token role changed")
	errWSTokenUnchecked   = errors.New("token check failed")
)

// wsTokenCheck reports whether client's token still grants the connection
// it was given: active, unexpired, same role. It fails closed: a missing
// token, a lookup error or no token storage all disconnect (the client can
// reconnect, which re-authenticates).
func (g *Gateway) wsTokenCheck(client *Client) error {
	if g.auth == nil || g.auth.AuthStorage == nil || client.TokenID == "" {
		return errWSTokenUnchecked
	}
	info, err := g.auth.AuthStorage.GetTokenInfo(client.TokenID)
	if err != nil {
		g.logger.Warn("websocket token re-check failed", "client_id", client.ID, "token_id", client.TokenID, "error", err)
		return errWSTokenUnchecked
	}
	if !info.IsActive {
		return errWSTokenInvalid
	}
	if info.ExpiresAt != nil && time.Now().After(*info.ExpiresAt) {
		return errWSTokenExpired
	}
	role, _ := auth.TokenRole(info.Metadata)
	if (role == auth.RoleOwner) != client.Owner {
		return errWSTokenRoleChanged
	}
	return nil
}
