package stem_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/ports/stem"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestTerminalLinkDelivery(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint("full=", full), func(t *testing.T) {
			_, eng, _, loop, _ := newPort(t)
			ctx := context.Background()
			defer func() { _ = loop.Do(ctx, func() { eng.CloseTerminals() }) }()
			var messages []protocol.Message
			var logs []string
			port := stem.TerminalPort{Engine: eng, Loop: loop,
				Size: func(string, protocol.SurfaceID) protocol.TerminalAttachResult {
					return protocol.TerminalAttachResult{Width: 40, Height: 3}
				},
				Sender: func(plugin string) func(protocol.Message) error {
					if plugin != "alpha.files" {
						t.Errorf("recipient = %s", plugin)
					}
					return func(m protocol.Message) error {
						messages = append(messages, m)
						if full {
							return protocol.ErrSendQueueFull
						}
						return nil
					}
				}, Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
			}
			caller := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "alpha.files"}
			id := protocol.SurfaceMainStage
			if _, err := port.TerminalAttach(ctx, caller, protocol.TerminalAttachParams{Surface: id}); err != nil {
				t.Fatal(err)
			}
			data := base64.StdEncoding.EncodeToString([]byte("\x1b[?1000h\x1b[?1006hhttps://example.test"))
			if err := port.TerminalWrite(ctx, caller, protocol.TerminalWriteParams{Surface: id, Data: data}); err != nil {
				t.Fatal(err)
			}
			_ = loop.Do(ctx, func() {
				m := uv.Mouse{X: 3, Y: 0, Button: uv.MouseLeft, Mod: uv.ModCtrl}
				eng.Terminal(id).Mouse(uv.MouseClickEvent(m))
				eng.Terminal(id).Mouse(uv.MouseReleaseEvent(m))
				eng.CloseTerminals()
			})
			if len(messages) != 1 || messages[0].Method != protocol.MethodTerminalLink {
				t.Fatalf("deliveries = %+v", messages)
			}
			var got protocol.TerminalLinkParams
			if err := json.Unmarshal(messages[0].Params, &got); err != nil {
				t.Fatal(err)
			}
			if got != (protocol.TerminalLinkParams{Surface: id, URL: "https://example.test", Row: 0, Col: 3}) {
				t.Fatalf("payload = %+v", got)
			}
			if full && (len(logs) != 1 || !strings.Contains(logs[0], "dropped link")) {
				t.Fatalf("drop logs = %v", logs)
			}
			if !full && len(logs) != 0 {
				t.Fatalf("unexpected logs = %v", logs)
			}
		})
	}
}
