package ssh

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	charmssh "github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	wishbubbletea "github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"

	"conduit/internal/tui"
)

// SSHConfig holds configuration for the SSH server
type SSHConfig struct {
	ListenAddr         string
	HostKeyPath        string
	AuthorizedKeysPath string
	GatewayURL         string
	GatewayToken       string
	AssistantName      string
	// Location is the timezone for rendering timestamps in the TUI. If nil, times render as-is.
	Location *time.Location
	// ClientFactory, if set, creates an in-process GatewayClient for the given
	// SSH user instead of connecting back via WebSocket.
	ClientFactory func(sshUser string) tui.GatewayClient
	// ShellSecurity configures the shell escape (! prefix) feature for SSH sessions
	ShellSecurity tui.ShellSecurityConfig
}

// NewServer creates a Wish SSH server that serves the TUI
func NewServer(config SSHConfig) (*charmssh.Server, error) {
	if config.ListenAddr == "" {
		config.ListenAddr = ":2222"
	}
	if config.HostKeyPath == "" {
		dir, err := sshConfigDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get SSH config dir: %w", err)
		}
		config.HostKeyPath = dir + "/ssh_host_key"
	}

	// Load authorized keys for public key auth. Fail closed (conduit-31jg.1):
	// charmbracelet/ssh sets NoClientAuth=true when no auth handler is
	// registered, so starting without keys would admit anyone who can reach
	// the port. Refuse to start instead.
	if config.AuthorizedKeysPath == "" {
		config.AuthorizedKeysPath = defaultAuthorizedKeysPath()
	}
	keyStore := newAuthorizedKeyStore(config.AuthorizedKeysPath)
	authorizedKeys, err := keyStore.load()
	if err != nil {
		return nil, fmt.Errorf("no authorized SSH keys loaded from %s (%v); refusing to start SSH server without authentication; add keys with `conduit ssh-keys add`", config.AuthorizedKeysPath, err)
	}
	if len(authorizedKeys) == 0 {
		return nil, fmt.Errorf("no authorized SSH keys loaded from %s; refusing to start SSH server without authentication; add keys with `conduit ssh-keys add`", config.AuthorizedKeysPath)
	}
	log.Printf("[SSH] Loaded %d authorized keys from %s", len(authorizedKeys), config.AuthorizedKeysPath)

	handler := func(sess charmssh.Session) (tea.Model, []tea.ProgramOption) {
		return sshBubbleTeaHandler(sess, config)
	}

	// Public key auth is always registered (defense in depth, conduit-31jg.1)
	// so an empty key list denies rather than falling back to no-auth. Keys
	// are re-read when authorized_keys changes, so `ssh-keys add/remove`
	// take effect without a restart.
	opts := []charmssh.Option{
		wish.WithAddress(config.ListenAddr),
		wish.WithHostKeyPath(config.HostKeyPath),
		wish.WithPublicKeyAuth(func(ctx charmssh.Context, key charmssh.PublicKey) bool {
			return publicKeyHandler(ctx, key, keyStore.keys())
		}),
		wish.WithMiddleware(
			wishbubbletea.Middleware(handler),
			activeterm.Middleware(),
			logging.Middleware(),
		),
	}

	server, err := wish.NewServer(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH server: %w", err)
	}

	return server, nil
}

// sshBubbleTeaHandler creates a TUI model for each SSH session
func sshBubbleTeaHandler(sess charmssh.Session, config SSHConfig) (tea.Model, []tea.ProgramOption) {
	sshUser := sess.User()
	if sshUser == "" {
		sshUser = "ssh-user"
	}

	// Create client: prefer in-process DirectClient via factory, fall back to WebSocket
	var client tui.GatewayClient
	if config.ClientFactory != nil {
		client = config.ClientFactory(sshUser)
	} else {
		client = tui.NewWSClient(config.GatewayURL, config.GatewayToken, sshUser)
	}

	// Create renderer for this SSH session so styles emit correct ANSI
	// escape sequences for the connecting terminal.
	renderer := wishbubbletea.MakeRenderer(sess)

	// Create TUI model with the SSH-aware renderer
	model := tui.NewModel(tui.ModelConfig{
		Client:        client,
		UserID:        sshUser,
		GatewayURL:    config.GatewayURL,
		AssistantName: config.AssistantName,
		Location:      config.Location,
		Renderer:      renderer,
		ShellSecurity: config.ShellSecurity,
	})

	// Set SSH-specific status bar info
	model.SetSSHUser(sshUser)
	if config.ClientFactory != nil {
		model.SetGatewayURL("direct (in-process)")
	} else {
		model.SetGatewayURL(config.GatewayURL)
	}

	return model, []tea.ProgramOption{tea.WithAltScreen()}
}

// publicKeyHandler validates SSH public keys against the authorized keys list
func publicKeyHandler(ctx charmssh.Context, key charmssh.PublicKey, authorizedKeys []charmssh.PublicKey) bool {
	for _, authKey := range authorizedKeys {
		if charmssh.KeysEqual(key, authKey) {
			log.Printf("[SSH] Public key accepted for user: %s", ctx.User())
			return true
		}
	}
	log.Printf("[SSH] Public key rejected for user: %s", ctx.User())
	return false
}

// authorizedKeyStore caches the parsed authorized_keys file and reloads it
// when the file's mtime or size changes (conduit-31jg.1). A file that
// disappears or cannot be read yields an empty list, which denies all keys.
type authorizedKeyStore struct {
	path string

	mu      sync.Mutex
	loaded  bool
	modTime time.Time
	size    int64
	cached  []charmssh.PublicKey
}

func newAuthorizedKeyStore(path string) *authorizedKeyStore {
	return &authorizedKeyStore{path: path}
}

// load reads the file unconditionally and refreshes the cache.
func (s *authorizedKeyStore) load() ([]charmssh.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadLocked()
}

func (s *authorizedKeyStore) reloadLocked() ([]charmssh.PublicKey, error) {
	s.loaded, s.cached = false, nil
	if s.path == "" {
		return nil, fmt.Errorf("no authorized keys path available")
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return nil, fmt.Errorf("failed to open authorized keys: %w", err)
	}
	keys, err := LoadAuthorizedKeys(s.path)
	if err != nil {
		return nil, err
	}
	s.loaded, s.cached, s.modTime, s.size = true, keys, info.ModTime(), info.Size()
	return keys, nil
}

// keys returns the current authorized keys, re-reading the file if its mtime
// or size changed since the last load. Errors fail closed (no keys).
func (s *authorizedKeyStore) keys() []charmssh.PublicKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	if info, err := os.Stat(s.path); err == nil && s.loaded &&
		info.ModTime().Equal(s.modTime) && info.Size() == s.size {
		return s.cached
	}
	keys, err := s.reloadLocked()
	if err != nil {
		log.Printf("[SSH] Failed to reload authorized keys, denying all: %v", err)
		return nil
	}
	log.Printf("[SSH] Reloaded %d authorized keys from %s", len(keys), s.path)
	return keys
}
