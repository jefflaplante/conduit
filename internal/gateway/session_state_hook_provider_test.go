package gateway

import (
	"context"
	"sync"

	"conduit/internal/ai"
)

// hookSideCallProvider is a Provider whose GenerateResponse records every
// request body and returns a canned reply. For Path B tests only.
type hookSideCallProvider struct {
	mu     sync.Mutex
	resp   string
	onCall func(reqBody string)
}

func (p *hookSideCallProvider) Name() string { return "testprov" }

func (p *hookSideCallProvider) GenerateResponse(ctx context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	body := ""
	if req != nil {
		for _, m := range req.Messages {
			body += m.Role + ": " + m.Content + "\n"
		}
	}
	if p.onCall != nil {
		p.onCall(body)
	}
	p.mu.Lock()
	resp := p.resp
	p.mu.Unlock()
	return &ai.GenerateResponse{Content: resp}, nil
}
