package auth

import (
	"fmt"
	"strings"
)

// Token roles (conduit-31jg.67, conduit-31jg.55). A token's role lives in
// its metadata under MetadataKeyRole; no schema change is needed.
//
//   - owner: the human operator. May open any session, act as any user ID
//     and approve owner actions.
//   - automation: scripts and services. Confined to sessions whose user is
//     the token's client name, cannot claim another user ID, cannot approve
//     anything, and its turns are non-interactive (owner-account actions
//     fail closed instead of prompting).
const (
	MetadataKeyRole = "role"
	RoleOwner       = "owner"
	RoleAutomation  = "automation"
)

// DefaultRole is the role of a token created before roles existed.
// Owner decision 2026-10-01: untagged tokens keep their previous (owner)
// powers so a deploy breaks nothing; the gateway logs a warning until the
// token is tagged with `conduit token set-role`.
const DefaultRole = RoleOwner

// ParseRole validates a role name (case-insensitive).
func ParseRole(s string) (string, error) {
	switch r := strings.ToLower(strings.TrimSpace(s)); r {
	case RoleOwner, RoleAutomation:
		return r, nil
	default:
		return "", fmt.Errorf("invalid role %q (want %q or %q)", s, RoleOwner, RoleAutomation)
	}
}

// TokenRole returns the role recorded in a token's metadata. explicit is
// false when the token has no valid role and DefaultRole applies.
func TokenRole(metadata map[string]string) (role string, explicit bool) {
	if r, err := ParseRole(metadata[MetadataKeyRole]); err == nil {
		return r, true
	}
	return DefaultRole, false
}

// SetTokenRole records role in the token's metadata, keeping other keys.
func (ts *TokenStorage) SetTokenRole(tokenID, role string) error {
	r, err := ParseRole(role)
	if err != nil {
		return err
	}
	info, err := ts.GetTokenInfo(tokenID)
	if err != nil {
		return err
	}
	md := make(map[string]string, len(info.Metadata)+1)
	for k, v := range info.Metadata {
		md[k] = v
	}
	md[MetadataKeyRole] = r
	return ts.UpdateTokenMetadata(tokenID, md)
}
