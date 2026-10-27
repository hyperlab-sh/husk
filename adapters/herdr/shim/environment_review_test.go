package shim

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestActionLinkContextDoesNotExportHandlerEnvironment(t *testing.T) {
	r := newRig(t, true, "")
	r.o.Env = append(r.o.Env, "HERDR_PLUGIN_CLICKED_URL=stale", "HERDR_PLUGIN_LINK_HANDLER_ID=stale")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "")
	id := protocol.NewStringID("context")
	params := json.RawMessage(`{"action":"demo.go","args":{"context":{"clicked_url":"https://example.test/file","link_handler_id":"legacy"}}}`)
	if err := protocol.NewWriter(r.in).Send(protocol.Message{ID: &id, Method: protocol.MethodActionInvoke, Params: params}); err != nil {
		t.Fatal(err)
	}
	var reply protocol.Message
	if err := r.reply.Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	spawned := take(t, r.spawns)
	defer func() { spawned.process.done <- 0 }()
	env := envMap(spawned.command.Env)
	for _, key := range []string{"HERDR_PLUGIN_CLICKED_URL", "HERDR_PLUGIN_LINK_HANDLER_ID"} {
		if _, ok := env[key]; ok {
			t.Fatalf("removed handler environment exported: %s", key)
		}
	}
	var context InvocationContext
	if err := json.Unmarshal([]byte(env["HERDR_PLUGIN_CONTEXT_JSON"]), &context); err != nil {
		t.Fatal(err)
	}
	if context.ClickedURL != "https://example.test/file" || context.LinkHandlerID != "legacy" {
		t.Fatalf("schema context lost: %+v", context)
	}
}
