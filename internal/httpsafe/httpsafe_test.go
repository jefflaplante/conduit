package httpsafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadLimited(t *testing.T) {
	data, err := ReadLimited(strings.NewReader("hello"), 10)
	if err != nil || string(data) != "hello" {
		t.Fatalf("under limit: %q %v", data, err)
	}
	data, err = ReadLimited(strings.NewReader("hello"), 5)
	if err != nil || string(data) != "hello" {
		t.Fatalf("exact limit: %q %v", data, err)
	}
	data, err = ReadLimited(bytes.NewReader(make([]byte, 1<<20)), 1024)
	if !errors.Is(err, ErrBodyTooLarge) || len(data) != 1024 {
		t.Fatalf("oversized: len=%d err=%v", len(data), err)
	}
}

func TestLimitReaderWithDecoder(t *testing.T) {
	var v map[string]string
	ok := `{"a":"b"}`
	if err := json.NewDecoder(LimitReader(strings.NewReader(ok), int64(len(ok)))).Decode(&v); err != nil {
		t.Fatalf("exact-size decode failed: %v", err)
	}
	big := `{"a":"` + strings.Repeat("x", 4096) + `"}`
	err := json.NewDecoder(LimitReader(strings.NewReader(big), 100)).Decode(&v)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("expected ErrBodyTooLarge, got %v", err)
	}
}

func TestPolicyCheck(t *testing.T) {
	def := Policy{}
	cases := []struct {
		ip    string
		port  int
		p     Policy
		allow bool
	}{
		{"8.8.8.8", 443, def, true},
		{"192.168.1.10", 80, def, true}, // RFC1918 allowed by default
		{"10.0.0.5", 80, def, true},
		{"fd00::1", 80, def, true},
		{"127.0.0.1", 18789, def, false},
		{"127.1.2.3", 80, def, false},
		{"::1", 80, def, false},
		{"::ffff:127.0.0.1", 80, def, false},
		{"169.254.169.254", 80, def, false},
		{"169.254.1.1", 80, def, false},
		{"fe80::1", 80, def, false},
		{"0.0.0.0", 80, def, false},
		{"::", 80, def, false},
		{"224.0.0.1", 80, def, false},
		{"192.168.1.10", 80, Policy{BlockPrivate: true}, false},
		{"100.100.1.1", 80, Policy{BlockPrivate: true}, false},
		{"192.168.1.10", 80, Policy{BlockPrivate: true, AllowedHosts: []string{"192.168.1.10:80"}}, true},
		{"127.0.0.1", 8123, Policy{AllowedHosts: []string{"localhost:8123"}}, true},
		{"127.0.0.1", 18789, Policy{AllowedHosts: []string{"localhost:8123"}}, false},
		{"127.0.0.1", 9, Policy{AllowedHosts: []string{"127.0.0.1:*"}}, true},
		// allowlist can never re-open link-local / metadata
		{"169.254.169.254", 80, Policy{AllowedHosts: []string{"169.254.169.254:80"}}, false},
	}
	for _, c := range cases {
		err := c.p.Check(netip.MustParseAddr(c.ip), c.port)
		if (err == nil) != c.allow {
			t.Errorf("Check(%s:%d, %+v) err=%v, want allow=%v", c.ip, c.port, c.p, err, c.allow)
		}
	}
}

func get(t *testing.T, c *http.Client, url string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := c.Do(req)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func TestClientBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	err := get(t, NewClient(Policy{}, 5*time.Second, nil), srv.URL)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("expected loopback to be blocked, got %v", err)
	}
	// Allowlisted loopback works.
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	c := NewClient(Policy{AllowedHosts: []string{"localhost:" + itoa(port)}}, 5*time.Second, nil)
	if err := get(t, c, srv.URL); err != nil {
		t.Fatalf("allowlisted loopback should work: %v", err)
	}
}

func TestClientBlocksMetadata(t *testing.T) {
	err := get(t, NewClient(Policy{}, 2*time.Second, nil), "http://169.254.169.254/latest/meta-data/")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("expected metadata IP to be blocked, got %v", err)
	}
}

// fakeResolver answers every lookup with the configured addresses, and
// counts calls; used to model DNS rebinding.
type fakeResolver struct {
	answers [][]netip.Addr
	calls   atomic.Int32
}

func (f *fakeResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	n := int(f.calls.Add(1)) - 1
	if n >= len(f.answers) {
		n = len(f.answers) - 1
	}
	return f.answers[n], nil
}

func TestClientBlocksDNSRebinding(t *testing.T) {
	// The hostname "looks" public on a first (pre-flight) lookup but resolves
	// to loopback when the connection is actually made. The dial-time check
	// must catch it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	r := &fakeResolver{answers: [][]netip.Addr{
		{netip.MustParseAddr("93.184.216.34")},
		{netip.MustParseAddr("127.0.0.1")},
	}}
	// Simulate a pre-flight check that saw the public address.
	pre, _ := r.LookupNetIP(context.Background(), "ip", "rebind.test")
	if err := (Policy{}).Check(pre[0], port); err != nil {
		t.Fatalf("pre-flight should pass: %v", err)
	}
	err := get(t, NewClient(Policy{}, 5*time.Second, r), "http://rebind.test:"+itoa(port)+"/")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("expected rebinding to loopback to be blocked, got %v", err)
	}
}

func TestClientBlocksRedirectToLoopback(t *testing.T) {
	// httptest only listens on loopback, so allowlist the front server's
	// port and redirect to a different, non-allowlisted loopback port: the
	// redirect hop must be re-checked at dial time.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("internal secret"))
	}))
	defer target.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer front.Close()
	frontPort := front.Listener.Addr().(*net.TCPAddr).Port
	c := NewClient(Policy{AllowedHosts: []string{"127.0.0.1:" + itoa(frontPort)}}, 5*time.Second, nil)
	err := get(t, c, front.URL)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("expected redirect to loopback to be blocked, got %v", err)
	}
}

func TestClientAllowsPrivateByDefault(t *testing.T) {
	// Resolve a name to an RFC1918 address that nothing listens on: the
	// policy must let the dial proceed (it then fails with a network error,
	// not ErrBlockedAddress).
	r := &fakeResolver{answers: [][]netip.Addr{{netip.MustParseAddr("10.255.255.1")}}}
	c := NewClient(Policy{}, 300*time.Millisecond, r)
	err := get(t, c, "http://lan.test:81/")
	if errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("RFC1918 should be allowed by default, got %v", err)
	}
	c = NewClient(Policy{BlockPrivate: true}, 300*time.Millisecond, r)
	if err := get(t, c, "http://lan.test:81/"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("RFC1918 should be blocked with BlockPrivate, got %v", err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
