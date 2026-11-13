package brain

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// parseToolArguments rejects ambiguous JSON and preserves numbers exactly.
func parseToolArguments(args string) (string, map[string]any, error) {
	invalid := errors.New("invalid tool arguments")
	if len(args) > 4096 {
		return "", nil, invalid
	}
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber()
	var readValue func(int) (any, error)
	readValue = func(depth int) (any, error) {
		if depth > 64 {
			return nil, invalid
		}
		token, err := dec.Token()
		if err != nil {
			return nil, invalid
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				object := make(map[string]any)
				for dec.More() {
					keyToken, err := dec.Token()
					if err != nil {
						return nil, invalid
					}
					key, ok := keyToken.(string)
					if !ok {
						return nil, invalid
					}
					if _, exists := object[key]; exists {
						return nil, invalid
					}
					value, err := readValue(depth + 1)
					if err != nil {
						return nil, invalid
					}
					object[key] = value
				}
				end, err := dec.Token()
				if err != nil || end != json.Delim('}') {
					return nil, invalid
				}
				return object, nil
			case '[':
				array := make([]any, 0)
				for dec.More() {
					value, err := readValue(depth + 1)
					if err != nil {
						return nil, invalid
					}
					array = append(array, value)
				}
				end, err := dec.Token()
				if err != nil || end != json.Delim(']') {
					return nil, invalid
				}
				return array, nil
			default:
				return nil, invalid
			}
		}
		return token, nil
	}
	value, err := readValue(0)
	object, ok := value.(map[string]any)
	if err != nil || !ok {
		return "", nil, invalid
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", nil, invalid
	}
	data, err := json.Marshal(object)
	if err != nil {
		return "", nil, invalid
	}
	return string(data), object, nil
}
