package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the local TUI client
func Run(config *TUIConfig) error {
	// Create WebSocket client
	client := NewWSClient(config.GatewayURL, config.Token, config.UserID)

	// Create the model
	model := NewModel(ModelConfig{
		Client:        client,
		UserID:        config.UserID,
		GatewayURL:    config.GatewayURL,
		AssistantName: config.AssistantName,
	})

	// Set gateway URL in status bar
	model.statusBar.GatewayURL = config.GatewayURL
	model.sidebar.GatewayURL = config.GatewayURL
	model.sidebar.UserID = config.UserID

	// Create BubbleTea program
	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	// Run the program
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	// Ensure cleanup
	if m, ok := finalModel.(Model); ok {
		if m.client != nil {
			m.client.Close()
		}
	}

	return nil
}
