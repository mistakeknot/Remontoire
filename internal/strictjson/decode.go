package strictjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// RejectDuplicateKeys validates one JSON value and rejects duplicate object keys.
func RejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON token %v", token)
		}
		return err
	}
	return nil
}

// RejectNonExactFields rejects object keys that do not exactly match the target's JSON field names.
func RejectNonExactFields(data []byte, target any) error {
	targetType := reflect.TypeOf(target)
	if targetType == nil || targetType.Kind() != reflect.Pointer {
		return fmt.Errorf("exact field validation target must be a pointer")
	}
	return rejectNonExactFields(json.RawMessage(data), targetType.Elem(), "$")
}

func rejectNonExactFields(data json.RawMessage, targetType reflect.Type, path string) error {
	data = bytes.TrimSpace(data)
	if targetType.Kind() == reflect.Pointer {
		if bytes.Equal(data, []byte("null")) {
			return nil
		}
		return rejectNonExactFields(data, targetType.Elem(), path)
	}

	switch targetType.Kind() {
	case reflect.Struct:
		if len(data) == 0 || data[0] != '{' {
			return fmt.Errorf("expected JSON object at %s", path)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		fields := make(map[string]reflect.Type, targetType.NumField())
		for i := 0; i < targetType.NumField(); i++ {
			field := targetType.Field(i)
			if !field.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
		}
		for name, value := range object {
			fieldType, ok := fields[name]
			if !ok {
				return fmt.Errorf("JSON object field %q is not an exact field at %s", name, path)
			}
			if err := rejectNonExactFields(value, fieldType, path+"."+name); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if len(data) == 0 || data[0] != '[' {
			return fmt.Errorf("expected JSON array at %s", path)
		}
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for i, value := range values {
			if err := rejectNonExactFields(value, targetType.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		if len(data) == 0 || data[0] != '{' {
			return fmt.Errorf("expected JSON object at %s", path)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for name, value := range values {
			if err := rejectNonExactFields(value, targetType.Elem(), path+"."+name); err != nil {
				return err
			}
		}
	}
	return nil
}

func scanValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = true
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		return consumeDelimiter(decoder, '}')
	case '[':
		for decoder.More() {
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		return consumeDelimiter(decoder, ']')
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func consumeDelimiter(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != want {
		return fmt.Errorf("unexpected JSON delimiter %v", token)
	}
	return nil
}
