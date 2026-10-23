package stem

import (
	"testing"

	"github.com/hyperlab-sh/husk/internal/stem/hooks"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The runner's transform kind is built separately; the mapping of a
// transform outcome is checked here on a constructed one.
func TestHookOutcomeOfTransform(t *testing.T) {
	o := hookOutcomeOf(hooks.Outcome{
		Kind: hooks.Transform,
		Data: map[string]any{"path": "/x", "viewer": "md"},
		Results: []hooks.HookResult{
			{Plugin: "b", Data: map[string]any{"path": "/x", "viewer": "md"}},
			{Plugin: "c"},
			{Plugin: "d", Data: map[string]any{}},
			{Plugin: "e", Err: &hooks.HookError{Code: protocol.CodeCancelled, Message: "gone", Data: map[string]any{"reason": "plugin_exited"}}},
		},
	})
	if o.Kind != "transform" || string(o.Data) != `{"path":"/x","viewer":"md"}` {
		t.Fatalf("outcome %#v (data %s)", o, o.Data)
	}
	if string(o.Results[0].Data) != `{"path":"/x","viewer":"md"}` {
		t.Fatalf("replacing entry data %s", o.Results[0].Data)
	}
	if o.Results[1].Data != nil {
		t.Fatalf("pass-through entry carries data %s", o.Results[1].Data)
	}
	if o.Results[2].Data == nil || string(o.Results[2].Data) != `{}` {
		t.Fatalf("empty replacement lost: %q", o.Results[2].Data)
	}
	if err := o.Results[3].Err; err == nil || err.Code() != "cancelled" {
		t.Fatalf("leg error %v", err)
	}
}

func TestHookOutcomeOfGateAndCollectFields(t *testing.T) {
	g := hookOutcomeOf(hooks.Outcome{Kind: hooks.Gate, Allowed: false,
		Vetoes:  []hooks.Veto{{Plugin: "b", Reason: "r"}},
		Results: []hooks.HookResult{{Plugin: "b", Veto: true, Reason: "r"}}})
	if g.Kind != "gate" || g.Allowed || len(g.Vetoes) != 1 || g.Vetoes[0].Plugin != "b" || !g.Results[0].Veto || g.Results[0].Reason != "r" {
		t.Fatalf("gate %#v", g)
	}
	c := hookOutcomeOf(hooks.Outcome{Kind: hooks.Collect, Allowed: true,
		Results: []hooks.HookResult{{Plugin: "b", Items: []any{map[string]any{"label": "L"}}}, {Plugin: "c"}}})
	if c.Kind != "collect" || string(c.Results[0].Items) != `[{"label":"L"}]` || c.Results[1].Items != nil {
		t.Fatalf("collect %#v", c)
	}
}
