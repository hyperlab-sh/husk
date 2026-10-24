package shim

import (
	"path/filepath"
	"testing"
)

func TestStartLockRequiresAnAbsoluteSharedStateDirectory(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		name, home, state string
		ok                bool
	}{
		{"no_home", "", "", false},
		{"relative_home", "relative", "", false},
		{"relative_state", home, "relative", false},
		{"home", home, "", true},
		{"state", "", home, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := startStateDirectory(map[string]string{"HOME": tc.home, "XDG_STATE_HOME": tc.state})
			if tc.ok {
				if err != nil || !filepath.IsAbs(dir) {
					t.Fatalf("shared lock directory=%q err=%v", dir, err)
				}
			} else if err == nil || dir != "" {
				t.Fatalf("relative lock directory accepted: %q err=%v", dir, err)
			}
		})
	}
}
