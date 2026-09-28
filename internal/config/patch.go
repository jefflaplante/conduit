package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// PatchOp is one leaf assignment of a config update (conduit-rmho).
type PatchOp struct {
	// Path is the key path. A segment that meets a JSON array selects the
	// element whose "name" field equals it (providers, channels), or an
	// index.
	Path []string
	// Value is the new JSON value (a string, number, bool, array or object);
	// ignored when Delete is set.
	Value interface{}
	// Delete removes the key (the patch gave null).
	Delete bool
}

// FlattenPatch turns an update_config patch into leaf assignments, merge-
// patch style (RFC 7396): nested objects merge into the existing ones, any
// other value (scalar, array, empty object) replaces the value at its
// path, and null deletes the key. Top-level keys may be dot-paths
// ("ai.providers.z-ai.timeout_seconds"); keys inside nested objects are
// taken literally, so a name containing dots ("glm-5.3") must be given as a
// nested key. Ops are returned sorted by path.
func FlattenPatch(patch map[string]interface{}) ([]PatchOp, error) {
	if len(patch) == 0 {
		return nil, fmt.Errorf("config update is empty")
	}
	var ops []PatchOp
	for k, v := range patch {
		segs := strings.Split(k, ".")
		for _, s := range segs {
			if strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("invalid config key %q: empty path segment", k)
			}
		}
		flattenInto(&ops, segs, v)
	}
	sort.Slice(ops, func(i, j int) bool { return JoinPath(ops[i].Path) < JoinPath(ops[j].Path) })
	for i := range ops {
		for j := range ops {
			if i != j && pathHasPrefix(ops[j].Path, ops[i].Path) {
				a := JoinPath(ops[i].Path)
				return nil, fmt.Errorf("config update sets %q twice, or both %q and a key inside it", a, a)
			}
		}
	}
	return ops, nil
}

func flattenInto(ops *[]PatchOp, path []string, v interface{}) {
	if m, ok := v.(map[string]interface{}); ok && len(m) > 0 {
		for k, child := range m {
			flattenInto(ops, append(append([]string(nil), path...), k), child)
		}
		return
	}
	if v == nil {
		*ops = append(*ops, PatchOp{Path: path, Delete: true})
		return
	}
	*ops = append(*ops, PatchOp{Path: path, Value: v})
}

// envPlaceholderRe matches a value that is exactly one ${VAR} reference.
var envPlaceholderRe = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// urlPasswordRe captures the password of a URL with userinfo.
var urlPasswordRe = regexp.MustCompile(`://[^/\s:@]+:([^@\s/]+)@`)

// IsEnvPlaceholder reports whether s is exactly one ${VAR} reference.
func IsEnvPlaceholder(s string) bool { return envPlaceholderRe.MatchString(s) }

// CheckPatchSecrets refuses an op that would write a literal credential to
// the config (conduit-rmho): a non-empty value under a secret key (see
// IsSecretKey) must be a ${VAR} reference, and a URL password likewise. It
// also refuses the RedactedValue marker, which is what a model sees in the
// redacted config and must never be written back as a real value.
func CheckPatchSecrets(op PatchOp) error {
	if op.Delete {
		return nil
	}
	key := op.Path[len(op.Path)-1]
	return checkSecretValue(JoinPath(op.Path), key, op.Value)
}

func checkSecretValue(where, key string, v interface{}) error {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, c := range val {
			if err := checkSecretValue(where+"."+k, k, c); err != nil {
				return err
			}
		}
	case []interface{}:
		for i, c := range val {
			if err := checkSecretValue(fmt.Sprintf("%s[%d]", where, i), key, c); err != nil {
				return err
			}
		}
	case string:
		if strings.Contains(val, RedactedValue) {
			return fmt.Errorf("%s: %q is the redaction marker, not a real value; leave the key out to keep its current value", where, RedactedValue)
		}
		if val != "" && IsSecretKey(key) && !IsEnvPlaceholder(val) {
			return fmt.Errorf("%s: secrets cannot be written through update_config; set it to an environment reference like ${MY_API_KEY} and put the value in the environment or secrets_file", where)
		}
		if m := urlPasswordRe.FindStringSubmatch(val); m != nil && !IsEnvPlaceholder(m[1]) {
			return fmt.Errorf("%s: URL passwords cannot be written through update_config; use an environment reference like ${MY_PASSWORD}", where)
		}
	}
	return nil
}

// pathHasPrefix reports whether p starts with (or equals) prefix.
func pathHasPrefix(p, prefix []string) bool {
	if len(p) < len(prefix) {
		return false
	}
	for i := range prefix {
		if p[i] != prefix[i] {
			return false
		}
	}
	return true
}
