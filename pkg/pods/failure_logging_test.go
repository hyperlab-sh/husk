package pods

import (
	"reflect"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
)

func TestExitLogPreservesFailureCause(t *testing.T) {
	for _, tc := range []struct {
		name, cause, exitStatus string
		status                  wrapper.Liveness
		level, message          string
	}{
		{"queue overflow", "stopped reading: protocol: send queue full (1024 messages)", "signal: killed", wrapper.Failed, "warn", "plugin p failed: stopped reading: protocol: send queue full (1024 messages) (signal: killed); stderr log: /logs/p.log"},
		{"write timeout", "stopped reading: protocol: write timed out after 5s", "signal: killed", wrapper.Failed, "warn", "plugin p failed: stopped reading: protocol: write timed out after 5s (signal: killed); stderr log: /logs/p.log"},
		{"crash", "exit status 7", "exit status 7", wrapper.Failed, "warn", "plugin p failed: exit status 7 (exit status 7); stderr log: /logs/p.log"},
		{"failure without cause", "", "signal: killed", wrapper.Failed, "warn", "plugin p exited (signal: killed); stderr log: /logs/p.log"},
		{"clean exit", "", "exit status 0", wrapper.Exited, "info", "plugin p exited (exit status 0); stderr log: /logs/p.log"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, loop, _, _ := hostFixture(t)
			p := addPod(t, h, "p")
			prepareHost(t, h)
			h.Handle(ExitMsg{Info: wrapper.Info{PluginID: "p", Status: tc.status, LastError: tc.cause, ExitStatus: tc.exitStatus, LogPath: "/logs/p.log"}})
			want := []LogMsg{{PluginID: "p", Level: tc.level, Message: tc.message}}
			if !reflect.DeepEqual(loop.logs, want) {
				t.Errorf("logs = %+v, want exactly %+v", loop.logs, want)
			}
			if got := p.Info().LastError; got != tc.cause {
				t.Errorf("last_error = %q, want %q", got, tc.cause)
			}
		})
	}
}
