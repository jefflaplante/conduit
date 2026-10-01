package gateway

import (
	"context"
	"sync"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/protocol"
)

// originProvider records the approval origin's RequestText of each turn.
type originProvider struct {
	mu    sync.Mutex
	texts []string
}

func (p *originProvider) Name() string { return "origin" }
func (p *originProvider) GenerateResponse(ctx context.Context, _ *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	o, _ := approval.OriginFrom(ctx)
	p.mu.Lock()
	p.texts = append(p.texts, o.RequestText)
	p.mu.Unlock()
	return &ai.GenerateResponse{Content: "ok", FinishReason: "stop"}, nil
}

// conduit-25lt.2: an interactive turn carries the human's message into the
// approval origin, so a prompt raised during it can show "You asked".
func TestTurnOrigin_CarriesRequestText(t *testing.T) {
	gw, _, router := newTestGatewayWithRouter(t)
	withApprovals(t, gw)
	p := &originProvider{}
	router.RegisterProvider("testprov", p)

	gw.handleIncomingMessage(context.Background(), &protocol.IncomingMessage{
		ChannelID: "telegram", UserID: "42", Text: "please reply to Bob about Friday",
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.texts) != 1 || p.texts[0] != "please reply to Bob about Friday" {
		t.Fatalf("RequestText = %q", p.texts)
	}
}
