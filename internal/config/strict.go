package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// validateConfigFields follows the declared JSON types, including map values
// and slice elements. Map names and plugin-owned RawMessage options remain
// free-form. Only inert documentation annotations are exempt in typed objects.
// Checking exact tags also rejects case variants that encoding/json accepts.
func validateConfigFields(raw json.RawMessage, typ reflect.Type, path string) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeFor[json.RawMessage]() || string(raw) == "null" {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return fmt.Errorf("config: %s: %w", path, err)
		}
		declared := make(map[string]reflect.Type, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.IsExported() && name != "-" && name != "" {
				declared[name] = field.Type
			}
		}
		for _, name := range sortedConfigFields(fields) {
			fieldType, ok := declared[name]
			if !ok {
				if name == "_comment" || strings.HasPrefix(name, "_comment_") ||
					(strings.HasPrefix(name, "_") && strings.HasSuffix(name, "_example")) {
					continue
				}
				return fmt.Errorf("config: unknown field %s.%s", path, name)
			}
			if err := validateConfigFields(fields[name], fieldType, path+"."+name); err != nil {
				return err
			}
		}
	case reflect.Map:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return fmt.Errorf("config: %s: %w", path, err)
		}
		for _, name := range sortedConfigFields(fields) {
			if err := validateConfigFields(fields[name], typ.Elem(), fmt.Sprintf("%s[%q]", path, name)); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var elements []json.RawMessage
		if err := json.Unmarshal(raw, &elements); err != nil {
			return fmt.Errorf("config: %s: %w", path, err)
		}
		for i, element := range elements {
			if err := validateConfigFields(element, typ.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedConfigFields(fields map[string]json.RawMessage) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
