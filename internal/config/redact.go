package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// RedactedValue replaces every secret value in redacted output.
const RedactedValue = "[redacted]"

// secretKeyPattern matches JSON keys whose values are credentials: API/app
// keys, tokens, secrets, passwords, private keys, credentials. It is applied
// to lower-cased keys at every depth, so free-form maps (channel config,
// tools.services) are covered as well as the typed structs. Count fields
// such as max_tokens / budget_tokens do not match (plural, and they are
// numbers, which are never redacted).
var secretKeyPattern = regexp.MustCompile(
	`(api_?key|app_?key|client_?key|secret|password|passwd|passphrase|private_?key|credential|bearer|authorization|(^|_)token$|(^|_)token_)`)

// IsSecretKey reports whether a config JSON key names a secret value.
func IsSecretKey(key string) bool {
	return secretKeyPattern.MatchString(strings.ToLower(key))
}

// Redacted returns a JSON-shaped deep copy of v (maps, slices, strings,
// numbers, bools) with secrets replaced by RedactedValue (conduit-31jg.56):
//   - any non-empty value under a key matched by IsSecretKey;
//   - the password in any URL string with userinfo (e.g. a base_url of
//     https://user:pass@host).
//
// Use it whenever configuration leaves the process boundary (tool results
// shown to the model, HTTP responses, logs).
func Redacted(v interface{}) (interface{}, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("redact: marshal: %w", err)
	}
	var out interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("redact: unmarshal: %w", err)
	}
	return redactValue(out), nil
}

func redactValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, child := range val {
			if IsSecretKey(k) && !isEmptyOrScalarSetting(child) {
				val[k] = RedactedValue
				continue
			}
			val[k] = redactValue(child)
		}
		return val
	case []interface{}:
		for i := range val {
			val[i] = redactValue(val[i])
		}
		return val
	case string:
		return redactURLPassword(val)
	default:
		return v
	}
}

// isEmptyOrScalarSetting reports values under a secret-looking key that are
// not secrets themselves: empty/nil (nothing to hide, and "unset" is useful
// diagnostics), booleans and numbers (e.g. redact_secrets: true).
func isEmptyOrScalarSetting(v interface{}) bool {
	switch val := v.(type) {
	case nil:
		return true
	case string:
		return val == ""
	case bool, float64:
		return true
	case map[string]interface{}:
		return len(val) == 0
	case []interface{}:
		return len(val) == 0
	}
	return false
}

func redactURLPassword(s string) string {
	if !strings.Contains(s, "://") || !strings.Contains(s, "@") {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.User == nil {
		return s
	}
	if _, has := u.User.Password(); !has {
		return s
	}
	return u.Redacted() // password → "xxxxx"
}
