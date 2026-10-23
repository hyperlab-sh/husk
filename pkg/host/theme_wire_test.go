package host

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

// assertThemeWire checks raw JSON too: a hex theme must omit ansi, not send null
// or an empty object. Palette values remain hex even when every role is indexed.
func assertThemeWire(t *testing.T, raw json.RawMessage, name string) {
	t.Helper()
	var got protocol.ThemeChangedParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Palette.Name != name {
		t.Fatalf("palette name = %q, want %q", got.Palette.Name, name)
	}
	if _, present := got.ANSI["name"]; present {
		t.Fatal("ansi must never contain name")
	}
	if name == "base16" && len(got.ANSI) != 19 {
		t.Fatalf("base16 has %d ANSI roles, want 19", len(got.ANSI))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	roles := theme.BuiltIn()["themes"].(map[string]any)[name].(map[string]any)["palette"].(map[string]any)
	var values map[string]string
	if err := json.Unmarshal(fields["palette"], &values); err != nil {
		t.Fatal(err)
	}
	wantIndices := map[string]int{}
	for role, value := range roles {
		hex, index, err := theme.ResolveValue(value)
		if err != nil {
			t.Fatal(err)
		}
		if values[role] != hex {
			t.Errorf("%s = %q, want %q", role, values[role], hex)
		}
		if index >= 0 {
			wantIndices[role] = index
		}
	}
	if !maps.Equal(got.ANSI, wantIndices) {
		t.Fatalf("ansi = %v, want %v", got.ANSI, wantIndices)
	}
	if _, present := fields["ansi"]; present != (len(wantIndices) > 0) {
		t.Fatalf("ansi presence: %s", raw)
	}
}

func TestThemeWireGetAndLaunch(t *testing.T) {
	for _, name := range []string{"base16", "charm"} {
		t.Run(name, func(t *testing.T) {
			h, f, _ := bootFrontend(t, func(c *contracts.Config) {
				c.InstanceDocs["config"]["theme"] = map[string]any{"name": name}
			})
			// Read the actual bytes to detect omission independently of decoding.
			p := f.proc("alpha")
			p.mu.Lock()
			var raw json.RawMessage
			for _, m := range p.opening {
				if m.Method == protocol.MethodThemeChanged {
					raw = m.Params
				}
			}
			p.mu.Unlock()
			assertThemeWire(t, raw, name)

			// Exercise the registered handler, port and limiter through the real
			// Server, both as the CLI and as the authenticated live plugin.
			for _, token := range []string{"", p.Token()} {
				reply := rpc(t, h, token, protocol.MethodThemeGet, map[string]any{})
				if reply.Error != nil {
					t.Fatal(reply.Error)
				}
				assertThemeWire(t, reply.Result, name)
			}
		})
	}
}

func TestThemeWireReloadAndIdenticalRepublish(t *testing.T) {
	h, f, _ := bootFrontend(t, nil)
	for i, name := range []string{"base16", "base16", "charm", "charm"} {
		r := hostSet(t, h, "theme.name", name)
		h.loop(func() {})
		sawTheme := false
		for _, report := range r.Reports {
			if report.Service != "theme" {
				continue
			}
			sawTheme = true
			if !report.Applied || report.Changed != (i%2 == 0) {
				t.Fatalf("iteration %d: %+v", i, report)
			}
		}
		if !sawTheme {
			t.Fatalf("iteration %d: no theme report", i)
		}
		p := f.proc("alpha")
		p.mu.Lock()
		var pushes []json.RawMessage
		for _, m := range p.sent {
			if m.Method == protocol.MethodThemeChanged {
				pushes = append(pushes, m.Params)
			}
		}
		p.mu.Unlock()
		if len(pushes) != i/2+1 {
			t.Fatalf("iteration %d: got %d pushes", i, len(pushes))
		}
		assertThemeWire(t, pushes[len(pushes)-1], name)
	}
}
