package httpsafe

import (
	"errors"
	"net/netip"
	"testing"
)

// conduit-31jg.70: IPv6 transition addresses embedding an IPv4 address must
// be judged by the embedded IPv4 too.
func TestPolicyCheck_EmbeddedIPv4(t *testing.T) {
	def := Policy{}
	blockPriv := Policy{BlockPrivate: true}
	cases := []struct {
		name  string
		ip    string
		p     Policy
		allow bool
	}{
		// NAT64 well-known prefix 64:ff9b::/96
		{"nat64 loopback", "64:ff9b::127.0.0.1", def, false},
		{"nat64 loopback hex", "64:ff9b::7f00:1", def, false},
		{"nat64 metadata", "64:ff9b::169.254.169.254", def, false},
		{"nat64 unspecified", "64:ff9b::0.0.0.0", def, false},
		{"nat64 public", "64:ff9b::8.8.8.8", def, true},
		{"nat64 private default", "64:ff9b::192.168.1.10", def, true},
		{"nat64 private blocked", "64:ff9b::192.168.1.10", blockPriv, false},
		{"nat64 loopback allowlisted", "64:ff9b::127.0.0.1", Policy{AllowedHosts: []string{"localhost:80"}}, true},
		// NAT64 local-use 64:ff9b:1::/48 (RFC 8215), /96 and /48 embeddings
		{"nat64 local /96 loopback", "64:ff9b:1::127.0.0.1", def, false},
		{"nat64 local /48 metadata", "64:ff9b:1:a9fe:a9:fe00::", def, false},
		{"nat64 local /96 public", "64:ff9b:1::8.8.8.8", def, true},
		{"nat64 local /48 public", "64:ff9b:1:808:8:800::", def, true},
		{"nat64 local /64 loopback", "64:ff9b:1:0:7f:0:100:0", def, false},
		// 6to4 2002::/16
		{"6to4 loopback", "2002:7f00:1::1", def, false},
		{"6to4 metadata", "2002:a9fe:a9fe::", def, false},
		{"6to4 public", "2002:808:808::1", def, true},
		{"6to4 private blocked", "2002:c0a8:10a::1", blockPriv, false},
		// Teredo 2001:0::/32: client IPv4 is the last 32 bits XOR 0xffffffff
		{"teredo loopback client", "2001:0:4136:e378:8000:63bf:80ff:fffe", def, false}, // ^ = 127.0.0.1
		{"teredo metadata client", "2001:0:4136:e378:8000:63bf:5601:5601", def, false}, // ^ = 169.254.169.254
		{"teredo public client", "2001:0:4136:e378:8000:63bf:f7f7:f7f7", def, true},    // ^ = 8.8.8.8
		// Plain global IPv6 unaffected
		{"global v6", "2606:4700::1111", def, true},
		// IPv4-compatible (deprecated ::a.b.c.d)
		{"v4-compatible loopback", "::127.0.0.1", def, false},
	}
	for _, c := range cases {
		err := c.p.Check(netip.MustParseAddr(c.ip), 80)
		if (err == nil) != c.allow {
			t.Errorf("%s: Check(%s) err=%v, want allow=%v", c.name, c.ip, err, c.allow)
		}
		if err != nil && !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: error %v does not wrap ErrBlockedAddress", c.name, err)
		}
	}
}
