package protocol

import (
	"fmt"
	"reflect"
	"strings"
)

// ANSIRoles maps palette role names (never name) to terminal colour indices
// 0..15. A nil or empty map is omitted by theme payloads.
type ANSIRoles map[string]int

// Validate rejects unknown palette roles and indices outside 0..15.
func (a ANSIRoles) Validate() error {
	palette := reflect.TypeFor[Palette]()
	for role, index := range a {
		if index < 0 || index > 15 {
			return fmt.Errorf("ANSI role %q: want index 0..15", role)
		}
		known := false
		for i := 0; i < palette.NumField(); i++ {
			tag, _, _ := strings.Cut(palette.Field(i).Tag.Get("json"), ",")
			if tag != "name" && tag == role {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown ANSI palette role %q", role)
		}
	}
	return nil
}
