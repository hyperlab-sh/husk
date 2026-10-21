package huskipc

import (
	"fmt"
	"strconv"
	"strings"
)

// ProtocolVersion is the wire protocol version this package speaks, as
// "MAJOR.MINOR". The host sends it in welcome.protocol_version and a plugin
// should send the version it was written against in hello.protocol_version.
//
// Compatibility rule: a minor bump only adds optional fields or message types
// (both sides ignore what they do not know); a major bump is a breaking change.
// The host rejects a hello whose major differs from its own with a
// protocol_error and closes the connection.
//
// 1.1: welcome.palette and theme_changed.palette values are "#RRGGBB" strings
// instead of ANSI/256 indices; the keys are unchanged.
const ProtocolVersion = "1.1"

// ProtocolMajor is the major component of ProtocolVersion.
const ProtocolMajor = 1

// ParseProtocolVersion splits a "MAJOR" or "MAJOR.MINOR" version string into
// its numeric components.
func ParseProtocolVersion(v string) (major, minor int, err error) {
	majorStr, minorStr, hasMinor := strings.Cut(v, ".")
	major, err = parseVersionPart(majorStr)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid protocol_version %q: want MAJOR.MINOR", v)
	}
	if hasMinor {
		minor, err = parseVersionPart(minorStr)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid protocol_version %q: want MAJOR.MINOR", v)
		}
	}
	return major, minor, nil
}

func parseVersionPart(s string) (int, error) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0, fmt.Errorf("not a number")
	}
	return strconv.Atoi(s)
}

// CheckProtocolVersion reports whether a peer announcing version v can talk to
// this package. An empty v is a plugin written before versioning existed and
// is treated as major 1. Any minor of the same major is compatible.
func CheckProtocolVersion(v string) error {
	if v == "" {
		return nil
	}
	major, _, err := ParseProtocolVersion(v)
	if err != nil {
		return err
	}
	if major != ProtocolMajor {
		return fmt.Errorf("unsupported protocol_version %q: host speaks %s (major %d)", v, ProtocolVersion, ProtocolMajor)
	}
	return nil
}
