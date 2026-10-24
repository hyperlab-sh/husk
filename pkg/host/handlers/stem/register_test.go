package stem

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// TestRegisterRoutesEveryStemMethod runs one request per Stem-owned method
// family through a Mux built by Register and checks it reaches the matching
// fake.
func TestRegisterRoutesEveryStemMethod(t *testing.T) {
	rec := fakes.NewRecorder()
	ports := Ports{
		Session: fakes.NewSession(rec),
		Calls:   fakes.NewCalls(rec),
		Event:   fakes.NewEvents(rec),
		Hook:    fakes.NewHooks(rec),
		Surface: fakes.NewSurfaces(rec),
		State:   fakes.NewState(rec),
	}
	m := handlers.NewMux()
	Register(m, ports)

	cases := []struct {
		method string
		params string
		caller contracts.Caller
		wants  string
	}{
		{protocol.MethodSessionPing, ``, plugin, "Session.Ping"},
		{protocol.MethodActionInvoke, `{"action":"go"}`, plugin, "Calls.ActionInvoke"},
		{protocol.MethodActionList, ``, plugin, "Calls.ActionList"},
		{protocol.MethodEventEmit, `{"name":"tick"}`, plugin, "Events.Emit"},
		{protocol.MethodEventWatch, `{"pattern":"*"}`, plugin, "Events.Watch"},
		{protocol.MethodHookCall, `{"hook":"h"}`, plugin, "Hooks.HookCall"},
		{protocol.MethodHookList, ``, cli, "Hooks.HookList"},
		{protocol.MethodSurfaceFocus, `{"surface":"notes.screen"}`, plugin, "Surfaces.SurfaceFocus"},
		{protocol.MethodSurfaceOpen, `{"placement":"canvas","name":"screen"}`, plugin, "Surfaces.SurfaceOpen"},
		{protocol.MethodSurfaceUpdate, `{"surface":"modal-surface","lines":[]}`, plugin, "Surfaces.SurfaceUpdate"},
		{protocol.MethodSurfaceClose, `{"surface":"modal-surface"}`, plugin, "Surfaces.SurfaceClose"},
		{protocol.MethodSlotContribute, `{"slot_id":"s","id":"i","content":"c"}`, plugin, "Surfaces.SlotContribute"},
		{protocol.MethodToastShow, `{"level":"info","title":"t","message":"m"}`, plugin, "Surfaces.ToastShow"},
		{protocol.MethodStateGet, `{"slice":"s"}`, plugin, "State.Get"},
		{protocol.MethodStateSet, `{"slice":"s","value":{}}`, plugin, "State.Set"},
		{protocol.MethodStatePatch, `{"slice":"s","patch":{}}`, plugin, "State.Patch"},
		{protocol.MethodStateSnapshot, `{}`, cli, "State.Snapshot"},
		{protocol.MethodStateWatch, `{}`, plugin, "State.Watch"},
		{protocol.MethodStateClear, `{"namespace":"n"}`, cli, "State.Clear"},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			rec.Reset()
			reply := m.Serve(ctx, tc.caller, protocol.Message{Method: tc.method, Params: []byte(tc.params)}, nil)
			if reply.Error != nil {
				t.Fatalf("error = %+v", reply.Error)
			}
			methods := rec.Methods()
			if len(methods) != 1 || methods[0] != tc.wants {
				t.Fatalf("port calls = %v, want %s", methods, tc.wants)
			}
		})
	}
}

// Other units' prefixes stay unregistered: the Mux answers them.
func TestRegisterLeavesOtherPrefixesAlone(t *testing.T) {
	m := handlers.NewMux()
	Register(m, Ports{
		Session: fakes.NewSession(nil),
		Calls:   fakes.NewCalls(nil),
		Event:   fakes.NewEvents(nil),
		Hook:    fakes.NewHooks(nil),
		Surface: fakes.NewSurfaces(nil),
		State:   fakes.NewState(nil),
	})
	reply := m.Serve(ctx, cli, protocol.Message{Method: protocol.MethodSettingsList}, nil)
	if reply.Error == nil || reply.Error.Code != protocol.CodeUnknownMethod {
		t.Fatalf("reply = %+v", reply.Error)
	}
}
