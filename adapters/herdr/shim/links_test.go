package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/proxy"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// guardedDial makes every link test fail closed before any non-fixture dial.
func guardedDial(t *testing.T, allowed string) upstream.Dialer {
	t.Helper()
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "unix" || address != allowed {
			t.Errorf("non-fixture upstream dial: %s %q", network, address)
			return nil, fmt.Errorf("non-fixture upstream dial")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
}

type linkLog struct {
	*lockedBuffer
	records chan map[string]any
}

func (l *linkLog) Write(p []byte) (int, error) {
	var record map[string]any
	_ = json.Unmarshal(p, &record)
	l.records <- record
	return l.lockedBuffer.Write(p)
}

func TestTerminalLinkHandlers(t *testing.T) {
	// regex::Regex::is_match in upstream plugins/mod.rs at the pinned revision
	// is a substring search, with anchors and inline flags controlled by the
	// manifest, not a glob, full-string match, or case-folded URL comparison.
	for _, tc := range []struct {
		name, pattern, target, platform, handlerPlatforms, actionPlatforms, want string
		foreign, missing                                                         bool
	}{
		{name: "substring", pattern: `example\.test`, target: "https://example.test/path", want: "first"},
		{name: "anchors", pattern: `^example\.test$`, target: "https://example.test/path", want: "opener"},
		{name: "case sensitive", pattern: `EXAMPLE`, target: "https://example.test", want: "opener"},
		{name: "inline flags", pattern: `(?i)EXAMPLE`, target: "https://example.test", want: "first"},
		{name: "alternation and classes", pattern: `/(issue|pull)/[0-9]+$`, target: "https://example.test/issue/42", want: "first"},
		{name: "manifest order", pattern: `example`, target: "https://example.test", want: "first"},
		{name: "custom scheme handler", pattern: `^custom:`, target: "custom://item", want: "first"},
		{name: "handler platform", pattern: `example`, target: "https://example.test", handlerPlatforms: "windows", want: "second"},
		{name: "action platform", pattern: `example`, target: "https://example.test", actionPlatforms: "windows", want: "second"},
		{name: "missing action", pattern: `example`, target: "https://example.test", missing: true, want: "second"},
		{name: "unsupported pattern", pattern: `(?=example)`, target: "https://example.test", want: "second"},
		{name: "http fallback", pattern: `never`, target: "http://other.test/path?a=1&b=2", want: "opener"},
		{name: "darwin fallback", pattern: `never`, target: "https://other.test", platform: "darwin", want: "opener"},
		{name: "ignore custom scheme", pattern: `never`, target: "custom://other", want: "ignore"},
		{name: "foreign surface", pattern: `example`, target: "https://example.test", foreign: true, want: "ignore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newRig(t, "never", false, "")
			o := rig.o
			o.Client = upstream.New(upstream.Options{Socket: o.Socket, Dial: guardedDial(t, o.Socket), Clock: o.Clock})
			if tc.platform != "" {
				o.Platform = tc.platform
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p, err := proxy.Start(ctx, proxy.Options{Dial: o.Client.Dial, Clock: o.Clock})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			data, err := os.ReadFile(filepath.Join(o.Dir, "herdr-plugin.toml"))
			if err != nil {
				t.Fatal(err)
			}
			m, err := manifest.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			m.Actions = []manifest.Entry{{ID: "go", Title: "Go", Command: []string{"first"}}, {ID: "other", Title: "Other", Command: []string{"second"}}}
			if tc.actionPlatforms != "" {
				m.Actions[0].Platforms = []string{tc.actionPlatforms}
			}
			laterPattern := `never-second`
			if tc.want == "second" || tc.name == "manifest order" {
				laterPattern = `example`
			}
			m.LinkHandlers = []manifest.Entry{{ID: "original:handler", Pattern: tc.pattern, Action: "go"}, {ID: "later", Pattern: laterPattern, Action: "other"}}
			if tc.handlerPlatforms != "" {
				m.LinkHandlers[0].Platforms = []string{tc.handlerPlatforms}
			}
			if tc.missing {
				m.LinkHandlers[0].Action = "absent"
			}
			opens := make(chan Command, 2)
			opener := &testProcess{pid: 900, done: make(chan int, 1)}
			o.SpawnOpener = func(c Command) (Process, error) { opens <- c; return opener, nil }
			logs := &linkLog{lockedBuffer: rig.logs, records: make(chan map[string]any, 16)}
			r := &runner{o: o, m: m, id: "demo", ctx: ctx, cancel: cancel, log: logs, proxy: p, processes: map[Process]bool{}, panes: map[protocol.SurfaceID]*pane{}, state: state{Surfaces: map[string]any{}}}
			surface := protocol.SurfaceID("demo.owned")
			r.panes[surface] = &pane{id: surface, process: &paneProcess{}, ctx: ctx}
			if tc.foreign {
				surface = "other.foreign"
			}
			raw, _ := json.Marshal(protocol.TerminalLinkParams{Surface: surface, URL: tc.target})
			r.linkNotification(raw)
			r.workers.Wait()
			switch tc.want {
			case "first", "second":
				spawn := take(t, rig.spawns)
				if !reflect.DeepEqual(spawn.command.Argv, []string{tc.want}) {
					t.Fatalf("argv=%v", spawn.command.Argv)
				}
				env := envMap(spawn.command.Env)
				handler, action := "original:handler", "go"
				if tc.want == "second" {
					handler, action = "later", "other"
				}
				want := InvocationContext{WorkspaceID: "w", WorkspaceLabel: "Project", WorkspaceCwd: "/project", TabID: "t", TabLabel: "Main", FocusedPaneID: "p", FocusedPaneCwd: "/project", FocusedPaneAgent: "worker", FocusedPaneStatus: "working", InvocationSource: "link_click", CorrelationID: "link_click", ClickedURL: tc.target, LinkHandlerID: handler}
				var got InvocationContext
				if err := json.Unmarshal([]byte(env["HERDR_PLUGIN_CONTEXT_JSON"]), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("context=%+v want=%+v", got, want)
				}
				if env["HERDR_PLUGIN_CLICKED_URL"] != tc.target || env["HERDR_PLUGIN_LINK_HANDLER_ID"] != handler || env["HERDR_PLUGIN_ACTION_ID"] != action || env["HUSK_PLUGIN_TOKEN"] != "" {
					t.Fatalf("link environment differs: %v", env)
				}
				spawn.process.done <- 0
				r.children.Wait()
			case "opener":
				command := take(t, opens)
				binary := "xdg-open"
				if o.Platform == "darwin" {
					binary = "open"
				}
				if !reflect.DeepEqual(command.Argv, []string{binary, tc.target}) {
					t.Fatalf("opener=%v", command.Argv)
				}
				// No join: shutdown returns even while the detached opener waits.
				r.stopCommands()
				opener.done <- 7
				record := take(t, logs.records)
				if record["status"] != float64(7) || record["stderr"] != "last stderr" {
					t.Fatalf("opener completion not logged: %v", record)
				}
			case "ignore":
				if !tc.foreign && strings.Count(rig.logs.String(), `"level":"debug"`) != 1 {
					t.Fatal("expected one debug ignore log")
				}
				if tc.foreign && rig.logs.String() != "" {
					t.Fatal("foreign surface was not ignored silently")
				}
			}
			select {
			case extra := <-rig.spawns:
				t.Fatalf("unexpected action: %v", extra.command.Argv)
			default:
			}
			select {
			case extra := <-opens:
				t.Fatalf("unexpected opener: %v", extra.Argv)
			default:
			}
		})
	}
}
