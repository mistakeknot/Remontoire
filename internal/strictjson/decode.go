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
	return rejectFields(data, target, true)
}

// RejectCaseAliases rejects object keys that match a target JSON field name
// only by case. Go's decoder maps such a key onto the field, so a repeated
// field spelled two ways would slip past duplicate-key detection. Keys that
// match no field at all are left to the caller.
func RejectCaseAliases(data []byte, target any) error {
	return rejectFields(data, target, false)
}

func rejectFields(data []byte, target any, exact bool) error {
	targetType := reflect.TypeOf(target)
	if targetType == nil || targetType.Kind() != reflect.Pointer {
		return fmt.Errorf("field validation target must be a pointer")
	}
	return rejectFieldsAt(json.RawMessage(data), targetType.Elem(), "$", exact)
}

var unmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

// jsonFields maps each JSON name of a struct, including promoted fields of
// untagged embedded structs, to its type.
func jsonFields(targetType reflect.Type, fields map[string]reflect.Type) {
	for i := 0; i < targetType.NumField(); i++ {
		field := targetType.Field(i)
		tag := field.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" && tag == "-" {
			continue
		}
		if field.Anonymous && name == "" {
			embedded := field.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				jsonFields(embedded, fields)
				continue
			}
		}
		if !field.IsExported() {
			continue
		}
		if name == "" {
			name = field.Name
		}
		if _, taken := fields[name]; !taken {
			fields[name] = field.Type
		}
	}
}

func rejectFieldsAt(data json.RawMessage, targetType reflect.Type, path string, exact bool) error {
	data = bytes.TrimSpace(data)
	if targetType.Kind() == reflect.Pointer {
		if bytes.Equal(data, []byte("null")) {
			return nil
		}
		return rejectFieldsAt(data, targetType.Elem(), path, exact)
	}
	if targetType.Implements(unmarshalerType) || reflect.PointerTo(targetType).Implements(unmarshalerType) {
		return nil
	}

	switch targetType.Kind() {
	case reflect.Struct:
		if !exact && bytes.Equal(data, []byte("null")) {
			return nil
		}
		if len(data) == 0 || data[0] != '{' {
			return fmt.Errorf("expected JSON object at %s", path)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		fields := make(map[string]reflect.Type, targetType.NumField())
		jsonFields(targetType, fields)
		for name, value := range object {
			fieldType, ok := fields[name]
			if !ok {
				if exact {
					return fmt.Errorf("JSON object field %q is not an exact field at %s", name, path)
				}
				for known := range fields {
					if strings.EqualFold(known, name) {
						return fmt.Errorf("JSON object field %q is a case variant of %q at %s", name, known, path)
					}
				}
				continue
			}
			if err := rejectFieldsAt(value, fieldType, path+"."+name, exact); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if targetType.Elem().Kind() == reflect.Uint8 || (!exact && bytes.Equal(data, []byte("null"))) {
			return nil
		}
		if len(data) == 0 || data[0] != '[' {
			return fmt.Errorf("expected JSON array at %s", path)
		}
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for i, value := range values {
			if err := rejectFieldsAt(value, targetType.Elem(), fmt.Sprintf("%s[%d]", path, i), exact); err != nil {
				return err
			}
		}
	case reflect.Map:
		if !exact && bytes.Equal(data, []byte("null")) {
			return nil
		}
		if len(data) == 0 || data[0] != '{' {
			return fmt.Errorf("expected JSON object at %s", path)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for name, value := range values {
			if err := rejectFieldsAt(value, targetType.Elem(), path+"."+name, exact); err != nil {
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
