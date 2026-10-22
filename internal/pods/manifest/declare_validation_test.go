package manifest

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// declaring builds a legacy manifest claiming slots, with extra top-level members.
func declaring(slots, extra string) string {
	return `{"id":"acme.p","name":"P","version":"1","command":["./plugin"],"slots":` + slots + extra + `}`
}

// The legacy manifest validation cases, one per rule. Each must refuse the
// whole manifest: no Spec comes back, so nothing from it can be registered.
func TestManifestDeclarationValidation(t *testing.T) {
	region := func(surface string, x, y, w, h int) string {
		return `{"id":"acme.p.bar","kind":"region","rect":{"surface":"` + surface + `","x":` + strconv.Itoa(x) + `,"y":` + strconv.Itoa(y) + `,"width":` + strconv.Itoa(w) + `,"height":` + strconv.Itoa(h) + `}}`
	}
	const claim = `["main-stage"]`
	for _, tc := range []struct {
		name, slots, extra, want string
	}{
		{"foreign slot namespace", claim, `,"published_slots":[{"id":"other.rows","kind":"inline"}]`, `invalid or duplicate slot id "other.rows"`},
		{"nested slot name", claim, `,"published_slots":[{"id":"acme.p.a.b","kind":"inline"}]`, `invalid or duplicate slot id "acme.p.a.b"`},
		{"bad kind", claim, `,"published_slots":[{"id":"acme.p.rows","kind":"floating"}]`, "slot kind must be inline or region"},
		{"inline with rect", claim, `,"published_slots":[{"id":"acme.p.rows","kind":"inline","rect":{"surface":"main-stage","width":1,"height":1}}]`, "inline slots cannot have rect"},
		{"region without rect", claim, `,"published_slots":[{"id":"acme.p.bar","kind":"region"}]`, "region requires a positive rect"},
		{"region zero width", claim, `,"published_slots":[` + region("main-stage", 0, 1, 0, 2) + `]`, "region requires a positive rect"},
		{"region zero height", claim, `,"published_slots":[` + region("main-stage", 0, 1, 2, 0) + `]`, "region requires a positive rect"},
		{"region negative x", claim, `,"published_slots":[` + region("main-stage", -1, 1, 2, 2) + `]`, "region requires a positive rect"},
		{"region negative y", claim, `,"published_slots":[` + region("main-stage", 0, -1, 2, 2) + `]`, "region requires a positive rect"},
		{"region on surface the manifest does not claim", claim, `,"published_slots":[` + region("sidebar-list", 0, 1, 2, 2) + `]`, "region requires a positive rect on a claimed surface"},
		// v1 claimed an unknown surface and failed at the region. v2 refuses the
		// claim itself: conversion maps "slots" to Surfaces (pods.md, Manifest
		// conversion, item 1) and every Surfaces entry must be a defined host
		// layout surface (protocol.SurfaceID.Valid), so the region rule is never
		// reached. The design docs have no sentence naming the closed set; the
		// check is the existing "validated as today" conversion behaviour.
		{"unknown surface in slots is refused before regions are checked", `["nowhere"]`, `,"published_slots":[` + region("nowhere", 0, 1, 2, 2) + `]`, `invalid or duplicate surface "nowhere"`},
		{"unknown surface beside a valid claim", `["main-stage","nowhere"]`, `,"published_slots":[` + region("nowhere", 0, 1, 2, 2) + `]`, `invalid or duplicate surface "nowhere"`},
		{"duplicate slot", claim, `,"published_slots":[{"id":"acme.p.rows","kind":"inline"},{"id":"acme.p.rows","kind":"inline"}]`, `invalid or duplicate slot id "acme.p.rows"`},
		{"foreign hook", claim, `,"hooks":[{"id":"other.before"}]`, `invalid or duplicate hook id "other.before"`},
		{"duplicate hook", claim, `,"hooks":[{"id":"acme.p.before"},{"id":"acme.p.before"}]`, `invalid or duplicate hook id "acme.p.before"`},
		{"event with space", claim, `,"events":[{"id":"acme.p.a b"}]`, `invalid or duplicate event id "acme.p.a b"`},
		{"empty event id", claim, `,"events":[{"id":""}]`, `invalid or duplicate event id ""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, _, err := convertBytes(t, declaring(tc.slots, tc.extra), false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if spec != nil {
				t.Fatalf("a refused manifest returned a Spec: %+v", spec)
			}
		})
	}
}

// The validation is shared by both dialects: a v2 manifest refuses a foreign
// hook published under hooks.publish the same way.
func TestV2ManifestRefusesForeignHook(t *testing.T) {
	spec, _, err := convertBytes(t, `{"manifest_version":2,"protocol":2,"id":"acme.p","name":"P","version":"1","command":["./plugin"],`+
		`"hooks":{"publish":[{"id":"other.before"}]}}`, true)
	if err == nil || !strings.Contains(err.Error(), `invalid or duplicate hook id "other.before"`) || spec != nil {
		t.Fatalf("spec = %+v, err = %v", spec, err)
	}
}

// The accepting side of the same rules: valid declarations convert and are kept.
func TestManifestDeclarationsAcceptedWhenValid(t *testing.T) {
	spec, _, err := convertBytes(t, declaring(`["main-stage"]`, `,"published_slots":[`+
		`{"id":"acme.p.rows","title":"Rows","description":"Row actions","kind":"inline"},`+
		`{"id":"acme.p.bar","kind":"region","rect":{"surface":"main-stage","x":0,"y":1,"width":20,"height":2}}],`+
		`"hooks":[{"id":"acme.p.before"}],"events":[{"id":"acme.p.changed"}]`), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.PublishedSlots) != 2 || len(spec.HooksDeclared) != 1 || len(spec.EventsDeclared) != 1 {
		t.Fatalf("declarations = %+v", spec)
	}
}

// Hook and event title/description must reach the declared catalog, in both
// manifest dialects; consumers list them to users.
func TestHookAndEventTitleAndDescriptionReachTheCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		modern    bool
	}{
		{"legacy", `{"id":"acme.p","name":"P","version":"1","command":["./plugin"],` +
			`"hooks":[{"id":"acme.p.before","title":"Before","description":"Runs first"}],` +
			`"events":[{"id":"acme.p.changed","title":"Changed","description":"State moved"}]}`, false},
		{"v2", `{"manifest_version":2,"protocol":2,"id":"acme.p","name":"P","version":"1","command":["./plugin"],` +
			`"hooks":{"publish":[{"id":"acme.p.before","title":"Before","description":"Runs first"}]},` +
			`"events":{"publish":[{"name":"acme.p.changed","title":"Changed","description":"State moved"}]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, _, err := convertBytes(t, tc.raw, tc.modern)
			if err != nil {
				t.Fatal(err)
			}
			data, err := spec.CatalogJSON()
			if err != nil {
				t.Fatal(err)
			}
			var catalog struct {
				Hooks  []HookDecl  `json:"hooks_declared"`
				Events []EventDecl `json:"events_declared"`
			}
			if err := json.Unmarshal(data, &catalog); err != nil {
				t.Fatal(err)
			}
			wantHook := HookDecl{ID: "acme.p.before", Title: "Before", Description: "Runs first"}
			if len(catalog.Hooks) != 1 || catalog.Hooks[0] != wantHook {
				t.Fatalf("hooks = %+v, want %+v", catalog.Hooks, wantHook)
			}
			if len(catalog.Events) != 1 {
				t.Fatalf("events = %+v", catalog.Events)
			}
			if e := catalog.Events[0]; e.Name != "acme.p.changed" || e.Title != "Changed" || e.Description != "State moved" {
				t.Fatalf("event = %+v", e)
			}
			// The retention must survive a Clone, which Store declarations use.
			clone := spec.Clone()
			if clone.HooksDeclared[0] != wantHook || clone.EventsDeclared[0].Title != "Changed" || clone.EventsDeclared[0].Description != "State moved" {
				t.Fatalf("clone lost metadata: %+v %+v", clone.HooksDeclared, clone.EventsDeclared)
			}
		})
	}
}
