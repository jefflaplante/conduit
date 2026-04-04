package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

// expandEnvTagged expands ${ENV_VAR} placeholders using os.ExpandEnv
// in all string fields tagged with cfg:"env".
func (c *Config) expandEnvTagged() {
	walkStringFields(reflect.ValueOf(c).Elem(), func(field reflect.Value, tags reflect.StructTag) {
		if hasCfgFlag(tags, "env") {
			field.SetString(os.ExpandEnv(field.String()))
		}
	})
}

// expandEnvMaps expands ${ENV_VAR} in map[string]interface{} fields that
// can't use struct tags (ChannelConfig.Config, ToolsConfig.Services).
func (c *Config) expandEnvMaps() {
	for i := range c.Channels {
		for key, value := range c.Channels[i].Config {
			if strVal, ok := value.(string); ok {
				c.Channels[i].Config[key] = os.ExpandEnv(strVal)
			}
		}
	}
	for _, serviceConfig := range c.Tools.Services {
		for key, value := range serviceConfig {
			if strVal, ok := value.(string); ok {
				serviceConfig[key] = os.ExpandEnv(strVal)
			}
		}
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
