package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func copyJSON(value json.RawMessage) json.RawMessage {
	return bytes.Clone(value)
}

func validateValue(value json.RawMessage, maxBytes int) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(value) {
		return ErrInvalidValue
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%w: limit %d bytes", ErrTooLarge, maxBytes)
	}
	return nil
}

// mergePatch implements RFC 7396 without decoding JSON numbers as floats.
// State validates the final result separately: RFC 7396 allows scalars and
// arrays as patches, but replacing the slice root with one is forbidden.
func mergePatch(target, patch json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(patch) {
		return nil, ErrInvalidValue
	}
	patch = bytes.TrimSpace(patch)
	if patch[0] != '{' {
		return copyJSON(patch), nil
	}
	updates := make(map[string]json.RawMessage)
	if err := json.Unmarshal(patch, &updates); err != nil {
		return nil, err
	}
	object := make(map[string]json.RawMessage)
	trimmed := bytes.TrimSpace(target)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if err := json.Unmarshal(target, &object); err != nil {
			return nil, err
		}
	}
	for key, value := range updates {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			delete(object, key)
			continue
		}
		merged, err := mergePatch(object[key], value)
		if err != nil {
			return nil, err
		}
		object[key] = merged
	}
	return json.Marshal(object)
}

// At resolves an RFC 6901 JSON Pointer within this entry. The empty pointer
// returns the whole document. Invalid pointers, absent members and out-of-range
// indices return false. The returned bytes are an independent copy; no numeric
// precision is lost. This method is safe on entries from a published snapshot.
func (e Entry) At(pointer string) (json.RawMessage, bool) {
	value := e.Value
	if pointer == "" {
		return copyJSON(value), true
	}
	if pointer[0] != '/' {
		return nil, false
	}
	for _, token := range strings.Split(pointer[1:], "/") {
		var key strings.Builder
		for i := 0; i < len(token); i++ {
			if token[i] != '~' {
				key.WriteByte(token[i])
				continue
			}
			i++
			if i == len(token) || token[i] != '0' && token[i] != '1' {
				return nil, false
			}
			if token[i] == '0' {
				key.WriteByte('~')
			} else {
				key.WriteByte('/')
			}
		}
		value = bytes.TrimSpace(value)
		if len(value) == 0 {
			return nil, false
		}
		switch value[0] {
		case '{':
			object := make(map[string]json.RawMessage)
			if json.Unmarshal(value, &object) != nil {
				return nil, false
			}
			var ok bool
			value, ok = object[key.String()]
			if !ok {
				return nil, false
			}
		case '[':
			index := key.String()
			if index == "" || len(index) > 1 && index[0] == '0' {
				return nil, false
			}
			for i := range len(index) {
				if index[i] < '0' || index[i] > '9' {
					return nil, false
				}
			}
			n, err := strconv.Atoi(index)
			var array []json.RawMessage
			if err != nil || json.Unmarshal(value, &array) != nil || n >= len(array) {
				return nil, false
			}
			value = array[n]
		default:
			return nil, false
		}
	}
	return copyJSON(value), true
}
