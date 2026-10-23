package protocol

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is the full wire protocol version.
const Version = "2.2"

// Major is the only wire protocol major this package speaks.
const Major = 2

// ParseVersion parses MAJOR or MAJOR.MINOR; an omitted minor is zero.
func ParseVersion(v string) (major, minor int, err error) {
	a, b, hasMinor := strings.Cut(v, ".")
	parse := func(s string) (int, error) {
		if s == "" || strings.TrimLeft(s, "0123456789") != "" {
			return 0, fmt.Errorf("not an unsigned integer")
		}
		return strconv.Atoi(s)
	}
	major, err = parse(a)
	if err == nil && hasMinor {
		minor, err = parse(b)
	}
	if err != nil {
		return 0, 0, &Error{Code: CodeVersionMismatch, Message: fmt.Sprintf("invalid protocol version %q: want MAJOR.MINOR", v)}
	}
	return major, minor, nil
}

// CheckVersion accepts any minor of Major and rejects absent or invalid versions.
func CheckVersion(v string) error {
	major, _, err := ParseVersion(v)
	if err != nil {
		return err
	}
	if major != Major {
		return &Error{Code: CodeVersionMismatch, Message: fmt.Sprintf("unsupported protocol version %q: host speaks %s", v, Version)}
	}
	return nil
}
