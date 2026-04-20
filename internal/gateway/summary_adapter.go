package gateway

import (
	"context"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/workspace"
)

// summaryAIRouterAdapter adapts ai.Router to workspace.SummaryAIRouter so the
// workspace-summary subsystem can call the configured (small-context) model
// without taking a direct dependency on the ai package's Router type.
type summaryAIRouterAdapter struct {
	router *ai.Router
}

// newSummaryAIRouterAdapter creates a new adapter.
func newSummaryAIRouterAdapter(router *ai.Router) *summaryAIRouterAdapter {
	return &summaryAIRouterAdapter{router: router}
}

// GenerateSimpleResponse generates a simple AI response without tools. A
// minimal temp session is created so the router can route the call through
// its normal message-building path; the session is never persisted.
func (a *summaryAIRouterAdapter) GenerateSimpleResponse(ctx context.Context, prompt, model string) (workspace.SummaryAIResponse, error) {
	tempSession := &sessions.Session{
		Key:     "summary_temp",
		Context: map[string]string{"model": model},
	}

	response, err := a.router.GenerateResponse(ctx, tempSession, prompt, "")
	if err != nil {
		return nil, err
	}

	return &summaryAIResponseAdapter{content: response.Content}, nil
}

// summaryAIResponseAdapter adapts ai.GenerateResponse to
// workspace.SummaryAIResponse.
type summaryAIResponseAdapter struct {
	content string
}

// GetContent returns the response content.
func (a *summaryAIResponseAdapter) GetContent() string {
	return a.content
}

// convertSummaryFileConfigs converts config types to workspace types for the
// workspace summary subsystem.
func convertSummaryFileConfigs(cfgConfigs map[string]config.SummaryFileConfig) map[string]workspace.SummaryFileConfig {
	if len(cfgConfigs) == 0 {
		return nil
	}
	result := make(map[string]workspace.SummaryFileConfig, len(cfgConfigs))
	for filename, cfg := range cfgConfigs {
		result[filename] = workspace.SummaryFileConfig{
			Ratio:        cfg.Ratio,
			PreserveKeys: cfg.PreserveKeys,
		}
	}
	return result
}
