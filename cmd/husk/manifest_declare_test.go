package main

import (
	"bytes"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
	"github.com/hyperlab-sh/husk/pkg/manifest"
)

func declaringManifest() *manifest.Plugin {
	mf := fileManifest("acme.p", "main-stage")
	mf.PublishedSlots = []huskipc.SlotDeclarePayload{
		{ID: "acme.p.rows", Title: "Rows", Description: "Row actions", Kind: "inline"},
		{ID: "acme.p.bar", Title: "Bar", Kind: "region", Rect: &huskipc.PublishedRect{Surface: huskipc.SlotMainStage, X: 0, Y: 1, Width: 20, Height: 2}},
	}
	mf.Hooks = []huskipc.HookDeclarePayload{{ID: "acme.p.before", Title: "Before", Description: "pre"}}
	mf.Events = []huskipc.EventDeclarePayload{{ID: "acme.p.changed", Title: "Changed"}}
	return mf
}

func TestManifestDeclarationsAreRegisteredBeforeConnect(t *testing.T) {
	r := newPublicationRegistry()
	if err := r.declareFromManifest(declaringManifest()); err != nil {
		t.Fatal(err)
	}
	rows := r.Slots["acme.p.rows"]
	if rows == nil || rows.Owner != "acme.p" || rows.Spec.Title != "Rows" || !rows.FromManifest {
		t.Fatalf("rows = %+v", rows)
	}
	if bar := r.Slots["acme.p.bar"]; bar == nil || bar.Spec.Rect == nil || bar.Spec.Rect.Width != 20 {
		t.Fatalf("bar = %+v", bar)
	}
	if h := r.Hooks["acme.p.before"]; h == nil || h.Owner != "acme.p" || h.Title != "Before" || h.Description != "pre" {
		t.Fatalf("hook = %+v", h)
	}
	if e := r.Events["acme.p.changed"]; e == nil || e.Owner != "acme.p" || e.Title != "Changed" {
		t.Fatalf("event = %+v", e)
	}
	// Nothing is claimed yet: the plugin has not connected.
	if got := r.Occupants[huskipc.SlotMainStage]; got != "" {
		t.Fatalf("surface occupied by %q before connect", got)
	}
}

func TestManifestDeclarationsAreValidated(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*manifest.Plugin)
		want   string
	}{
		{"foreign slot namespace", func(p *manifest.Plugin) { p.PublishedSlots[0].ID = "other.rows" }, `published_slots: id "other.rows" must use acme.p.<name>`},
		{"nested slot name", func(p *manifest.Plugin) { p.PublishedSlots[0].ID = "acme.p.a.b" }, "must use acme.p.<name>"},
		{"bad kind", func(p *manifest.Plugin) { p.PublishedSlots[0].Kind = "floating" }, "slot kind must be inline or region"},
		{"inline with rect", func(p *manifest.Plugin) {
			p.PublishedSlots[0].Rect = &huskipc.PublishedRect{Surface: huskipc.SlotMainStage, Width: 1, Height: 1}
		}, "inline slots use anchors"},
		{"region without rect", func(p *manifest.Plugin) { p.PublishedSlots[1].Rect = nil }, "region requires a positive rect"},
		{"region zero width", func(p *manifest.Plugin) { p.PublishedSlots[1].Rect.Width = 0 }, "region requires a positive rect"},
		{"region on surface the manifest does not claim", func(p *manifest.Plugin) { p.PublishedSlots[1].Rect.Surface = huskipc.SlotSidebarList }, `region surface "sidebar-list" is not held by acme.p`},
		{"region on unknown surface", func(p *manifest.Plugin) {
			p.Slots = append(p.Slots, "nowhere")
			p.PublishedSlots[1].Rect.Surface = "nowhere"
		}, `region surface "nowhere" is not held`},
		{"duplicate slot", func(p *manifest.Plugin) { p.PublishedSlots[1].ID = p.PublishedSlots[0].ID }, `slot "acme.p.rows" is already declared`},
		{"foreign hook", func(p *manifest.Plugin) { p.Hooks[0].ID = "other.before" }, "hooks: id \"other.before\" must use acme.p.<name>"},
		{"duplicate hook", func(p *manifest.Plugin) { p.Hooks = append(p.Hooks, p.Hooks[0]) }, `hooks: id "acme.p.before" is already declared`},
		{"event with space", func(p *manifest.Plugin) { p.Events[0].ID = "acme.p.a b" }, "events: id"},
		{"empty event id", func(p *manifest.Plugin) { p.Events[0].ID = "" }, "events: id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPublicationRegistry()
			baseline := len(r.Slots)
			mf := declaringManifest()
			tc.mutate(mf)
			err := r.declareFromManifest(mf)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			// Atomic: a failure leaves nothing from this manifest behind.
			if len(r.Slots) != baseline || len(r.Hooks) != 0 || len(r.Events) != 0 {
				t.Fatalf("partial registration: %d slots (baseline %d), %d hooks, %d events", len(r.Slots), baseline, len(r.Hooks), len(r.Events))
			}
		})
	}
}

func TestLaunchPluginRejectsInvalidDeclarationsWithoutSpawning(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	mf := declaringManifest()
	mf.Hooks[0].ID = "other.before"
	err := m.launchPlugin(mf)
	if err == nil || !strings.Contains(err.Error(), manifest.FileName) || !strings.Contains(err.Error(), "other.before") {
		t.Fatalf("err = %v", err)
	}
	if len(m.procs) != 0 {
		t.Fatalf("spawned %d processes", len(m.procs))
	}
	if m.manifests["acme.p"] != nil || m.registry.Slots["acme.p.rows"] != nil {
		t.Fatal("invalid manifest left state behind")
	}
}

func connectDeclaring(t *testing.T, m *WorkbenchModel, hello string) (io.Closer, *ConnectedPlugin) {
	t.Helper()
	peer, _ := connectPipePlugin(t, m, hello)
	go func() { _, _ = io.Copy(io.Discard, peer) }()
	return peer, m.plugins["acme.p"]
}

func TestHelloMayRepeatManifestDeclarations(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	mf := declaringManifest()
	m.manifests[mf.ID] = mf
	if err := m.registry.declareFromManifest(mf); err != nil {
		t.Fatal(err)
	}
	hello := `{"type":"hello","hello":{"plugin_id":"acme.p","slots":["main-stage"],` +
		`"published_slots":[{"id":"acme.p.rows","title":"Rows","kind":"inline"},{"id":"acme.p.bar","kind":"region","rect":{"surface":"main-stage","x":0,"y":1,"width":20,"height":2}}],` +
		`"hooks":[{"id":"acme.p.before"}],"events":[{"id":"acme.p.changed","title":"Changed"}]}}` + "\n"
	peer, cp := connectDeclaring(t, m, hello)
	defer peer.Close()
	if cp == nil {
		t.Fatal("hello repeating the manifest was not registered")
	}
	// The manifest's richer metadata survives an under-specified hello.
	if got := m.registry.Slots["acme.p.rows"].Spec.Description; got != "Row actions" {
		t.Fatalf("description = %q", got)
	}
	if got := m.registry.Hooks["acme.p.before"].Title; got != "Before" {
		t.Fatalf("hook title = %q", got)
	}
	if got := m.registry.Occupants[huskipc.SlotMainStage]; got != "acme.p" {
		t.Fatalf("occupant = %q", got)
	}
}

func TestHelloWithoutDeclarationsKeepsManifestOnes(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	mf := declaringManifest()
	m.manifests[mf.ID] = mf
	if err := m.registry.declareFromManifest(mf); err != nil {
		t.Fatal(err)
	}
	hello := `{"type":"hello","hello":{"plugin_id":"acme.p","slots":["main-stage"]}}` + "\n"
	peer, cp := connectDeclaring(t, m, hello)
	defer peer.Close()
	if cp == nil || m.registry.Slots["acme.p.rows"] == nil || m.registry.Hooks["acme.p.before"] == nil || m.registry.Events["acme.p.changed"] == nil {
		t.Fatal("manifest declarations were dropped by a hello that omitted them")
	}
}

func TestHelloMayAddDeclarationsBeyondManifest(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	mf := declaringManifest()
	m.manifests[mf.ID] = mf
	if err := m.registry.declareFromManifest(mf); err != nil {
		t.Fatal(err)
	}
	hello := `{"type":"hello","hello":{"plugin_id":"acme.p","slots":["main-stage"],"hooks":[{"id":"acme.p.extra"}]}}` + "\n"
	peer, cp := connectDeclaring(t, m, hello)
	defer peer.Close()
	if cp == nil || m.registry.Hooks["acme.p.extra"] == nil || m.registry.Hooks["acme.p.before"] == nil {
		t.Fatal("extra hello declaration was not additive")
	}
}

func TestHelloConflictingWithManifestDeclarationIsRejected(t *testing.T) {
	cases := map[string]struct{ declaration, want string }{
		"slot title":  {`"published_slots":[{"id":"acme.p.rows","title":"Other","kind":"inline"}]`, `slot "acme.p.rows" conflicts with its manifest declaration`},
		"slot kind":   {`"published_slots":[{"id":"acme.p.rows","kind":"region","rect":{"surface":"main-stage","x":0,"y":0,"width":1,"height":1}}]`, `slot "acme.p.rows" conflicts`},
		"slot rect":   {`"published_slots":[{"id":"acme.p.bar","kind":"region","rect":{"surface":"main-stage","x":0,"y":1,"width":21,"height":2}}]`, `slot "acme.p.bar" conflicts`},
		"hook title":  {`"hooks":[{"id":"acme.p.before","title":"Other"}]`, `id "acme.p.before" conflicts with its manifest declaration`},
		"event descr": {`"events":[{"id":"acme.p.changed","description":"Other"}]`, `id "acme.p.changed" conflicts`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := newWorkbenchModel("", nil, t.TempDir())
			var log bytes.Buffer
			m.logOut = &log
			mf := declaringManifest()
			m.manifests[mf.ID] = mf
			if err := m.registry.declareFromManifest(mf); err != nil {
				t.Fatal(err)
			}
			hello := `{"type":"hello","hello":{"plugin_id":"acme.p","slots":["main-stage"],` + tc.declaration + `}}` + "\n"
			env, _, m := rejectedFrom(t, m, streamOrigin{}, hello, &log)
			if env.Type != "protocol_error" || env.ProtocolError.MessageType != "hello" || !strings.Contains(env.ProtocolError.Message, tc.want) {
				t.Fatalf("got %+v, want message containing %q", env.ProtocolError, tc.want)
			}
			if m.plugins["acme.p"] != nil || m.registry.Occupants[huskipc.SlotMainStage] != "" {
				t.Fatal("rejected hello was registered")
			}
		})
	}
}

func TestHelloRedeclaringManifestSlotViaSlotDeclareIsRejected(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	mf := declaringManifest()
	m.manifests[mf.ID] = mf
	if err := m.registry.declareFromManifest(mf); err != nil {
		t.Fatal(err)
	}
	// A later slot_declare for a manifest-declared id is a duplicate.
	if err := m.registry.declareSlot("acme.p", huskipc.SlotDeclarePayload{ID: "acme.p.rows", Kind: "inline"}); err == nil || !strings.Contains(err.Error(), "already declared") {
		t.Fatalf("err = %v", err)
	}
}

func TestPluginConnectingLateGetsContributionsMadeToItsManifestSlots(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	mf := declaringManifest()
	m.manifests[mf.ID] = mf
	if err := m.registry.declareFromManifest(mf); err != nil {
		t.Fatal(err)
	}
	if _, err := m.registry.contribute("other.c", huskipc.SlotContributePayload{SlotID: "acme.p.rows", ID: "other.c.row", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	// The hello omits published_slots: the manifest is the only declaration.
	hello := `{"type":"hello","hello":{"plugin_id":"acme.p","slots":["main-stage"]}}` + "\n"
	peer, _ := connectPipePlugin(t, m, hello)
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := map[string][]huskipc.SlotContribution{}
	var order []string
	err := huskipc.ReadLoop(peer, func(e huskipc.Envelope) {
		if e.Type == "slot_contributions" {
			got[e.SlotContributions.SlotID] = e.SlotContributions.Contributions
			order = append(order, e.SlotContributions.SlotID)
		}
		if len(order) == 2 {
			_ = peer.Close()
		}
	})
	_ = err
	rows := got["acme.p.rows"]
	if len(rows) != 1 || rows[0].ID != "other.c.row" || rows[0].PluginID != "other.c" {
		t.Fatalf("rows contributions = %+v (order %v)", rows, order)
	}
	if want := []string{"acme.p.bar", "acme.p.rows"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("delivery order = %v, want %v", order, want)
	}
}

func TestRejectedHelloKeepsManifestDeclarationsAndContributions(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	var log bytes.Buffer
	m.logOut = &log
	mf := declaringManifest()
	m.manifests[mf.ID] = mf
	if err := m.registry.declareFromManifest(mf); err != nil {
		t.Fatal(err)
	}
	if _, err := m.registry.contribute("other.c", huskipc.SlotContributePayload{SlotID: "acme.p.rows", ID: "other.c.row", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	// An impostor claiming acme.p with a conflicting and an extra declaration.
	hello := `{"type":"hello","hello":{"plugin_id":"acme.p","slots":["main-stage"],` +
		`"hooks":[{"id":"acme.p.extra"}],"published_slots":[{"id":"acme.p.rows","title":"Other","kind":"inline"}]}}` + "\n"
	env, _, m := rejectedFrom(t, m, streamOrigin{}, hello, &log)
	if env.Type != "protocol_error" {
		t.Fatalf("got %+v", env)
	}
	rows := m.registry.Slots["acme.p.rows"]
	if rows == nil || !rows.FromManifest || len(rows.Contributions) != 1 {
		t.Fatalf("manifest slot lost on rollback: %+v", rows)
	}
	if m.registry.Slots["acme.p.bar"] == nil || m.registry.Hooks["acme.p.before"] == nil || m.registry.Events["acme.p.changed"] == nil {
		t.Fatal("manifest declarations lost on rollback")
	}
	if m.registry.Hooks["acme.p.extra"] != nil {
		t.Fatal("hello-only declaration survived rollback")
	}
	if m.registry.Occupants[huskipc.SlotMainStage] != "" {
		t.Fatal("surface claim survived rollback")
	}
}

func assertNoDeclarations(t *testing.T, m *WorkbenchModel) {
	t.Helper()
	if m.registry.Slots["acme.p.rows"] != nil || m.registry.Slots["acme.p.bar"] != nil || m.registry.Hooks["acme.p.before"] != nil || m.registry.Events["acme.p.changed"] != nil {
		t.Fatal("declarations remain registered")
	}
}

func TestLaunchPluginRollsBackDeclarationsWhenSpawnFails(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	mf := declaringManifest()
	mf.Command = []string{filepath.Join(t.TempDir(), "does-not-exist")}
	if err := m.launchPlugin(mf); err == nil {
		t.Fatal("spawn of a nonexistent command succeeded")
	}
	assertNoDeclarations(t, m)
	if m.manifests["acme.p"] != nil || len(m.procs) != 0 {
		t.Fatal("failed launch left a manifest or process behind")
	}
}

func TestLaunchPluginRegistersDeclarationsBeforeHelloAndDropsThemOnEarlyExit(t *testing.T) {
	t.Setenv("HUSK_FAKE_PLUGIN", "crash")
	l, path, _, err := listenPrivate(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := lifecycleModel(t, path, l)
	mf := declaringManifest()
	mf.Command = fakeArgv
	if err := m.launchPlugin(mf); err != nil {
		t.Fatal(err)
	}
	if rows := m.registry.Slots["acme.p.rows"]; rows == nil || !rows.FromManifest {
		t.Fatal("declarations not registered right after launch")
	}
	// A contributor reaches the slot while the plugin is still starting.
	if _, err := m.registry.contribute("other.c", huskipc.SlotContributePayload{SlotID: "acme.p.rows", ID: "other.c.row"}); err != nil {
		t.Fatal(err)
	}
	// The plugin dies without ever sending hello.
	msg := awaitMsg[pluginExitedMsg](t, m)
	m.Update(msg)
	assertNoDeclarations(t, m)
	if _, err := m.registry.contribute("other.c", huskipc.SlotContributePayload{SlotID: "acme.p.rows", ID: "other.c.row2"}); err == nil {
		t.Fatal("contribution to the dead plugin's slot was accepted")
	}
	if m.manifests["acme.p"] == nil {
		t.Fatal("manifest was dropped; a late hello would no longer be checked")
	}
}
