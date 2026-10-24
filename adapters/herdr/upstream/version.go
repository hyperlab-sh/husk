package upstream

import (
	"fmt"
	"regexp"
	"strings"
)

type version struct {
	core [3]string
	pre  []string
}

func parseVersion(text string) (version, error) {
	// SemVer 2.0: no leading zeroes in numeric identifiers; build metadata does
	// not affect ordering. Keep numeric components as strings to avoid overflow.
	re := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		return version{}, fmt.Errorf("invalid semantic version %q", text)
	}
	v := version{core: [3]string{m[1], m[2], m[3]}}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
		for _, p := range v.pre {
			if numeric(p) && len(p) > 1 && p[0] == '0' {
				return version{}, fmt.Errorf("invalid semantic version %q", text)
			}
		}
	}
	return v, nil
}

func numeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func numberCompare(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

func compare(a, b version) int {
	for i := range a.core {
		if n := numberCompare(a.core[i], b.core[i]); n != 0 {
			return n
		}
	}
	if len(a.pre) == 0 && len(b.pre) > 0 {
		return 1
	}
	if len(b.pre) == 0 && len(a.pre) > 0 {
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		n := strings.Compare(x, y)
		if numeric(x) && numeric(y) {
			n = numberCompare(x, y)
		} else if numeric(x) != numeric(y) {
			if numeric(x) {
				n = -1
			} else {
				n = 1
			}
		}
		if n != 0 {
			return n
		}
	}
	if len(a.pre) < len(b.pre) {
		return -1
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return 0
}

// CheckVersion enforces both the adapter floor (0.9.0) and the plugin's
// min_herdr_version using semantic-version ordering, including prereleases.
func CheckVersion(server, minimum string) error {
	actual, err := parseVersion(server)
	if err != nil {
		return err
	}
	floor, _ := parseVersion("0.9.0")
	required, err := parseVersion(minimum)
	if err != nil {
		return err
	}
	if compare(required, floor) < 0 {
		required = floor
		minimum = "0.9.0"
	}
	if compare(actual, required) < 0 {
		return &Error{Code: "herdr_too_old", Message: fmt.Sprintf("needs %s, server is %s", minimum, server)}
	}
	return nil
}
