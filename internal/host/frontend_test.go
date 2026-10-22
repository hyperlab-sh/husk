package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/internal/stem/dispatcher"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// recNotifier records the notifications a watcher receives.
type recNotifier struct {
	mu   sync.Mutex
	msgs []protocol.Message
}

func (n *recNotifier) Notify(m protocol.Message) error {
	n.mu.Lock()
	n.msgs = append(n.msgs, m)
	n.mu.Unlock()
	return nil
}

// resized returns every surface.resized the notifier received, in order.
func (n *recNotifier) resized(t *testing.T) [][]protocol.SurfaceSize {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	var out [][]protocol.SurfaceSize
	for _, m := range n.msgs {
		if m.Method != protocol.MethodSurfaceResized {
			continue
		}
		var p protocol.SurfaceResizedParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p.Surfaces)
	}
	return out
}

// bootFrontend boots a host with one launched plugin "alpha" (claims
// sidebar-list) and returns it with the frontend.
func bootFrontend(t *testing.T, mutate func(*contracts.Config)) (*harness, *fakes, contracts.Frontend) {
	t.Helper()
	hook, ready := readyHook("")
	f := newFakes()
	h, err := boot(t, func(c *contracts.Config) {
		c.Plugins = []string{writePlugin(t, "alpha", false)}
		if mutate != nil {
			mutate(c)
		}
	}, withHook(hook), WithFactory(f.factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	return h, f, h.root.Frontend()
}

func geometryDoc(w, hgt int) json.RawMessage {
	return json.RawMessage(`{"terminal":{"width":160,"height":48},"too_small":false,"rects":{"sidebar-list":{"x":0,"y":0,"width":` +
		itoa(w+2) + `,"height":` + itoa(hgt+2) + `,"inner":{"x":1,"y":1,"width":` + itoa(w) + `,"height":` + itoa(hgt) + `}}}}`)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestNewReturnsAHostWhoseFrontendIsTheRoot(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	var host contracts.Host = h.root
	if host.Frontend() == nil {
		t.Fatal("Host.Frontend() is nil")
	}
	if host.Frontend() != contracts.Frontend(h.root) {
		t.Fatal("Host.Frontend() is not the root")
	}
}

func TestFrontendReadsStateThemeAndKeys(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	h.loop(func() {
		res, ok := decodeSurfaces(fe.Snapshot())
		if !ok || res.Occupants["sidebar-list"].Plugin != "alpha" {
			t.Errorf("snapshot occupants = %+v", res.Occupants)
		}
	})
	if got := fe.Palette(); got != h.root.rt.Theme.Wire() {
		t.Errorf("Palette = %+v, want the Theme service's palette", got)
	}
	// The embedded husk keymap binds quit; KeysFor and Resolve agree on it.
	keys := fe.KeysFor("husk.quit")
	if len(keys) == 0 {
		t.Fatal("KeysFor(husk.quit) is empty")
	}
	res := fe.Resolve(contracts.KeyContext{}, []string{keys[0]})
	if res.Outcome != contracts.KeyMatch || res.Binding == nil || res.Binding.Action != "husk.quit" {
		t.Fatalf("Resolve(%q) = %+v", keys[0], res)
	}
	if fe.Resolve(contracts.KeyContext{}, []string{"ctrl+alt+shift+f19"}).Outcome != contracts.KeyNoMatch {
		t.Error("an unbound key must be NoMatch")
	}
	if len(fe.Hints(contracts.KeyContext{})) > 4 {
		t.Error("Hints returns at most four")
	}
}

func TestInvokeActionRunsHostActionsAsTheStem(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	type outcome struct {
		res map[string]any
		err error
	}
	invoke := func(id string, args map[string]any) outcome {
		var o outcome
		replies := 0
		fe.InvokeAction(id, args, func(res map[string]any, err error) { replies++; o = outcome{res, err} })
		if replies != 1 {
			t.Fatalf("%s: reply called %d times, want exactly once", id, replies)
		}
		return o
	}
	h.loop(func() {
		// Before registration the id is not in the catalog.
		if o := invoke("husk.test.ping", nil); codeOf(o.err) != string(protocol.CodeNotFound) {
			t.Errorf("unregistered: err = %v, want not_found", o.err)
		}
		var gotArgs map[string]any
		fe.RegisterHostAction("husk.test.ping", func(args map[string]any) (map[string]any, error) {
			gotArgs = args
			return map[string]any{"pong": true}, nil
		})
		fe.RegisterHostAction("alpha.panel", func(map[string]any) (map[string]any, error) { return nil, nil })
		fe.RegisterHostAction("husk.test.deny", func(map[string]any) (map[string]any, error) {
			return nil, contracts.NewError(protocol.CodeDenied, "nope", map[string]any{"why": "test"})
		})
		fe.RegisterHostAction("husk.test.boom", func(map[string]any) (map[string]any, error) { return nil, errors.New("plain") })

		o := invoke("husk.test.ping", map[string]any{"n": "1"})
		if o.err != nil || o.res["pong"] != true || gotArgs["n"] != "1" {
			t.Errorf("ping: %+v args=%v", o, gotArgs)
		}
		if o := invoke("alpha.panel", nil); o.err != nil || o.res == nil {
			t.Errorf("a rail item id as host action: %+v (a nil result must become an empty object)", o)
		}
		o = invoke("husk.test.deny", nil)
		var de contracts.DataError
		if codeOf(o.err) != string(protocol.CodeDenied) || !errors.As(o.err, &de) || de.Data()["why"] != "test" {
			t.Errorf("coded handler error lost: %v", o.err)
		}
		if o := invoke("husk.test.boom", nil); codeOf(o.err) != string(protocol.CodeInternal) {
			t.Errorf("plain handler error: %v, want internal", o.err)
		}
		// A plugin cannot invoke a host action; the Stem can.
		var denied *dispatcher.ActionError
		h.root.disp.Invoke(dispatcher.ActionRequest{Action: "husk.test.ping", Caller: dispatcher.Caller{Kind: dispatcher.CallerPlugin, PluginID: "alpha"},
			Reply: func(r dispatcher.ActionReply) { denied = r.Err }})
		if denied == nil || denied.Code != string(protocol.CodeDenied) {
			t.Errorf("a plugin invoking a host action: %+v, want denied", denied)
		}
		// Removal.
		fe.RegisterHostAction("husk.test.ping", nil)
		if o := invoke("husk.test.ping", nil); codeOf(o.err) != string(protocol.CodeNotFound) {
			t.Errorf("removed: err = %v, want not_found", o.err)
		}
	})
}

func codeOf(err error) string {
	var ce contracts.CodedError
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return ""
}

func TestSendDeliversToALivePodAndRefusesTheRest(t *testing.T) {
	h, f, fe := bootFrontend(t, nil)
	key := protocol.SurfaceKeyParams{Surface: "sidebar-list", Key: "ctrl+x", Modifiers: protocol.ModifierState{Ctrl: true}}
	mouse := protocol.SurfaceMouseParams{Surface: "sidebar-list", X: 3, Y: 4, Button: "left", HitID: "row1"}
	ks := protocol.KeyStateParams{Phase: protocol.KeyPhaseDown, Key: "shift", Held: true, Modifiers: protocol.ModifierState{Shift: true}}
	h.loop(func() {
		for name, err := range map[string]error{
			"SendKey": fe.SendKey("alpha", key), "SendMouse": fe.SendMouse("alpha", mouse), "SendKeyState": fe.SendKeyState("alpha", ks),
		} {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
	p := f.proc("alpha")
	p.mu.Lock()
	sent := slices.Clone(p.sent)
	p.mu.Unlock()
	methods := make([]string, len(sent))
	for i, m := range sent {
		methods[i] = m.Method
	}
	want := []string{protocol.MethodInputKey, protocol.MethodInputMouse, protocol.MethodInputKeyState}
	if !slices.Equal(methods, want) {
		t.Fatalf("delivered %v, want %v", methods, want)
	}
	var gotKey protocol.SurfaceKeyParams
	if err := json.Unmarshal(sent[0].Params, &gotKey); err != nil || gotKey.Key != "ctrl+x" || !gotKey.Modifiers.Ctrl {
		t.Errorf("input.key params = %s (%v)", sent[0].Params, err)
	}
	var gotMouse protocol.SurfaceMouseParams
	if err := json.Unmarshal(sent[1].Params, &gotMouse); err != nil || gotMouse.X != 3 || gotMouse.HitID != "row1" {
		t.Errorf("input.mouse params = %s (%v)", sent[1].Params, err)
	}

	code := codeOf
	h.loop(func() {
		if c := code(fe.SendKey("ghost", key)); c != string(protocol.CodeNotFound) {
			t.Errorf("unknown plugin: code %q, want not_found", c)
		}
		if c := code(fe.SendKey("alpha", protocol.SurfaceKeyParams{Surface: "nope", Key: "a"})); c != string(protocol.CodeInvalidParams) {
			t.Errorf("bad surface: code %q, want invalid_params", c)
		}
		if c := code(fe.SendMouse("alpha", protocol.SurfaceMouseParams{Surface: "nope"})); c != string(protocol.CodeInvalidParams) {
			t.Errorf("bad mouse surface: code %q, want invalid_params", c)
		}
		if c := code(fe.SendKeyState("alpha", protocol.KeyStateParams{Phase: "bogus", Key: "a"})); c != string(protocol.CodeInvalidParams) {
			t.Errorf("bad phase: code %q, want invalid_params", c)
		}
	})

	// A plugin whose process exited is dead: unavailable, nothing sent.
	h.root.cfg.Post(pods.ExitMsg{Info: wrapper.Info{PluginID: "alpha", Status: wrapper.Exited}})
	before := len(sent)
	h.loop(func() {
		if c := code(fe.SendKey("alpha", key)); c != string(protocol.CodeUnavailable) {
			t.Errorf("dead plugin: code %q, want unavailable", c)
		}
	})
	p.mu.Lock()
	after := len(p.sent)
	p.mu.Unlock()
	if after != before {
		t.Errorf("a dead plugin received %d messages", after-before)
	}
}

func TestOwnSlicesRegistersOnceAndGeometryDrivesResized(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	var slices contracts.FrontendSlices
	var n recNotifier
	h.loop(func() {
		var err error
		if slices, err = fe.OwnSlices(); err != nil {
			t.Fatal(err)
		}
		if _, err := fe.OwnSlices(); err == nil {
			t.Error("a second OwnSlices must fail")
		}
		for ns, hnd := range map[string]*state.Handle{state.LayoutNamespace: slices.Layout, state.GeometryNamespace: slices.Geometry, state.FocusNamespace: slices.Focus} {
			if hnd == nil || hnd.Namespace() != ns {
				t.Errorf("handle for %s = %v", ns, hnd)
			}
			e, ok := h.root.st.Snapshot().Slices[ns]
			if !ok || e.Owner != state.OwnerHusk {
				t.Errorf("%s not registered as husk-owned: %+v", ns, e)
			}
		}
		if e := h.root.st.Snapshot().Slices[state.LayoutNamespace]; !e.Persistent || e.Retention.Policy != state.KeepLatest {
			t.Errorf("husk.layout must be persistent keep-latest: %+v", e.Spec)
		}
		if e := h.root.st.Snapshot().Slices[state.GeometryNamespace]; e.Persistent {
			t.Error("husk.geometry must not be persistent")
		}
		h.root.engine.WatchPlugin("alpha", &n)
	})

	// The frontend's geometry write is what drives surface.resized.
	h.loop(func() {
		if _, err := slices.Geometry.Set(geometryDoc(30, 10)); err != nil {
			t.Fatal(err)
		}
	})
	h.loop(func() {}) // the tick after the write has run
	got := n.resized(t)
	if len(got) != 1 || len(got[0]) != 1 || got[0][0].Surface != "sidebar-list" || got[0][0].Width != 30 || got[0][0].Height != 10 {
		t.Fatalf("after the first geometry write: resized = %+v", got)
	}

	// An unrelated tick and an identical write push nothing.
	h.loop(func() {})
	h.loop(func() {
		if _, err := slices.Geometry.Set(geometryDoc(30, 10)); err != nil {
			t.Fatal(err)
		}
	})
	h.loop(func() {}) // the tick after the write has run
	if got := n.resized(t); len(got) != 1 {
		t.Fatalf("an unchanged geometry pushed again: %+v", got)
	}

	// A new size and a focus change each push once, with the new facts.
	h.loop(func() {
		if _, err := slices.Geometry.Set(geometryDoc(40, 12)); err != nil {
			t.Fatal(err)
		}
	})
	h.loop(func() {
		if _, err := slices.Focus.Set(json.RawMessage(`{"surface":"sidebar-list","terminal_focused":true}`)); err != nil {
			t.Fatal(err)
		}
	})
	h.loop(func() {}) // the tick after the write has run
	got = n.resized(t)
	if len(got) != 3 {
		t.Fatalf("resized pushes = %d, want 3: %+v", len(got), got)
	}
	if s := got[1][0]; s.Width != 40 || s.Height != 12 || s.Focused {
		t.Errorf("second push = %+v, want 40x12 unfocused", s)
	}
	if s := got[2][0]; s.Width != 40 || !s.Focused {
		t.Errorf("third push = %+v, want focused", s)
	}
}

func TestResizedReachesOnlyTheOccupant(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	var other recNotifier
	h.loop(func() {
		s, err := fe.OwnSlices()
		if err != nil {
			t.Fatal(err)
		}
		h.root.engine.WatchPlugin("beta", &other) // watches, occupies nothing
		if _, err := s.Geometry.Set(geometryDoc(30, 10)); err != nil {
			t.Fatal(err)
		}
	})
	if got := other.resized(t); len(got) != 0 {
		t.Fatalf("a non-occupant was told about sizes: %+v", got)
	}
}

func TestUpdateSurfaceViewWritesOnlyTheFrontendHalf(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	var n recNotifier
	h.loop(func() {
		h.root.engine.WatchPlugin("alpha", &n)
		err := fe.UpdateSurfaceView(func(v *contracts.SurfaceView) {
			v.Visible = []protocol.SurfaceID{"sidebar-list", "main-stage"}
			v.Placement = contracts.Placement{RailItem: "alpha.panel", Kind: "stage"}
			v.Overlays.Modal = contracts.ModalOverlay{Plugin: "alpha", RailItem: "alpha.panel"}
			v.Overlays.Palette = true
		})
		if err != nil {
			t.Fatal(err)
		}
		res, _ := decodeSurfaces(fe.Snapshot())
		if res.Occupants["sidebar-list"].Plugin != "alpha" {
			t.Errorf("the engine's occupants were lost: %+v", res.Occupants)
		}
		if !slices.Equal(res.Visible, []protocol.SurfaceID{"sidebar-list", "main-stage"}) || res.Placement.Kind != "stage" ||
			res.Overlays.Modal.Plugin != "alpha" || !res.Overlays.Palette {
			t.Errorf("view not written: %+v", res)
		}
		// Read-modify-write: the callback sees the current value.
		if err := fe.UpdateSurfaceView(func(v *contracts.SurfaceView) {
			if !v.Overlays.Palette {
				t.Error("callback did not see the committed overlays")
			}
			v.Overlays.Palette = false
		}); err != nil {
			t.Fatal(err)
		}
		// An invalid surface refuses the whole update; nothing commits.
		rev := fe.Snapshot().Slices[state.SurfacesNamespace].Revision
		if err := fe.UpdateSurfaceView(func(v *contracts.SurfaceView) {
			v.Visible = []protocol.SurfaceID{"not-a-surface"}
			v.Overlays.SlotExplorer = true
		}); err == nil {
			t.Error("an invalid visible surface was accepted")
		}
		if got := fe.Snapshot().Slices[state.SurfacesNamespace].Revision; got != rev {
			t.Errorf("a refused update committed: revision %d -> %d", rev, got)
		}
	})
	// Visibility reaches the occupant through surface.resized.
	h.loop(func() {}) // the tick after the write has run
	got := n.resized(t)
	if len(got) == 0 || !got[len(got)-1][0].Visible {
		t.Fatalf("resized after the view write = %+v, want sidebar-list visible", got)
	}
}

func TestRailItemsComeFromTheDeclaredCatalog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "railed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"manifest_version":2,"protocol":2,"id":"railed","name":"railed","version":"1","command":["./p"],
	  "surfaces":[{"id":"sidebar-list"}],
	  "rail_items":[
	    {"id":"railed.panel","icon":"P","label":"Panel","placement":"modal","badge":"3","description":"d","frame":{"border":true,"label":"Panel","close":true}},
	    {"id":"railed.full","icon":"F","label":"Full","placement":"full-canvas"}]}`
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	h, _, fe := bootFrontend(t, func(c *contracts.Config) { c.Plugins = append(c.Plugins, dir) })
	_ = h
	// Both plugins launch; wait for the second one to be declared.
	h.loop(func() {})
	var items []contracts.RailItem
	for _, it := range fe.RailItems() {
		if it.Plugin == "railed" {
			items = append(items, it)
		}
	}
	if len(items) != 2 {
		t.Fatalf("RailItems = %+v", fe.RailItems())
	}
	first := items[0]
	if first.ID != "railed.panel" || first.Placement != "modal" || first.Badge != "3" || first.Label != "Panel" || first.Icon != "P" ||
		first.Frame == nil || !first.Frame.Border || !first.Frame.Close || first.Frame.Label != "Panel" {
		t.Errorf("first rail item = %+v", first)
	}
	if items[1].ID != "railed.full" || items[1].Frame != nil {
		t.Errorf("second rail item = %+v", items[1])
	}
}

func TestConfigContentReceivesOnlyAcceptedSurfaceUpdates(t *testing.T) {
	type got struct {
		plugin string
		p      protocol.SurfaceUpdateParams
	}
	var mu sync.Mutex
	var seen []got
	h, _, _ := bootFrontend(t, func(c *contracts.Config) {
		c.Content = func(plugin string, p protocol.SurfaceUpdateParams) {
			mu.Lock()
			seen = append(seen, got{plugin, p})
			mu.Unlock()
		}
	})
	update := func(caller contracts.Caller, params protocol.SurfaceUpdateParams) protocol.Message {
		raw, _ := json.Marshal(params)
		id := protocol.NewStringID("u")
		return h.root.mux.Serve(t.Context(), caller, protocol.Message{ID: &id, Method: protocol.MethodSurfaceUpdate, Params: raw}, nil)
	}
	alpha := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "alpha"}

	ok := update(alpha, protocol.SurfaceUpdateParams{Surface: "sidebar-list", Title: "T", Lines: []string{"a", "b"}, HitTargets: []protocol.RowHitTarget{{}}})
	if ok.Error != nil {
		t.Fatalf("accepted update refused: %+v", ok.Error)
	}
	unoccupied := update(alpha, protocol.SurfaceUpdateParams{Surface: "main-stage", Lines: []string{"x"}})
	if unoccupied.Error == nil || unoccupied.Error.Code != protocol.CodeDenied {
		t.Fatalf("unoccupied update = %+v, want denied", unoccupied.Error)
	}
	beta := update(contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "beta"}, protocol.SurfaceUpdateParams{Surface: "sidebar-list", Lines: []string{"y"}})
	if beta.Error == nil || beta.Error.Code != protocol.CodeDenied {
		t.Fatalf("update of another plugin's surface = %+v, want denied", beta.Error)
	}
	cli := update(contracts.Caller{Kind: contracts.CallerCLI}, protocol.SurfaceUpdateParams{Surface: "sidebar-list", Lines: []string{"z"}})
	if cli.Error == nil {
		t.Fatal("a CLI caller's surface.update was accepted")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("sink calls = %d, want exactly the accepted one: %+v", len(seen), seen)
	}
	if seen[0].plugin != "alpha" || seen[0].p.Surface != "sidebar-list" || seen[0].p.Title != "T" ||
		!slices.Equal(seen[0].p.Lines, []string{"a", "b"}) || len(seen[0].p.HitTargets) != 1 {
		t.Errorf("sink got %+v, want the plugin id and the params unchanged", seen[0])
	}
}
