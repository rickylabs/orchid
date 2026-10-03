package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"unicode/utf8"
)

// Private authority has exact, case-sensitive keys, required non-null fields,
// and bounded UTF-8 bytes. This complements the existing duplicate-key guard.
func ownerNativeStrictJSON(raw []byte, out any) error {
	if len(raw) > ownerNativeFrameLimit || !utf8.Valid(raw) || ownerNativeJSONShape(raw, reflect.TypeOf(out).Elem()) != nil {
		return errMatrix
	}
	return strictJSON(raw, out)
}

func ownerNativeJSONShape(raw []byte, kind reflect.Type) error {
	if string(raw) == "null" {
		return errMatrix
	}
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	switch kind.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return errMatrix
		}
		allowed := map[string]bool{}
		for i := 0; i < kind.NumField(); i++ {
			field := kind.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")
			if field.PkgPath != "" || tag[0] == "-" {
				continue
			}
			name := tag[0]
			if name == "" {
				name = field.Name
			}
			allowed[name] = true
			value, ok := fields[name]
			optional := len(tag) > 1 && tag[1] == "omitempty"
			if !ok {
				if !optional {
					return errMatrix
				}
				continue
			}
			if ownerNativeJSONShape(value, field.Type) != nil {
				return errMatrix
			}
		}
		for name := range fields {
			if !allowed[name] {
				return errMatrix
			}
		}
	case reflect.Slice:
		var entries []json.RawMessage
		if json.Unmarshal(raw, &entries) != nil {
			return errMatrix
		}
		for _, entry := range entries {
			if ownerNativeJSONShape(entry, kind.Elem()) != nil {
				return errMatrix
			}
		}
	default:
		if json.Unmarshal(raw, reflect.New(kind).Interface()) != nil {
			return errMatrix
		}
	}
	return nil
}
