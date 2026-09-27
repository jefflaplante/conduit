package httpsafe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrBlockedAddress is returned when an outbound connection targets an
// address the SSRF policy forbids.
var ErrBlockedAddress = errors.New("outbound connection blocked by SSRF policy")

// Policy decides which resolved IPs an outbound fetch may connect to.
//
// conduit-31jg.7 default (homelab) policy:
//   - always blocked: link-local (169.254.0.0/16 incl. cloud metadata
//     169.254.169.254, fe80::/10), unspecified (0.0.0.0/8, ::), multicast,
//     broadcast — the allowlist cannot re-open these;
//   - loopback (127.0.0.0/8, ::1) blocked unless allowlisted — it is how a
//     prompt-injected page would reach the gateway's own API on :18789;
//   - private (RFC1918, fc00::/7 ULA, 100.64.0.0/10 CGNAT) allowed unless
//     BlockPrivate is set, since the owner reaches LAN devices.
//
// Checks run on the resolved IP at connect time (every dial, including each
// redirect hop), which defeats DNS rebinding.
type Policy struct {
	// BlockPrivate additionally blocks RFC1918/ULA/CGNAT ranges.
	BlockPrivate bool
	// AllowedHosts re-opens specific loopback/private endpoints. Entries are
	// "host:port" where host is an IP literal or "localhost" (any loopback
	// address) and port is a number or "*".
	AllowedHosts []string
}

type allowEntry struct {
	localhost bool
	addr      netip.Addr
	port      int // 0 = any
}

func parseAllow(entries []string) []allowEntry {
	var out []allowEntry
	for _, e := range entries {
		host, portStr, err := net.SplitHostPort(strings.TrimSpace(e))
		if err != nil {
			continue
		}
		var ae allowEntry
		if portStr != "*" {
			p, err := strconv.Atoi(portStr)
			if err != nil || p <= 0 || p > 65535 {
				continue
			}
			ae.port = p
		}
		if strings.EqualFold(host, "localhost") {
			ae.localhost = true
		} else if a, err := netip.ParseAddr(host); err == nil {
			ae.addr = a.Unmap()
		} else {
			continue
		}
		out = append(out, ae)
	}
	return out
}

var (
	cgnat       = netip.MustParsePrefix("100.64.0.0/10")
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	broadcast   = netip.MustParseAddr("255.255.255.255")
)

var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")   // RFC 6052
	nat64LocalUse  = netip.MustParsePrefix("64:ff9b:1::/48") // RFC 8215
	sixToFour      = netip.MustParsePrefix("2002::/16")      // RFC 3056
	teredo         = netip.MustParsePrefix("2001::/32")      // RFC 4380
	v4Compatible   = netip.MustParsePrefix("::/96")          // deprecated ::a.b.c.d
)

// embeddedIPv4 returns the IPv4 addresses an IPv6 transition address can
// deliver to (conduit-31jg.70). A NAT64 gateway, 6to4 relay or Teredo relay
// forwards such a destination to the embedded IPv4, so 64:ff9b::127.0.0.1
// would otherwise sail past the loopback check.
//
//   - 64:ff9b::/96: last 32 bits.
//   - 64:ff9b:1::/48 (local-use): the operator picks the prefix length, so
//     every plausible RFC 6052 layout (/48, /56, /64, /96) is decoded and
//     all candidates are checked (any blocked candidate blocks); see
//     nat64LocalUseCandidates.
//   - 2002::/16: bits 16-47.
//   - 2001::/32 Teredo: client = last 32 bits XOR 0xffffffff, and the server
//     at bits 32-63.
//   - ::/96 IPv4-compatible (deprecated): last 32 bits.
func embeddedIPv4(ip netip.Addr) []netip.Addr {
	if !ip.Is6() || ip.Is4In6() {
		return nil
	}
	b := ip.As16()
	v4 := func(a, b2, c, d byte) netip.Addr { return netip.AddrFrom4([4]byte{a, b2, c, d}) }
	switch {
	case nat64WellKnown.Contains(ip):
		return []netip.Addr{v4(b[12], b[13], b[14], b[15])}
	case nat64LocalUse.Contains(ip):
		return nat64LocalUseCandidates(b)
	case sixToFour.Contains(ip):
		return []netip.Addr{v4(b[2], b[3], b[4], b[5])}
	case teredo.Contains(ip):
		return []netip.Addr{
			v4(b[12]^0xff, b[13]^0xff, b[14]^0xff, b[15]^0xff), // client
			v4(b[4], b[5], b[6], b[7]),                         // server
		}
	case v4Compatible.Contains(ip):
		if ip.IsUnspecified() || ip.IsLoopback() {
			return nil // judged as IPv6 below
		}
		return []netip.Addr{v4(b[12], b[13], b[14], b[15])}
	}
	return nil
}

// nat64LocalUseCandidates decodes every RFC 6052 layout that could apply to
// an address in 64:ff9b:1::/48. A /48, /56 or /64 layout is only plausible
// when the reserved "u" octet (byte 8) and the suffix after the embedded
// IPv4 are zero; the /96 layout always is. When a shorter layout is
// plausible the /96 reading is necessarily 0.0.0.0 (the zero suffix) and is
// dropped so real /48-/64 translations of public hosts are not blocked.
func nat64LocalUseCandidates(b [16]byte) []netip.Addr {
	zero := func(bs []byte) bool {
		for _, c := range bs {
			if c != 0 {
				return false
			}
		}
		return true
	}
	var out []netip.Addr
	if b[8] == 0 {
		if zero(b[11:]) {
			out = append(out, netip.AddrFrom4([4]byte{b[6], b[7], b[9], b[10]})) // /48
		}
		if zero(b[12:]) {
			out = append(out, netip.AddrFrom4([4]byte{b[7], b[9], b[10], b[11]})) // /56
		}
		if zero(b[13:]) {
			out = append(out, netip.AddrFrom4([4]byte{b[9], b[10], b[11], b[12]})) // /64
		}
	}
	if slash96 := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}); len(out) == 0 || !slash96.IsUnspecified() {
		out = append(out, slash96)
	}
	return out
}

// Check returns nil if connecting to ip:port is permitted.
func (p Policy) Check(ip netip.Addr, port int) error {
	ip = ip.Unmap()
	// conduit-31jg.70: apply the same policy to any embedded IPv4.
	for _, inner := range embeddedIPv4(ip) {
		if err := p.Check(inner, port); err != nil {
			return fmt.Errorf("%w (embedded in %s)", err, ip)
		}
	}
	switch {
	case !ip.IsValid(),
		ip.IsUnspecified(),
		thisNetwork.Contains(ip),
		ip == broadcast,
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(),
		ip.IsMulticast():
		return fmt.Errorf("%w: %s is link-local/unspecified/multicast", ErrBlockedAddress, ip)
	}

	loopback := ip.IsLoopback()
	private := ip.IsPrivate() || cgnat.Contains(ip)
	if !loopback && !(private && p.BlockPrivate) {
		return nil
	}
	for _, ae := range parseAllow(p.AllowedHosts) {
		if ae.port != 0 && ae.port != port {
			continue
		}
		if (ae.localhost && loopback) || (ae.addr.IsValid() && ae.addr == ip) {
			return nil
		}
	}
	if loopback {
		return fmt.Errorf("%w: %s is loopback (add it to tools.web.allowed_hosts to permit)", ErrBlockedAddress, ip)
	}
	return fmt.Errorf("%w: %s is a private address and tools.web.block_private_networks is set", ErrBlockedAddress, ip)
}

// Resolver is the subset of *net.Resolver used by the guarded dialer. It is an
// interface so tests can simulate DNS rebinding.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Dialer is an SSRF-guarded dialer. It resolves the host itself, checks every
// candidate IP against the Policy, and dials only the checked IP literal; a
// socket-level Control hook re-checks the final address as a second layer.
type Dialer struct {
	Policy   Policy
	Resolver Resolver // nil = net.DefaultResolver
	Timeout  time.Duration
}

func (d *Dialer) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparseable address %q", ErrBlockedAddress, address)
	}
	return d.Policy.Check(ap.Addr(), int(ap.Port()))
}

// DialContext implements the http.Transport DialContext signature.
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q: %w", portStr, err)
	}

	var ips []netip.Addr
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		ips = []netip.Addr{ip}
	} else {
		r := d.Resolver
		if r == nil {
			r = net.DefaultResolver
		}
		ips, err = r.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}

	nd := &net.Dialer{Timeout: d.Timeout, Control: d.control}
	if nd.Timeout == 0 {
		nd.Timeout = 30 * time.Second
	}
	var firstErr error
	for _, ip := range ips {
		if err := d.Policy.Check(ip, port); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		conn, err := nd.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), portStr))
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("no addresses for %s", host)
	}
	return nil, firstErr
}

// maxRedirects caps redirect chains for guarded clients.
const maxRedirects = 10

// NewClient returns an http.Client whose every connection (including each
// redirect hop) goes through the SSRF-guarded Dialer. Proxies from the
// environment are deliberately NOT honored: a proxy would make the dial
// target the proxy, bypassing the per-IP check.
func NewClient(policy Policy, timeout time.Duration, resolver Resolver) *http.Client {
	d := &Dialer{Policy: policy, Resolver: resolver, Timeout: 10 * time.Second}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
}
