package createcmd

import (
	"go/version"
	"regexp"
	"strings"
)

var dependencyVersionRE = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)

func dependencyVersion(v string) string {
	if !dependencyVersionRE.MatchString(v) {
		return ""
	}
	if _, pre, ok := strings.Cut(v, "-"); ok {
		for _, part := range strings.Split(pre, ".") {
			if part == "" {
				return ""
			}
			if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
				return ""
			}
		}
	}
	return v
}

// goDirective uses the language version, including for release candidates and
// development/experiment toolchains whose complete build string is not a valid
// go.mod directive. Unknown toolchain strings fail closed at the caller.
func goDirective(raw string) string {
	fields := strings.Fields(strings.TrimPrefix(raw, "devel "))
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(version.Lang(fields[0]), "go")
}

// validModulePath follows Go's module character and element rules without the
// remote-domain requirement: a local module defaults to the scaffold name.
func validModulePath(module string) bool {
	if module == "" || strings.HasPrefix(module, "-") {
		return false
	}
	for _, elem := range strings.Split(module, "/") {
		if elem == "" || strings.HasPrefix(elem, ".") || strings.HasSuffix(elem, ".") {
			return false
		}
		for _, r := range elem {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)) {
				return false
			}
		}
		short, _, _ := strings.Cut(elem, ".")
		upper := strings.ToUpper(short)
		if upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" || len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9' {
			return false
		}
		if tilde := strings.LastIndexByte(short, '~'); tilde >= 0 && tilde < len(short)-1 {
			if strings.Trim(short[tilde+1:], "0123456789") == "" {
				return false
			}
		}
	}
	return true
}
