package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hooksManifest builds a v2 manifest for plugin acme.p with the given hooks member.
func hooksManifest(hooks string) string {
	return `{"manifest_version":2,"protocol":2,"id":"acme.p","name":"P","version":"1","command":["./plugin"],"hooks":` + hooks + `}`
}

func publishOne(entry string) string   { return `{"publish":[` + entry + `]}` }
func subscribeOne(entry string) string { return `{"subscribe":[` + entry + `]}` }

func TestHookManifestValidation(t *testing.T) {
	long := "acme.p." + strings.Repeat("a", 250)
	for _, tc := range []struct {
		name, hooks string
		want        string // error substring; empty = accepted
	}{
		// kind
		{"kind transform", publishOne(`{"id":"acme.p.a","kind":"transform"}`), ""},
		{"kind collect", publishOne(`{"id":"acme.p.a","kind":"collect"}`), ""},
		{"kind gate", publishOne(`{"id":"acme.p.a","kind":"gate"}`), ""},
		{"kind missing", publishOne(`{"id":"acme.p.a"}`), "hooks.publish[0].kind"},
		{"kind other", publishOne(`{"id":"acme.p.a","kind":"other"}`), "hooks.publish[0].kind"},
		// timeout_ms edges
		{"timeout 999", publishOne(`{"id":"acme.p.a","kind":"gate","timeout_ms":999}`), "hooks.publish[0].timeout_ms"},
		{"timeout 1000", publishOne(`{"id":"acme.p.a","kind":"gate","timeout_ms":1000}`), ""},
		{"timeout 1001", publishOne(`{"id":"acme.p.a","kind":"gate","timeout_ms":1001}`), ""},
		{"timeout 10000", publishOne(`{"id":"acme.p.a","kind":"gate","timeout_ms":10000}`), ""},
		{"timeout 10001", publishOne(`{"id":"acme.p.a","kind":"gate","timeout_ms":10001}`), "hooks.publish[0].timeout_ms"},
		{"timeout negative", publishOne(`{"id":"acme.p.a","kind":"gate","timeout_ms":-5}`), "hooks.publish[0].timeout_ms"},
		{"timeout fractional", publishOne(`{"id":"acme.p.a","kind":"gate","timeout_ms":1500.5}`), "hooks.publish[0].timeout_ms"},
		{"timeout string", publishOne(`{"id":"acme.p.a","kind":"gate","timeout_ms":"2000"}`), "hooks.publish[0].timeout_ms"},
		// priority edges and forms
		{"subscribe string", subscribeOne(`"x.y"`), ""},
		{"subscribe object", subscribeOne(`{"id":"x.y","priority":5}`), ""},
		{"subscribe object without priority", subscribeOne(`{"id":"x.y"}`), ""},
		{"priority -1", subscribeOne(`{"id":"x.y","priority":-1}`), "hooks.subscribe[0].priority"},
		{"priority 0", subscribeOne(`{"id":"x.y","priority":0}`), ""},
		{"priority 100", subscribeOne(`{"id":"x.y","priority":100}`), ""},
		{"priority 101", subscribeOne(`{"id":"x.y","priority":101}`), "hooks.subscribe[0].priority"},
		{"priority fractional", subscribeOne(`{"id":"x.y","priority":5.5}`), "hooks.subscribe[0].priority"},
		{"priority exponent", subscribeOne(`{"id":"x.y","priority":1e1}`), "hooks.subscribe[0].priority"},
		{"priority string", subscribeOne(`{"id":"x.y","priority":"5"}`), "hooks.subscribe[0].priority"},
		{"subscribe number", subscribeOne(`7`), "hooks.subscribe[0]"},
		// publish namespace and id rule
		{"other plugin namespace", publishOne(`{"id":"other.a","kind":"gate"}`), "hooks.publish[0].id"},
		{"husk namespace", publishOne(`{"id":"husk.x","kind":"gate"}`), "hooks.publish[0].id"},
		{"plugin id alone", publishOne(`{"id":"acme.p","kind":"gate"}`), "hooks.publish[0].id"},
		{"empty name", publishOne(`{"id":"acme.p.","kind":"gate"}`), "hooks.publish[0].id"},
		{"nested name", publishOne(`{"id":"acme.p.a.b","kind":"gate"}`), ""},
		{"duplicate publish", `{"publish":[{"id":"acme.p.a","kind":"gate"},{"id":"acme.p.a","kind":"collect"}]}`, "hooks.publish[1].id"},
		{"publish whitespace", publishOne(`{"id":"acme.p.a b","kind":"gate"}`), "hooks.publish[0].id"},
		{"publish semicolon", publishOne(`{"id":"acme.p.a;b","kind":"gate"}`), "hooks.publish[0].id"},
		{"publish equals", publishOne(`{"id":"acme.p.a=b","kind":"gate"}`), "hooks.publish[0].id"},
		{"publish backslash", publishOne(`{"id":"acme.p.a\\b","kind":"gate"}`), "hooks.publish[0].id"},
		{"publish star", publishOne(`{"id":"acme.p.*","kind":"gate"}`), "hooks.publish[0].id"},
		{"publish 256 bytes", publishOne(`{"id":"` + long[:256] + `","kind":"gate"}`), ""},
		{"publish 257 bytes", publishOne(`{"id":"` + long[:256] + `a","kind":"gate"}`), "hooks.publish[0].id"},
		// subscribe ids
		{"subscribe no dot", subscribeOne(`"nodot"`), "hooks.subscribe[0].id"},
		{"subscribe star", subscribeOne(`"a.*"`), "hooks.subscribe[0].id"},
		{"subscribe empty segment", subscribeOne(`"a..b"`), "hooks.subscribe[0].id"},
		{"subscribe whitespace", subscribeOne(`"a.b c"`), "hooks.subscribe[0].id"},
		{"subscribe semicolon", subscribeOne(`"a.b;c"`), "hooks.subscribe[0].id"},
		{"subscribe equals", subscribeOne(`"a.b=c"`), "hooks.subscribe[0].id"},
		{"subscribe backslash", subscribeOne(`"a.b\\c"`), "hooks.subscribe[0].id"},
		{"subscribe 257 bytes", subscribeOne(`"` + long[:256] + `a"`), "hooks.subscribe[0].id"},
		{"subscribe object no id", subscribeOne(`{"priority":5}`), "hooks.subscribe[0].id"},
		{"duplicate string+string", `{"subscribe":["x.y","x.y"]}`, "hooks.subscribe[1].id"},
		{"duplicate string+object", `{"subscribe":["x.y",{"id":"x.y","priority":3}]}`, "hooks.subscribe[1].id"},
		{"duplicate object+object", `{"subscribe":[{"id":"x.y","priority":1},{"id":"x.y","priority":2}]}`, "hooks.subscribe[1].id"},
		{"subscribe own hook", subscribeOne(`"acme.p.a"`), ""},
		{"subscribe husk hook", subscribeOne(`"husk.before-state-clear"`), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, _, err := convertBytes(t, hooksManifest(tc.hooks), true)
			if tc.want == "" {
				if err != nil || spec == nil {
					t.Fatalf("want accepted, got spec=%v err=%v", spec, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if spec != nil {
				t.Fatalf("a refused manifest returned a Spec: %+v", spec)
			}
		})
	}
}

func TestHookDefaultsAreWrittenExplicitly(t *testing.T) {
	spec, _, err := convertBytes(t, hooksManifest(`{
		"publish":[
			{"id":"acme.p.a","kind":"transform"},
			{"id":"acme.p.b","kind":"gate","timeout_ms":0},
			{"id":"acme.p.c","kind":"collect","timeout_ms":2500}],
		"subscribe":["x.y",{"id":"x.z"},{"id":"x.w","priority":0},{"id":"x.v","priority":42}]}`), true)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct {
		kind    HookKind
		timeout int
	}{{HookTransform, 10000}, {HookGate, 10000}, {HookCollect, 2500}} {
		if h := spec.HooksDeclared[i]; h.Kind != want.kind || h.TimeoutMs != want.timeout {
			t.Fatalf("hook %d = %+v, want kind %s timeout %d", i, h, want.kind, want.timeout)
		}
	}
	for i, want := range []int{10, 10, 0, 42} {
		if got := spec.HookRegistrations[i].Priority; got != want {
			t.Fatalf("registration %d priority = %d, want %d", i, got, want)
		}
	}
}

func TestV1HooksBecomeGatePublishEntries(t *testing.T) {
	spec, _, err := convertBytes(t, declaring(`["main-stage"]`, `,"hooks":[{"id":"acme.p.before","title":"B"},{"id":"acme.p.after"}]`), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.HooksDeclared) != 2 {
		t.Fatalf("hooks = %+v", spec.HooksDeclared)
	}
	for _, h := range spec.HooksDeclared {
		if h.Kind != HookGate || h.TimeoutMs != 10000 {
			t.Fatalf("v1 hook = %+v, want gate with timeout 10000", h)
		}
	}
}

func TestHookCatalogRoundTripAndClone(t *testing.T) {
	spec, _, err := convertBytes(t, hooksManifest(`{
		"publish":[{"id":"acme.p.a","kind":"transform","title":"A","description":"Does a","timeout_ms":2000}],
		"subscribe":[{"id":"x.y","priority":7}]}`), true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := spec.CatalogJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks []map[string]any `json:"hooks_declared"`
		Regs  []map[string]any `json:"hook_registrations"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	h := doc.Hooks[0]
	if h["id"] != "acme.p.a" || h["kind"] != "transform" || h["title"] != "A" || h["description"] != "Does a" || h["timeout_ms"] != float64(2000) {
		t.Fatalf("catalog hook = %v", h)
	}
	if r := doc.Regs[0]; r["id"] != "x.y" || r["priority"] != float64(7) {
		t.Fatalf("catalog registration = %v", r)
	}
	// A priority of 0 must stay in the document: it is not omitted as empty.
	zero, _, err := convertBytes(t, hooksManifest(subscribeOne(`{"id":"x.y","priority":0}`)), true)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = zero.CatalogJSON()
	if !strings.Contains(string(data), `"priority":0`) {
		t.Fatalf("priority 0 missing from %s", data)
	}
	clone := spec.Clone()
	clone.HooksDeclared[0].Kind = HookGate
	clone.HookRegistrations[0].Priority = 99
	if spec.HooksDeclared[0].Kind != HookTransform || spec.HookRegistrations[0].Priority != 7 {
		t.Fatal("Clone shares hook slices with the original")
	}
}

// The bundled reference plugins are the documented template: they must convert.
func TestBundledPluginManifestsConvert(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "plugins", "*", FileName))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no bundled manifests found: %v", err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			dir, err := filepath.Abs(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			src := Source{Path: filepath.Join(dir, FileName), Dir: dir, Bytes: raw}
			if _, _, err := (V2Converter{}).Convert(src); err != nil {
				t.Fatal(err)
			}
		})
	}
}
