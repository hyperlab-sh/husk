package shim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PluginPathComponent matches plugin_paths.rs at the pinned upstream revision:
// percent-escaped bytes, reserved Windows stems, trailing dot, 120-byte cap.
func PluginPathComponent(id string) string {
	var b strings.Builder
	for _, c := range []byte(id) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	s := b.String()
	if strings.HasSuffix(s, ".") {
		s = strings.TrimSuffix(s, ".") + "%2E"
	}
	if s == "" {
		return "%plugin"
	}
	stem := strings.ToUpper(strings.SplitN(s, ".", 2)[0])
	reserved := stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL"
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		reserved = true
	}
	if reserved {
		s = "%" + s
	}
	if len(s) > 120 {
		hash := sha256.Sum256([]byte(id))
		suffix := hex.EncodeToString(hash[:6])
		s = s[:120-len(suffix)-1] + "-" + suffix
	}
	return s
}

// StateDir follows config/io.rs state_dir and platform_state_dir at revision
// 5608de0608918aba2f9d501e7793b984a0ca9cfa, including set-empty XDG_STATE_HOME.
// It uses LookupEnv rather than Getenv because upstream distinguishes absence.
func StateDir(platform string, lookup func(string) (string, bool), temp string) string {
	if v, ok := lookup("XDG_STATE_HOME"); ok {
		return filepath.Join(v, "herdr")
	}
	if platform == "windows" {
		if v, ok := lookup("LOCALAPPDATA"); ok {
			return filepath.Join(v, "herdr")
		}
		if v, ok := lookup("USERPROFILE"); ok {
			return filepath.Join(v, "AppData", "Local", "herdr")
		}
	}
	if v, ok := lookup("HOME"); ok {
		return filepath.Join(v, ".local", "state", "herdr")
	}
	return filepath.Join(temp, "herdr-state")
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, v := range env {
		if k, val, ok := strings.Cut(v, "="); ok {
			m[k] = val
		}
	}
	return m
}

// CommandEnvironment scrubs credentials and stale invocation variables before
// adding the upstream environment table. A command is not a Husk plugin.
func CommandEnvironment(base []string, dir, binary, socket, configDir, stateDir, id, action string, c InvocationContext) []string {
	m := envMap(base)
	for k := range m {
		if strings.HasPrefix(k, "HUSK_") && k != "HUSK_SOCKET_PATH" && k != "HUSK_INSTANCE" && k != "HUSK_BIN_PATH" {
			delete(m, k)
		}
		if strings.HasPrefix(k, "HERDR_PLUGIN_") || k == "HERDR_WORKSPACE_ID" || k == "HERDR_TAB_ID" || k == "HERDR_PANE_ID" {
			delete(m, k)
		}
	}
	raw, _ := json.Marshal(c)
	values := map[string]string{"HERDR_SOCKET_PATH": socket, "HERDR_BIN_PATH": binary, "HERDR_ENV": "1", "HERDR_PLUGIN_ID": id, "HERDR_PLUGIN_ROOT": dir, "HERDR_PLUGIN_CONFIG_DIR": configDir, "HERDR_PLUGIN_STATE_DIR": stateDir, "HERDR_PLUGIN_CONTEXT_JSON": string(raw), "HERDR_PLUGIN_ACTION_ID": action, "HERDR_WORKSPACE_ID": c.WorkspaceID, "HERDR_TAB_ID": c.TabID, "HERDR_PANE_ID": c.FocusedPaneID}
	for k, v := range values {
		if v != "" {
			m[k] = v
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(m))
	for _, k := range keys {
		result = append(result, k+"="+m[k])
	}
	return result
}

func ensureDirs(paths ...string) error {
	for _, p := range paths {
		if err := os.MkdirAll(p, 0700); err != nil {
			return err
		}
	}
	return nil
}
