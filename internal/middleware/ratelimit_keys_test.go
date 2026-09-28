package middleware

import (
	"net/http/httptest"
	"testing"
)

func TestExtractClientIP_NoTrustProxy(t *testing.T) {
	// When TrustProxy is false, only RemoteAddr should be used
	tests := []struct {
		name          string
		remoteAddr    string
		xForwardedFor string
		xRealIP       string
		expectedIP    string
	}{
		{
			name:       "Remote address only",
			remoteAddr: "192.168.1.100:12345",
			expectedIP: "192.168.1.100",
		},
		{
			name:          "X-Forwarded-For ignored when TrustProxy=false",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.1",
			expectedIP:    "10.0.0.1", // Uses RemoteAddr, not XFF
		},
		{
			name:       "X-Real-IP ignored when TrustProxy=false",
			remoteAddr: "10.0.0.1:12345",
			xRealIP:    "203.0.113.2",
			expectedIP: "10.0.0.1", // Uses RemoteAddr, not X-Real-IP
		},
		{
			name:       "IPv6",
			remoteAddr: "[2001:db8::1]:12345",
			expectedIP: "2001:db8::1",
		},
		{
			name:       "Malformed address",
			remoteAddr: "not-an-ip",
			expectedIP: "not-an-ip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}
			if tt.xRealIP != "" {
				req.Header.Set("X-Real-IP", tt.xRealIP)
			}

			ip := extractClientIP(req, false) // TrustProxy=false
			if ip != tt.expectedIP {
				t.Errorf("Expected IP: %s, got: %s", tt.expectedIP, ip)
			}
		})
	}
}

func TestExtractClientIP_TrustProxy(t *testing.T) {
	// When TrustProxy is true, use rightmost non-private IP from XFF
	tests := []struct {
		name          string
		remoteAddr    string
		xForwardedFor string
		xRealIP       string
		expectedIP    string
	}{
		{
			name:       "Remote address only (no headers)",
			remoteAddr: "192.168.1.100:12345",
			expectedIP: "192.168.1.100",
		},
		{
			name:          "X-Forwarded-For single public IP",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.1",
			expectedIP:    "203.0.113.1",
		},
		{
			name:          "X-Forwarded-For multiple IPs - uses rightmost non-private",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "1.2.3.4, 203.0.113.1, 10.0.0.2",
			expectedIP:    "203.0.113.1", // Rightmost non-private
		},
		{
			name:          "X-Forwarded-For all private IPs - uses leftmost",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "192.168.1.1, 10.0.0.2, 172.16.0.1",
			expectedIP:    "192.168.1.1", // Leftmost when all are private
		},
		{
			name:          "X-Forwarded-For spoofed + real - uses rightmost non-private",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "spoofed.ip.here, 1.2.3.4, 203.0.113.99",
			expectedIP:    "203.0.113.99", // Rightmost valid non-private
		},
		{
			name:       "X-Real-IP when no XFF",
			remoteAddr: "10.0.0.1:12345",
			xRealIP:    "203.0.113.2",
			expectedIP: "203.0.113.2",
		},
		{
			name:          "X-Forwarded-For takes precedence over X-Real-IP",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.1",
			xRealIP:       "203.0.113.2",
			expectedIP:    "203.0.113.1",
		},
		{
			name:       "IPv6 remote address",
			remoteAddr: "[2001:db8::1]:12345",
			expectedIP: "2001:db8::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}
			if tt.xRealIP != "" {
				req.Header.Set("X-Real-IP", tt.xRealIP)
			}

			ip := extractClientIP(req, true) // TrustProxy=true
			if ip != tt.expectedIP {
				t.Errorf("Expected IP: %s, got: %s", tt.expectedIP, ip)
			}
		})
	}
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip        string
		isPrivate bool
	}{
		// IPv4 private ranges
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.15.0.1", false}, // Just below 172.16
		{"172.32.0.1", false}, // Just above 172.31
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		{"169.254.0.1", true}, // Link-local
		{"100.64.0.1", true},  // Carrier-grade NAT
		{"100.127.255.255", true},
		{"100.63.255.255", false}, // Just below CGNAT
		{"100.128.0.0", false},    // Just above CGNAT

		// Public IPv4
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"203.0.113.1", false},

		// IPv6
		{"::1", true},           // Loopback
		{"fc00::1", true},       // Unique Local
		{"fd00::1", true},       // Unique Local
		{"fe80::1", true},       // Link-local
		{"2001:db8::1", false},  // Documentation (but public range)
		{"2607:f8b0::1", false}, // Public

		// Invalid
		{"not-an-ip", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			result := isPrivateIP(tt.ip)
			if result != tt.isPrivate {
				t.Errorf("isPrivateIP(%s) = %v, expected %v", tt.ip, result, tt.isPrivate)
			}
		})
	}
}

func TestSanitizeIdentifier(t *testing.T) {
	tests := []struct {
		identifier  string
		isAnonymous bool
		expected    string
	}{
		{"client_name", false, "client_name"},
		{"192.168.1.100", true, "192.168.*.* "},
		{"2001:db8::1", true, "2001::*"},
		{"not_an_ip", true, "IP_ADDR"},
	}

	for _, tt := range tests {
		result := sanitizeIdentifier(tt.identifier, tt.isAnonymous)
		if result != tt.expected {
			t.Errorf("sanitizeIdentifier(%s, %v) = %s, expected %s",
				tt.identifier, tt.isAnonymous, result, tt.expected)
		}
	}
}
