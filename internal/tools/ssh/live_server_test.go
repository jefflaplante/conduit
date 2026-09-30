//go:build with_ssh

package ssh

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// liveSSHServer is a hermetic in-process SSH server on 127.0.0.1 used to
// exercise the real pool-backed client end to end (conduit-enf0). It never
// runs a shell: "exec" requests are recorded and answered with
// "ran: <command>", and "scp -t"/"scp -f" speak just enough of the SCP
// protocol for one-file transfers against an in-memory file map. The
// command "hang" runs until the client signals or closes the session, and
// "direct-tcpip" channels are forwarded so the server can act as its own
// jump host (conduit-uanm).
type liveSSHServer struct {
	addr    string
	port    int
	hostKey ssh.Signer

	// knownHosts holds exactly this server's host key; keyPath is a client
	// private key the server accepts.
	knownHosts string
	keyPath    string

	mu       sync.Mutex
	commands []string
	signals  []string
	files    map[string][]byte // scp uploads by target path; scp -f source
	conns    int               // authenticated SSH connections still open
}

func newLiveSSHServer(t *testing.T) *liveSSHServer {
	t.Helper()
	dir := t.TempDir()

	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostKey, err := ssh.NewSignerFromKey(hostPriv)
	require.NoError(t, err)

	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	require.NoError(t, err)
	keyPath := filepath.Join(dir, "id_ed25519")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600))
	authorized, err := ssh.NewPublicKey(clientPub)
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port

	s := &liveSSHServer{
		addr:    ln.Addr().String(),
		port:    port,
		hostKey: hostKey,
		keyPath: keyPath,
		files:   map[string][]byte{},
	}
	s.knownHosts = s.writeKnownHosts(t, hostKey.PublicKey())

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(authorized.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown client key")
		},
	}
	cfg.AddHostKey(hostKey)

	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.serveConn(nc, cfg)
			}()
		}
	}()
	return s
}

// writeKnownHosts writes a known_hosts file binding this server's address
// to key and returns its path.
func (s *liveSSHServer) writeKnownHosts(t *testing.T, key ssh.PublicKey) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{s.addr}, key)
	require.NoError(t, os.WriteFile(path, []byte(line+"\n"), 0o600))
	return path
}

func (s *liveSSHServer) serveConn(nc net.Conn, cfg *ssh.ServerConfig) {
	defer nc.Close()
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	s.mu.Lock()
	s.conns++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.conns--
		s.mu.Unlock()
	}()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() == "direct-tcpip" {
			go s.forward(nch)
			continue
		}
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go s.serveSession(ch, chReqs)
	}
}

func (s *liveSSHServer) serveSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			continue
		}
		_ = req.Reply(true, nil)
		s.record(payload.Command)

		status := 0
		switch {
		case strings.HasPrefix(payload.Command, "scp -t "):
			status = s.scpSink(ch, unquote(strings.TrimPrefix(payload.Command, "scp -t ")))
		case strings.HasPrefix(payload.Command, "scp -f "):
			status = s.scpSource(ch, unquote(strings.TrimPrefix(payload.Command, "scp -f ")))
		case payload.Command == "hang":
			for req := range reqs {
				if req.Type == "signal" {
					var sig struct{ Signal string }
					_ = ssh.Unmarshal(req.Payload, &sig)
					s.mu.Lock()
					s.signals = append(s.signals, sig.Signal)
					s.mu.Unlock()
					return
				}
				_ = req.Reply(false, nil)
			}
			return
		default:
			_, _ = fmt.Fprintf(ch, "ran: %s\n", payload.Command)
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
		return
	}
}

// unquote reverses scpShellQuote for the paths the tests use.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], `'\''`, "'")
	}
	return s
}

func (s *liveSSHServer) scpSink(ch ssh.Channel, target string) int {
	r := bufio.NewReader(ch)
	_, _ = ch.Write([]byte{0})
	header, err := r.ReadString('\n')
	if err != nil {
		return 1
	}
	fields := strings.SplitN(strings.TrimSpace(header), " ", 3)
	if len(fields) != 3 {
		return 1
	}
	size, err := strconv.Atoi(fields[1])
	if err != nil {
		return 1
	}
	_, _ = ch.Write([]byte{0})
	data := make([]byte, size+1) // content + trailing NUL
	if _, err := io.ReadFull(r, data); err != nil {
		return 1
	}
	s.mu.Lock()
	s.files[target] = data[:size]
	s.mu.Unlock()
	_, _ = ch.Write([]byte{0})
	return 0
}

func (s *liveSSHServer) scpSource(ch ssh.Channel, source string) int {
	s.mu.Lock()
	data, ok := s.files[source]
	s.mu.Unlock()
	ack := make([]byte, 1)
	if _, err := io.ReadFull(ch, ack); err != nil || !ok {
		_, _ = ch.Write([]byte("\x02no such file\n"))
		return 1
	}
	_, _ = fmt.Fprintf(ch, "C0644 %d %s\n", len(data), filepath.Base(source))
	if _, err := io.ReadFull(ch, ack); err != nil {
		return 1
	}
	_, _ = ch.Write(append(append([]byte{}, data...), 0))
	_, _ = io.ReadFull(ch, ack)
	return 0
}

func (s *liveSSHServer) record(cmd string) {
	s.mu.Lock()
	s.commands = append(s.commands, cmd)
	s.mu.Unlock()
}

// ran returns the commands the server has received so far.
func (s *liveSSHServer) ran() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *liveSSHServer) file(path string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.files[path]
	return b, ok
}

func (s *liveSSHServer) putFile(path string, data []byte) {
	s.mu.Lock()
	s.files[path] = data
	s.mu.Unlock()
}

// openConns reports authenticated SSH connections that are still open.
func (s *liveSSHServer) openConns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

// gotSignals returns the signal names sent to "hang" commands.
func (s *liveSSHServer) gotSignals() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.signals...)
}

// forward serves a direct-tcpip channel (ssh -J) by dialling the requested
// address and copying both ways until either side closes.
func (s *liveSSHServer) forward(nch ssh.NewChannel) {
	var req struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(nch.ExtraData(), &req); err != nil {
		_ = nch.Reject(ssh.ConnectionFailed, "bad direct-tcpip request")
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port))))
	if err != nil {
		_ = nch.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nch.Accept()
	if err != nil {
		_ = target.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(ch, target); done <- struct{}{} }()
	go func() { _, _ = io.Copy(target, ch); done <- struct{}{} }()
	<-done
	_ = ch.Close()
	_ = target.Close()
}
