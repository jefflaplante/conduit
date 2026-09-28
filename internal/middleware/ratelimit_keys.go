package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// rateLimitKey normalises a client IP into a limiter key. conduit-31jg.4:
// IPv6 clients are keyed by their /64 prefix (a single subscriber usually
// controls a whole /64, so per-address keying is trivially evaded).
// IPv4-mapped IPv6 addresses are reduced to their IPv4 form. Non-IP input
// is returned unchanged.
func rateLimitKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String()
	}
	masked := parsed.Mask(net.CIDRMask(64, 128))
	return masked.String() + "/64"
}

// extractClientIP extracts the client IP from the request.
//
// When trustProxy is false (default), only RemoteAddr is used. This prevents
// attackers from spoofing their IP via X-Forwarded-For headers.
//
// When trustProxy is true, the function uses the rightmost non-private IP from
// X-Forwarded-For, which is the IP added by the trusted proxy. This is more
// secure than using the leftmost IP (which can be spoofed by the client).
//
// Security: Only set trustProxy=true when running behind a trusted reverse proxy
// that sets X-Forwarded-For correctly.
func extractClientIP(r *http.Request, trustProxy bool) string {
	// Always extract RemoteAddr as the fallback
	remoteIP := extractRemoteAddr(r.RemoteAddr)

	// If not trusting proxy headers, use RemoteAddr directly
	if !trustProxy {
		return remoteIP
	}

	// Check X-Forwarded-For header (can contain multiple IPs)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Parse IPs from right to left, looking for the rightmost non-private IP.
		// The rightmost IP is the one added by our trusted proxy, which is the
		// actual client IP (or the last external hop if behind multiple proxies).
		ips := strings.Split(xff, ",")
		for i := len(ips) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(ips[i])
			if isValidIP(ip) && !isPrivateIP(ip) {
				return ip
			}
		}
		// If all IPs are private (internal network), use the leftmost
		// as it's likely the actual internal client
		if len(ips) > 0 {
			ip := strings.TrimSpace(ips[0])
			if isValidIP(ip) {
				return ip
			}
		}
	}

	// Check X-Real-IP header (single IP set by proxy)
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		ip := strings.TrimSpace(xri)
		if isValidIP(ip) {
			return ip
		}
	}

	// Fallback to RemoteAddr
	return remoteIP
}

// extractRemoteAddr extracts the IP from RemoteAddr (host:port format)
func extractRemoteAddr(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// If SplitHostPort fails, return the raw RemoteAddr
		return remoteAddr
	}
	return host
}

// isValidIP checks if a string is a valid IP address
func isValidIP(ip string) bool {
	return net.ParseIP(ip) != nil
}

// isPrivateIP checks if an IP address is in a private/reserved range.
// This includes RFC1918 private networks, loopback, link-local, and other reserved ranges.
func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	// Check for IPv4 private ranges
	if ip4 := ip.To4(); ip4 != nil {
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return true
		}
		// 172.16.0.0/12
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		// 192.168.0.0/16
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
		// 127.0.0.0/8 (loopback)
		if ip4[0] == 127 {
			return true
		}
		// 169.254.0.0/16 (link-local)
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		// 100.64.0.0/10 (Carrier-grade NAT)
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
		return false
	}

	// Check for IPv6 private ranges
	// ::1 (loopback)
	if ip.Equal(net.IPv6loopback) {
		return true
	}
	// fc00::/7 (Unique Local Addresses)
	if ip[0] == 0xfc || ip[0] == 0xfd {
		return true
	}
	// fe80::/10 (Link-Local)
	if ip[0] == 0xfe && (ip[1]&0xc0) == 0x80 {
		return true
	}

	return false
}

// sanitizeIdentifier sanitizes identifiers for logging (privacy protection)
func sanitizeIdentifier(identifier string, isAnonymous bool) string {
	if !isAnonymous {
		// For authenticated clients, show the client name as-is
		return identifier
	}

	// conduit-31jg.4: IPv6 identifiers are now "<prefix>/64"
	identifier = strings.TrimSuffix(identifier, "/64")

	// For IP addresses, show only partial information for privacy
	if ip := net.ParseIP(identifier); ip != nil {
		if ip.To4() != nil {
			// IPv4: show only first two octets
			parts := strings.Split(identifier, ".")
			if len(parts) >= 2 {
				return fmt.Sprintf("%s.%s.*.* ", parts[0], parts[1])
			}
		} else if ip.To16() != nil {
			// IPv6: show only first part
			parts := strings.Split(identifier, ":")
			if len(parts) >= 1 {
				return fmt.Sprintf("%s::*", parts[0])
			}
		}
	}

	// Fallback: just indicate it's an IP
	return "IP_ADDR"
}

// getIdentifierType returns a string describing the identifier type
func getIdentifierType(isAnonymous bool) string {
	if isAnonymous {
		return "anonymous_ip"
	}
	return "authenticated_client"
}
