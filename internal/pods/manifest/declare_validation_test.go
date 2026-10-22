package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

// declaring builds a legacy manifest with the given extra top-level members.
func declaring(extra string) string {
	return `{"id":"acme.p","name":"P","version":"1","command":["./plugin"],"slots":["main-stage"]` + extra + `}`
}

// The legacy manifest validation cases, one per rule. Each must refuse the
// whole manifest: no Spec comes back, so nothing from it can be registered.
func TestManifestDeclarationValidation(t *testing.T) {
	region := func(surface string, w int) string {
		return `{"id":"acme.p.bar","kind":"region","rect":{"surface":"` + surface + `","x":0,"y":1,"width":` + string(rune('0'+w)) + `,"height":2}}`
	}
	for _, tc := range []struct {
		name, extra, want string
	}{
		{"foreign slot namespace", `,"published_slots":[{"id":"other.rows","kind":"inline"}]`, `invalid or duplicate slot id "other.rows"`},
		{"nested slot name", `,"published_slots":[{"id":"acme.p.a.b","kind":"inline"}]`, `invalid or duplicate slot id "acme.p.a.b"`},
		{"bad kind", `,"published_slots":[{"id":"acme.p.rows","kind":"floating"}]`, "slot kind must be inline or region"},
		{"inline with rect", `,"published_slots":[{"id":"acme.p.rows","kind":"inline","rect":{"surface":"main-stage","width":1,"height":1}}]`, "inline slots cannot have rect"},
		{"region without rect", `,"published_slots":[{"id":"acme.p.bar","kind":"region"}]`, "region requires a positive rect"},
		{"region zero width", `,"published_slots":[` + region("main-stage", 0) + `]`, "region requires a positive rect"},
		{"region on surface the manifest does not claim", `,"published_slots":[` + region("sidebar-list", 2) + `]`, "region requires a positive rect on a claimed surface"},
		{"region on unknown surface", `,"slots":["nowhere"],"published_slots":[` + region("nowhere", 2) + `]`, `invalid or duplicate surface "nowhere"`},
		{"duplicate slot", `,"published_slots":[{"id":"acme.p.rows","kind":"inline"},{"id":"acme.p.rows","kind":"inline"}]`, `invalid or duplicate slot id "acme.p.rows"`},
		{"foreign hook", `,"hooks":[{"id":"other.before"}]`, `invalid or duplicate hook id "other.before"`},
		{"duplicate hook", `,"hooks":[{"id":"acme.p.before"},{"id":"acme.p.before"}]`, `invalid or duplicate hook id "acme.p.before"`},
		{"event with space", `,"events":[{"id":"acme.p.a b"}]`, `invalid or duplicate event id "acme.p.a b"`},
		{"empty event id", `,"events":[{"id":""}]`, `invalid or duplicate event id ""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := declaring(tc.extra)
			if strings.HasPrefix(tc.extra, `,"slots"`) {
				// The case replaces the claim, so drop the default one.
				raw = strings.Replace(raw, `,"slots":["main-stage"]`, "", 1)
			}
			spec, _, err := convertBytes(t, raw, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if spec != nil {
				t.Fatalf("a refused manifest returned a Spec: %+v", spec)
			}
		})
	}
}

// The accepting side of the same rules: valid declarations convert and are kept.
func TestManifestDeclarationsAcceptedWhenValid(t *testing.T) {
	spec, _, err := convertBytes(t, declaring(`,"published_slots":[`+
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
