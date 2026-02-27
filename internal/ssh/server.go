package ssh

import (
	"fmt"
	"log"
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

	// Load authorized keys for public key auth
	authorizedKeys, err := LoadAuthorizedKeys(config.AuthorizedKeysPath)
	if err != nil {
		log.Printf("[SSH] No authorized keys loaded: %v", err)
		authorizedKeys = nil
	} else {
		log.Printf("[SSH] Loaded %d authorized keys", len(authorizedKeys))
	}

	handler := func(sess charmssh.Session) (tea.Model, []tea.ProgramOption) {
		return sshBubbleTeaHandler(sess, config)
	}

	opts := []charmssh.Option{
		wish.WithAddress(config.ListenAddr),
		wish.WithHostKeyPath(config.HostKeyPath),
		wish.WithMiddleware(
			wishbubbletea.Middleware(handler),
			activeterm.Middleware(),
			logging.Middleware(),
		),
	}

	// Add public key auth if we have authorized keys
	if len(authorizedKeys) > 0 {
		opts = append(opts, wish.WithPublicKeyAuth(func(ctx charmssh.Context, key charmssh.PublicKey) bool {
			return publicKeyHandler(ctx, key, authorizedKeys)
		}))
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
