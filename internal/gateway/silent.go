package gateway

import "strings"

// isSilentResponse returns true if the response content contains a silent
// token (NO_REPLY or HEARTBEAT_OK) anywhere in the body, meaning it should
// not be delivered to the user. The LLM sometimes wraps the token in
// surrounding text rather than sending it as the sole content.
func isSilentResponse(content string) bool {
	upper := strings.ToUpper(content)
	return strings.Contains(upper, "NO_REPLY") || strings.Contains(upper, "HEARTBEAT_OK")
}
