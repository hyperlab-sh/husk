package eventbus_test

import (
	"testing"

	"github.com/hyperlab-sh/husk/internal/stem/eventbus"
)

func TestCLIOnlyPattern(t *testing.T) {
	for p, want := range map[string]bool{
		"secrets.*": true, "secrets.read": true, "services.*": true, "services.reloaded": true,
		"notes.*": false, "secretsx.read": false, "servicesy.*": false, "state.changed": false, "": false,
	} {
		if got := eventbus.CLIOnlyPattern(p); got != want {
			t.Errorf("CLIOnlyPattern(%q) = %v, want %v", p, got, want)
		}
	}
}
