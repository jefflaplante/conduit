package ai

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Status-code extraction for provider errors. conduit-31jg.18(c): the old
// classifier did strings.Contains(msg, "400"), which also matched "4000".
var (
	// "API error: 400 - ..." (anthropic.go, streaming.go, openai.go),
	// possibly wrapped ("AI provider error: API error: 400 - ...").
	apiErrorStatusRe = regexp.MustCompile(`(?i)api error:\s*(\d{3})\b`)
	// "status 429" / "status code: 429" / "status=429".
	statusWordRe = regexp.MustCompile(`(?i)\bstatus(?:[ _]?code)?\s*[:=]?\s*(\d{3})\b`)
	// Legacy bare form at the start of the message: "400 - ..." / "400 quota ...".
	leadingStatusRe = regexp.MustCompile(`^\s*(\d{3})\b`)
)

// providerStatusCode extracts the HTTP status code from a provider error
// message, or 0 when none can be identified. Only well-delimited 3-digit
// numbers in known positions count — never a substring of a larger number.
func providerStatusCode(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	for _, re := range []*regexp.Regexp{apiErrorStatusRe, statusWordRe, leadingStatusRe} {
		if m := re.FindStringSubmatch(msg); m != nil {
			if code, convErr := strconv.Atoi(m[1]); convErr == nil && code >= 100 && code <= 599 {
				return code
			}
		}
	}
	return 0
}

// providerErrorBody extracts the error type/code/message from the JSON body
// embedded in a provider error ("API error: NNN - {json}"). Handles the
// Anthropic shape {"error":{"type","message"}} and the OpenAI-compatible
// shape {"error":{"type","code","message"}}. Missing fields are "".
func providerErrorBody(err error) (errType, errCode, errMsg string) {
	msg := err.Error()
	idx := strings.Index(msg, "{")
	if idx < 0 {
		return "", "", ""
	}
	var parsed struct {
		Error struct {
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(msg[idx:]), &parsed) != nil {
		return "", "", ""
	}
	code := strings.Trim(string(parsed.Error.Code), `"`)
	if code == "null" {
		code = ""
	}
	return parsed.Error.Type, code, parsed.Error.Message
}

// quotaErrorTypes are provider error types that unambiguously mean the
// account is out of quota/credit (retrying the same provider cannot help).
var quotaErrorTypes = map[string]bool{
	"insufficient_quota": true, // OpenAI
	"quota_error":        true,
	"billing_error":      true, // Anthropic
}

// quotaErrorCodes are provider-specific error codes for balance/quota
// exhaustion. z.ai: 1113 insufficient balance, 1308/1310 usage limit reached.
var quotaErrorCodes = map[string]bool{
	"insufficient_quota": true,
	"1113":               true,
	"1308":               true,
	"1310":               true,
}

// quotaPhrases indicate quota/credit exhaustion in an error message. Bare
// "limit"/"exceeded" are deliberately absent: they also describe context
// overflow ("exceed context limit") and per-minute rate limits.
var quotaPhrases = []string{
	"out of extra usage",
	"quota",
	"credit balance",
	"credits",
	"insufficient balance",
	"insufficient credit",
	"insufficient funds",
	"insufficient",
	"usage limit",
	"spend limit",
	"spending limit",
	"billing",
	"recharge",
}

// contextOverflowPhrases identify "request too large for the model" 400s,
// which must never be treated as quota (the fallback would get the same
// oversized request).
var contextOverflowPhrases = []string{
	"context limit",
	"context window",
	"context length",
	"prompt is too long",
	"too many tokens",
	"maximum context",
	"max_tokens",
}

// IsQuotaError determines if an error is a quota-exhaustion error.
// Used for fallback retry logic in bd-6tb.
//
// Deliberately NARROW: Claude Max quota exhaustion presents as HTTP 400 with
// "out of extra usage" (bd-8dy: 514 occurrences, zero 401s). Auth failures
// (401/403) must propagate so callers see credential problems instead of
// silently falling back to another provider — see
// TestGenerateResponseSmart_NoFallbackOnAuthError for the contract.
//
// conduit-31jg.18(c): classification is by parsed status code and error
// type/code, not substrings of the whole message:
//   - status must be exactly 400, 402 or 429 (no "400" inside "4000");
//   - an explicit quota/billing error type or code is quota;
//   - otherwise a quota phrase is required, and context-overflow wording
//     ("exceed context limit", "prompt is too long") is never quota;
//   - a 429 is quota only with an explicit quota type/code/phrase — a plain
//     rate_limit_error is transient and handled by provider retry/backoff.
func IsQuotaError(err error) bool {
	if err == nil {
		return false
	}

	status := providerStatusCode(err)
	if status != 400 && status != 402 && status != 429 {
		return false
	}

	errType, errCode, errMsg := providerErrorBody(err)
	if quotaErrorTypes[strings.ToLower(errType)] || quotaErrorCodes[strings.ToLower(errCode)] {
		return true
	}

	// Prefer the parsed message; fall back to the whole error string for
	// non-JSON bodies ("API error: 400 - out of extra usage").
	text := strings.ToLower(errMsg)
	if text == "" {
		text = strings.ToLower(err.Error())
	}
	for _, p := range contextOverflowPhrases {
		if strings.Contains(text, p) {
			return false
		}
	}
	if status == 429 && strings.Contains(strings.ToLower(errType), "rate_limit") {
		return false
	}
	for _, p := range quotaPhrases {
		if strings.Contains(text, p) {
			return true
		}
	}
	// A 400/402 "rate limit" is an account-level limit (not the retryable
	// 429 kind) — same-provider retry cannot clear it, so fall back.
	if status != 429 && strings.Contains(text, "rate limit") {
		return true
	}
	return false
}
