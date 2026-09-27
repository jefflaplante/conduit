package ai

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"conduit/internal/httpsafe"
)

// conduit-31jg.70: the success-path JSON decoders in openai.go and
// anthropic.go read resp.Body unbounded; a misbehaving or hostile upstream
// could stream an arbitrarily large body into memory.

func shrinkProviderBodyLimit(t *testing.T, n int64) {
	t.Helper()
	prev := providerResponseBodyLimit
	providerResponseBodyLimit = n
	t.Cleanup(func() { providerResponseBodyLimit = prev })
}

func oversizedJSON(content string) string {
	return `{"choices":[{"message":{"content":"` + content + `"},"finish_reason":"stop"}],` +
		`"content":[{"type":"text","text":"` + content + `"}],"stop_reason":"end_turn","model":"m"}`
}

func TestProviderSuccessBodyIsCapped(t *testing.T) {
	shrinkProviderBodyLimit(t, 4<<10)
	big := strings.Repeat("x", 64<<10)
	small := "fits"

	cases := map[string]func(url string) (*GenerateResponse, error){
		"openai": func(url string) (*GenerateResponse, error) {
			p := &OpenAIProvider{name: "z-ai", baseURL: url, client: &http.Client{Timeout: 5 * time.Second}}
			return p.GenerateResponse(context.Background(), &GenerateRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
		},
		"anthropic": func(url string) (*GenerateResponse, error) {
			return fastRetryAnthropic(t, url).GenerateResponse(context.Background(), simpleReq())
		},
	}
	for name, call := range cases {
		t.Run(name+"/oversized", func(t *testing.T) {
			srv, _ := scriptedServer(t, statusReply(200, map[string]string{"Content-Type": "application/json"}, oversizedJSON(big)))
			_, err := call(srv.URL)
			if err == nil || !errors.Is(err, httpsafe.ErrBodyTooLarge) {
				t.Fatalf("err = %v, want ErrBodyTooLarge", err)
			}
		})
		t.Run(name+"/within-limit", func(t *testing.T) {
			srv, _ := scriptedServer(t, statusReply(200, map[string]string{"Content-Type": "application/json"}, oversizedJSON(small)))
			resp, err := call(srv.URL)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(resp.Content, small) {
				t.Errorf("content = %q", resp.Content)
			}
		})
	}
}

func TestProviderResponseBodyLimitDefault(t *testing.T) {
	if providerResponseBodyLimit != httpsafe.APIBodyLimit {
		t.Errorf("providerResponseBodyLimit = %d, want httpsafe.APIBodyLimit (32 MiB)", providerResponseBodyLimit)
	}
}
