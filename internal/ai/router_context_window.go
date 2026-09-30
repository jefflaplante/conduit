package ai

import "strings"

// DefaultContextWindow is the fallback context window size in tokens.
const DefaultContextWindow = 200000

// ContextWindowSizes maps model ID prefixes to their context window sizes.
var ContextWindowSizes = map[string]int{
	// Anthropic
	"claude-opus-4-6":   200000,
	"claude-opus-4-5":   200000, // legacy
	"claude-sonnet-4":   200000,
	"claude-haiku-4-5":  200000,
	"claude-3-5-sonnet": 200000,
	"claude-3-5-haiku":  200000,
	"claude-3-opus":     200000,
	"claude-3-sonnet":   200000,
	"claude-3-haiku":    200000,
	// OpenAI
	"gpt-4o":        128000,
	"gpt-4-turbo":   128000,
	"gpt-4":         8192,
	"gpt-3.5-turbo": 16385,
	// Local / Ollama models
	"llama3":          8192,
	"llama3.1":        128000,
	"llama3.2":        128000,
	"llama3.3":        128000,
	"mistral":         32768,
	"mixtral":         32768,
	"codellama":       16384,
	"deepseek-coder":  16384,
	"deepseek-coder2": 16384,
	"qwen2.5":         32768,
	"qwen3.5":         131072,
	"phi-3":           128000,
	"gemma2":          8192,
	// Z.ai GLM (conduit-31jg.82). docs.z.ai/guides/llm/glm-5.3 and
	// docs.z.ai/guides/vlm/glm-5.3-flash publish "1M" context (input+output,
	// per docs.z.ai/guides/overview/concept-param) and 128K max output for
	// glm-5.3, glm-5.3-flash and glm-5.3-flashx. OpenRouter lists 1,048,576
	// for most hosts but 1,000,000 for some (and for z-ai/glm-5.3-prime), so
	// use the literal 1,000,000: it fits every host that advertises "1M".
	"glm-5.3": 1000000,
	// DeepSeek V4.1 Flash (conduit-31jg.82). api-docs.deepseek.com
	// quick_start/pricing publishes "1M" context and 384K max output;
	// OpenRouter's deepseek/deepseek-v4.1-flash hosts advertise between
	// 1,000,000 and 1,048,576 (DeepSeek's own endpoint: 1,048,576). Same
	// conservative 1,000,000 as GLM.
	"deepseek-v4.1-flash": 1000000,
}

// ContextWindowForModel returns the context window size for a given model.
// It tries an exact match first, then the LONGEST matching prefix, then the
// default. See LookupContextWindow.
func ContextWindowForModel(model string) int {
	size, _ := LookupContextWindow(model)
	return size
}

// LookupContextWindow resolves a model's context window and reports whether
// it matched a known entry (false = DefaultContextWindow was used).
//
// conduit-31jg.17: prefixes overlap (gpt-4/gpt-4o, llama3/llama3.1,
// deepseek-coder/deepseek-coder2), and the old first-match loop over the map
// was nondeterministic — "gpt-4o-2024-08-06" sometimes resolved to 8192 and
// trimRequestToFitContext then dropped nearly all history. The longest
// matching prefix is unique, so the result no longer depends on map order.
// A "provider/model" ID that matches nothing is retried without the prefix.
func LookupContextWindow(model string) (int, bool) {
	if model == "" {
		return DefaultContextWindow, false
	}
	if size, ok := longestPrefixContextWindow(model); ok {
		return size, true
	}
	if i := strings.LastIndex(model, "/"); i >= 0 && i < len(model)-1 {
		if size, ok := longestPrefixContextWindow(model[i+1:]); ok {
			return size, true
		}
	}
	return DefaultContextWindow, false
}

func longestPrefixContextWindow(model string) (int, bool) {
	if size, ok := ContextWindowSizes[model]; ok {
		return size, true
	}
	best, bestLen := 0, 0
	for prefix, size := range ContextWindowSizes {
		if len(prefix) > bestLen && strings.HasPrefix(model, prefix) {
			best, bestLen = size, len(prefix)
		}
	}
	return best, bestLen > 0
}
