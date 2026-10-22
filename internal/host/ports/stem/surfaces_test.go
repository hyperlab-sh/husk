package stem

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/surfaces"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func newPort(t *testing.T) (*SurfacePort, *state.State) {
	t.Helper()
	st := state.New(state.Options{})
	occ, err := st.Register(state.Spec{
		Namespace: "alpha.files.occupants", Owner: "alpha.files", Scope: state.ScopeInstance,
	})
	if err != nil {
		t.Fatalf("register occupants: %v", err)
	}
	if _, err := occ.Set(json.RawMessage(`{"main-stage":{"surface":"main-stage","plugin":"alpha.files"}}`)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	eng, err := surfaces.New(st, surfaces.Options{})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	st.Flush()
	eng.Flush()
	return NewSurfacePort(eng), st
}

func TestCallerKindsEnforced(t *testing.T) {
	port, _ := newPort(t)
	ctx := context.Background()
	plugin := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "alpha.files"}
	other := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "beta.notes"}
	cli := contracts.Caller{Kind: contracts.CallerCLI}
	stem := contracts.Caller{Kind: contracts.CallerStem}

	update := protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage}
	closeP := protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage}
	contribute := protocol.SlotContributeParams{SlotID: "s", ID: "alpha.files.x"}
	toast := protocol.ToastShowParams{Level: "info", Title: "t"}

	if err := port.SurfaceUpdate(ctx, plugin, update); err != nil {
		t.Fatalf("plugin update: %v", err)
	}
	if err := port.SurfaceUpdate(ctx, other, update); contracts.CodeOf(err) != "denied" {
		t.Fatalf("non-occupant update: %v", err)
	}
	for name, err := range map[string]error{
		"cli update":     port.SurfaceUpdate(ctx, cli, update),
		"cli close":      port.SurfaceClose(ctx, cli, closeP),
		"cli contribute": port.SlotContribute(ctx, cli, contribute),
		"stem update":    port.SurfaceUpdate(ctx, stem, update),
	} {
		if code := contracts.CodeOf(err); code != "denied" {
			t.Fatalf("%s: code = %q, want denied", name, code)
		}
	}
	if err := port.SurfaceClose(ctx, plugin, closeP); err != nil {
		t.Fatalf("plugin close: %v", err)
	}
	if code := contracts.CodeOf(port.SlotContribute(ctx, plugin, contribute)); code != "not_found" {
		t.Fatalf("plugin contribute code = %q, want not_found (no catalog in this fixture)", code)
	}
	if err := port.ToastShow(ctx, plugin, toast); err != nil {
		t.Fatalf("plugin toast: %v", err)
	}
	if err := port.ToastShow(ctx, cli, toast); err != nil {
		t.Fatalf("CLI toast: %v", err)
	}
}

func TestErrorsAreCoded(t *testing.T) {
	port, _ := newPort(t)
	ctx := context.Background()
	other := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "beta.notes"}

	err := port.SurfaceUpdate(ctx, other, protocol.SurfaceUpdateParams{
		Surface: protocol.SurfaceMainStage, PluginID: "alpha.files", // spoofed: ignored
	})
	if err == nil {
		t.Fatal("expected denial")
	}
	coded, ok := err.(contracts.CodedError)
	if !ok {
		t.Fatalf("error %v is not a CodedError", err)
	}
	if contracts.CodeOf(err) != "denied" {
		t.Fatalf("code = %q", contracts.CodeOf(err))
	}
	if coded.Error() == "" {
		t.Fatal("empty message")
	}

	if code := contracts.CodeOf(port.SurfaceUpdate(ctx, other, protocol.SurfaceUpdateParams{Surface: "bogus"})); code != "invalid_params" {
		t.Fatalf("invalid surface code = %q, want invalid_params", code)
	}
}

func TestToastAttributionFromCaller(t *testing.T) {
	port, _ := newPort(t)
	cli := contracts.Caller{Kind: contracts.CallerCLI}
	if err := port.ToastShow(context.Background(), cli, protocol.ToastShowParams{
		Level: "info", Title: "from CLI", PluginID: "spoofed",
	}); err != nil {
		t.Fatalf("CLI toast: %v", err)
	}
	active, ok := port.engine.ActiveToast()
	if !ok || active.Title != "from CLI" || active.Plugin != "" {
		t.Fatalf("active = %+v, want host-attributed toast", active)
	}
}
