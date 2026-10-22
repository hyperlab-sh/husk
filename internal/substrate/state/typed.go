package state

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Slice is a core owner's typed view over one write capability. Values are
// decoded from State rather than cached, so a user Clear is immediately visible.
// All methods are loop-only. Do not copy a Slice to share write authority.
type Slice[T any] struct {
	handle *Handle
}

// Register creates a typed slice, using initial instead of spec.Initial.
// T must encode as a JSON object and decode from that encoding. If hydration
// cannot decode into T, registration succeeds at initial and returns both a
// usable slice and a wrapped error; the owner should log that error and continue.
func Register[T any](s *State, spec Spec, initial T) (*Slice[T], error) {
	value, err := json.Marshal(initial)
	if err != nil {
		return nil, fmt.Errorf("%w: initial: %v", ErrInvalidValue, err)
	}
	if err := validateValue(value, HardMaxBytes); err != nil {
		return nil, err
	}
	if _, err := decode[T](value); err != nil {
		return nil, fmt.Errorf("state: initial cannot decode: %w", err)
	}
	spec.Initial = value
	handle, err := s.register(spec, func(raw json.RawMessage) error {
		_, err := decode[T](raw)
		return err
	})
	if handle == nil {
		return nil, err
	}
	return &Slice[T]{handle: handle}, err
}

func decode[T any](value json.RawMessage) (T, error) {
	var v T
	err := json.Unmarshal(value, &v)
	return v, err
}

// Get returns an independently decoded current value, including user clears.
// Callers treat it as read-only. After unregistration it returns T's zero value.
func (s *Slice[T]) Get() T {
	var zero T
	if s == nil || !s.handle.registered() {
		return zero
	}
	v, _ := decode[T](s.handle.record.entry.Value)
	return v
}

// Set commits v as a JSON object. State owns v afterwards; callers must not
// mutate it. In fact the encoding is copied, isolating State from aliases.
func (s *Slice[T]) Set(v T) error {
	if s == nil || !s.handle.registered() {
		return ErrUnregistered
	}
	value, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValue, err)
	}
	if _, err := decode[T](value); err != nil {
		return fmt.Errorf("state: value cannot decode: %w", err)
	}
	_, err = s.handle.Set(value)
	return err
}

// Update gives f an independent deep copy and commits only when its encoded
// value changed. Comparing before and after the callback also preserves unknown
// hydrated fields when f changes nothing in the typed representation.
func (s *Slice[T]) Update(f func(*T)) error {
	if s == nil || !s.handle.registered() {
		return ErrUnregistered
	}
	v, err := decode[T](s.handle.record.entry.Value)
	if err != nil {
		return err
	}
	before, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValue, err)
	}
	f(&v)
	after, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValue, err)
	}
	if bytes.Equal(before, after) {
		return nil
	}
	return s.Set(v)
}
