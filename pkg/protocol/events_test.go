package protocol

import "testing"

func TestCLIOnlyEventPattern(t *testing.T) {
	for p, want := range map[string]bool{
		"secrets.*": true, "secrets.read": true, "services.*": true, "services.reloaded": true,
		"notes.*": false, "secretsx.read": false, "servicesy.*": false, "state.changed": false, "": false,
	} {
		if got := CLIOnlyEventPattern(p); got != want {
			t.Errorf("CLIOnlyEventPattern(%q) = %v, want %v", p, got, want)
		}
	}
}
