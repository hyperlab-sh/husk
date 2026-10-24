package upstream

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SocketOptions are settings and environment values captured by the shim.
// ConfigPath is HERDR_CONFIG_PATH (a config file, not a directory); SocketPath
// is inherited HERDR_SOCKET_PATH. Home is the user's home directory.
type SocketOptions struct{ Session, SocketPath, ConfigPath, Home string }

// ResolveSocket follows the adapter attach contract: explicit session, then
// inherited socket, then the default socket beside the configuration file.
// Session names cannot escape the session directory. The special default
// session uses the configuration directory directly, just as the CLI does.
func ResolveSocket(o SocketOptions) (string, error) {
	if o.Session == "" && o.SocketPath != "" {
		return o.SocketPath, nil
	}
	var dir string
	if o.ConfigPath != "" {
		dir = filepath.Dir(o.ConfigPath)
	} else {
		if o.Home == "" {
			return "", fmt.Errorf("home directory is required")
		}
		dir = filepath.Join(o.Home, ".config", "herdr")
	}
	if o.Session == "" || o.Session == "default" {
		return filepath.Join(dir, "herdr.sock"), nil
	}
	if o.Session == "." || o.Session == ".." || strings.IndexFunc(o.Session, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
	}) >= 0 {
		return "", fmt.Errorf("invalid session name %q", o.Session)
	}
	return filepath.Join(dir, "sessions", o.Session, "herdr.sock"), nil
}
