package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

// walkStringFields recursively walks a struct and calls fn for each string field,
// passing the settable reflect.Value and the field's struct tags.
// Handles nested structs, pointer-to-struct, and slices of structs.
func walkStringFields(v reflect.Value, fn func(field reflect.Value, tags reflect.StructTag)) {
	switch v.Kind() {
	case reflect.Ptr:
		if !v.IsNil() {
			walkStringFields(v.Elem(), fn)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			if !field.CanSet() {
				continue
			}
			sf := t.Field(i)
			switch field.Kind() {
			case reflect.String:
				fn(field, sf.Tag)
			case reflect.Struct:
				walkStringFields(field, fn)
			case reflect.Ptr:
				if !field.IsNil() && field.Elem().Kind() == reflect.Struct {
					walkStringFields(field.Elem(), fn)
				}
			case reflect.Slice:
				for j := 0; j < field.Len(); j++ {
					elem := field.Index(j)
					if elem.Kind() == reflect.Struct {
						walkStringFields(elem, fn)
					} else if elem.Kind() == reflect.Ptr && !elem.IsNil() && elem.Elem().Kind() == reflect.Struct {
						walkStringFields(elem.Elem(), fn)
					}
				}
			}
		}
	}
}

// hasCfgFlag returns true if the cfg struct tag contains the given flag.
// Tags are comma-separated, e.g. cfg:"env,path".
func hasCfgFlag(tags reflect.StructTag, flag string) bool {
	val := tags.Get("cfg")
	if val == "" {
		return false
	}
	for _, part := range strings.Split(val, ",") {
		if strings.TrimSpace(part) == flag {
			return true
		}
	}
	return false
}

// expandTildeTagged replaces leading "~/" with the user's home directory
// in all string fields tagged with cfg:"path".
func (c *Config) expandTildeTagged() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	expand := func(p string) string {
		if p == "~" {
			return home
		}
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(home, p[2:])
		}
		return p
	}

	walkStringFields(reflect.ValueOf(c).Elem(), func(field reflect.Value, tags reflect.StructTag) {
		if hasCfgFlag(tags, "path") {
			field.SetString(expand(field.String()))
		}
	})
}

// envRefPattern matches ${NAME} or ${NAME:-default} (NAME = group 2,
// default = group 4) optionally preceded by an extra '$' escape (group 1).
// NAME must be a valid shell identifier.
var envRefPattern = regexp.MustCompile(`\$(\$?)\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// expandEnvBraced expands ONLY ${NAME} references in s.
//
// conduit-31jg.5: this replaces os.ExpandEnv, which also expanded bare
// $name and collapsed $$ — so a secret such as "p@ss$word1" silently became
// "p@ss" and "a$$b" became "ab". Rules:
//
//   - ${NAME}   -> value of environment variable NAME ("" when unset, as
//     before; validation treats an empty credential as "not configured")
//   - ${NAME:-default} -> NAME's value, or default when NAME is unset/empty
//   - $${NAME}  -> the literal text ${NAME} (escape)
//   - any other '$' (bare $name, $$, trailing $, ${not-an-identifier}) is
//     left untouched.
func expandEnvBraced(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return envRefPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := envRefPattern.FindStringSubmatch(m)
		if sub[1] != "" {
			return m[1:] // escaped: drop the extra '$'
		}
		if v := os.Getenv(sub[2]); v != "" || sub[3] == "" {
			return v
		}
		return sub[4]
	})
}

// expandEnvTagged expands ${ENV_VAR} placeholders (see expandEnvBraced)
// in all string fields tagged with cfg:"env".
func (c *Config) expandEnvTagged() {
	walkStringFields(reflect.ValueOf(c).Elem(), func(field reflect.Value, tags reflect.StructTag) {
		if hasCfgFlag(tags, "env") {
			field.SetString(expandEnvBraced(field.String()))
		}
	})
}

// expandEnvMaps expands ${ENV_VAR} in map[string]interface{} fields that
// can't use struct tags (ChannelConfig.Config, ToolsConfig.Services).
// conduit-31jg.5: recurses into nested maps and slices.
func (c *Config) expandEnvMaps() {
	for i := range c.Channels {
		expandEnvInMap(c.Channels[i].Config)
	}
	for _, serviceConfig := range c.Tools.Services {
		expandEnvInMap(serviceConfig)
	}
}

// expandEnvInMap expands string values in m in place, recursing into nested
// maps and slices.
func expandEnvInMap(m map[string]interface{}) {
	for key, value := range m {
		m[key] = expandEnvValue(value)
	}
}

func expandEnvValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return expandEnvBraced(val)
	case map[string]interface{}:
		expandEnvInMap(val)
		return val
	case []interface{}:
		for i := range val {
			val[i] = expandEnvValue(val[i])
		}
		return val
	default:
		return v
	}
}

// validateEnumTags checks all string fields tagged with validate:"enum=val1|val2|..."
// and returns an error if a non-empty field value is not in the allowed set.
func validateEnumTags(v interface{}) error {
	return walkValidateEnums(reflect.ValueOf(v), "")
}

func walkValidateEnums(v reflect.Value, path string) error {
	switch v.Kind() {
	case reflect.Ptr:
		if !v.IsNil() {
			return walkValidateEnums(v.Elem(), path)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			sf := t.Field(i)
			fieldPath := path + "." + sf.Name
			if path == "" {
				fieldPath = sf.Name
			}

			switch field.Kind() {
			case reflect.String:
				if err := checkEnum(field.String(), sf.Tag, fieldPath); err != nil {
					return err
				}
			case reflect.Struct:
				if err := walkValidateEnums(field, fieldPath); err != nil {
					return err
				}
			case reflect.Ptr:
				if !field.IsNil() && field.Elem().Kind() == reflect.Struct {
					if err := walkValidateEnums(field.Elem(), fieldPath); err != nil {
						return err
					}
				}
			case reflect.Slice:
				for j := 0; j < field.Len(); j++ {
					elem := field.Index(j)
					elemPath := fmt.Sprintf("%s[%d]", fieldPath, j)
					if elem.Kind() == reflect.Struct {
						if err := walkValidateEnums(elem, elemPath); err != nil {
							return err
						}
					} else if elem.Kind() == reflect.Ptr && !elem.IsNil() && elem.Elem().Kind() == reflect.Struct {
						if err := walkValidateEnums(elem.Elem(), elemPath); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

func checkEnum(value string, tags reflect.StructTag, fieldPath string) error {
	enumTag := tags.Get("validate")
	if enumTag == "" {
		return nil
	}
	if !strings.HasPrefix(enumTag, "enum=") {
		return nil
	}
	if value == "" {
		return nil // empty is ok — means "use default"
	}
	allowed := strings.Split(strings.TrimPrefix(enumTag, "enum="), "|")
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("invalid value %q for %s (allowed: %s)", value, fieldPath, strings.Join(allowed, ", "))
}
