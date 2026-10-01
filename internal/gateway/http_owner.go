package gateway

import (
	"net/http"

	"conduit/internal/auth"
	"conduit/internal/middleware"
)

// requireOwnerRole lets only owner-role tokens through (conduit-25lt.1). It
// runs after the auth middleware: endpoints that expose the owner's data
// (the full system prompt, the Brain graph, vector search over sessions and
// memory, diagnostics) must not be readable with an automation token.
//
// A request with no auth info reached here on a path the auth middleware is
// configured to skip (e.g. public diagnostics); that config decision stands.
func (g *Gateway) requireOwnerRole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := middleware.GetAuthInfo(r.Context())
		if info != nil {
			if role, _ := auth.TokenRole(info.Metadata); role != auth.RoleOwner {
				g.logger.Warn("owner-only endpoint denied", "path", r.URL.Path, "client", info.ClientName, "role", role)
				writeJSONError(w, http.StatusForbidden, "this endpoint requires an owner-role token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
