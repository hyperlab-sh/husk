package host

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// themePushes decodes every theme.changed the fake process was sent after its
// opening.
func (p *fakeProc) themePushes(t *testing.T) []protocol.Palette {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []protocol.Palette
	for _, m := range p.sent {
		if m.Method != protocol.MethodThemeChanged {
			continue
		}
		var params protocol.ThemeChangedParams
		if err := json.Unmarshal(m.Params, &params); err != nil {
			t.Fatal(err)
		}
		out = append(out, params.Palette)
	}
	return out
}

type probe struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type reportData struct {
	Service  string  `json:"service"`
	Applied  bool    `json:"applied"`
	Changed  bool    `json:"changed"`
	Problems []probe `json:"problems,omitempty"`
	// Palette is a decoy: the push must read Theme's serving palette.
	Palette *protocol.Palette `json:"palette,omitempty"`
}

// reloaded publishes a services.reloaded event as the coordinator does and
// runs one loop turn so the bus delivered it.
func reloaded(t *testing.T, h *harness, reason string, reports ...reportData) {
	t.Helper()
	data := map[string]any{"reason": reason, "reports": reports}
	h.loop(func() { publisher{h.root}.Publish(servicesReloadedEvent, data) })
	h.loop(func() {})
}

// A services.reloaded carrying theme changed:true pushes theme.changed, with
// Theme's serving palette, to every live Pod and to no other; changed:false or
// another Service's change pushes nothing.
func TestThemeChangedPushesOnlyWhenThePaletteChanged(t *testing.T) {
	h, f, _ := bootFrontendWith(t, nil, alphaPlugin(t), writeFrontendPlugin(t, "beta", []string{"sidebar-list"}, nil))
	// beta fails: no longer live.
	h.root.cfg.Post(wrapper.ExitMsg{Info: wrapper.Info{PluginID: "beta", Status: wrapper.Failed, LastError: "crashed"}})
	h.loop(func() {})
	alpha, beta := f.proc("alpha"), f.proc("beta")
	decoy := protocol.Palette{Canvas: "#123456"}

	reloaded(t, h, "cli",
		reportData{Service: "theme", Applied: true, Changed: false},
		reportData{Service: "settings", Applied: true, Changed: true})
	if got := len(alpha.themePushes(t)); got != 0 {
		t.Fatalf("pushed %d theme.changed for an unchanged palette", got)
	}

	reloaded(t, h, "cli", reportData{Service: "theme", Applied: true, Changed: true, Palette: &decoy})
	pushes := alpha.themePushes(t)
	if len(pushes) != 1 {
		t.Fatalf("live Pod got %d theme.changed, want 1", len(pushes))
	}
	var serving protocol.Palette
	h.loop(func() { serving = h.root.rt.Theme.Wire() })
	if pushes[0] != serving || pushes[0].Canvas == decoy.Canvas {
		t.Fatalf("pushed %+v, want Theme's serving palette %+v (not the event's)", pushes[0], serving)
	}
	if got := len(beta.themePushes(t)); got != 0 {
		t.Fatalf("non-live Pod got %d theme.changed", got)
	}
}

// A reload the user did not start from the CLI that ends with errors is noted
// for the frontend's toast, with the error count across reports.
func TestConfigNoticesForUnattendedReloadsWithErrors(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	errs := func(n int) []probe {
		var ps []probe
		for range n {
			ps = append(ps, probe{Severity: "error", Code: "low_contrast", Message: "x"})
		}
		return ps
	}
	warning := []probe{{Severity: "warning", Code: "unknown_key", Message: "x"}}
	for _, tc := range []struct {
		name    string
		reason  string
		reports []reportData
		want    int // errors; 0 = no notice
	}{
		{"startup", "startup", []reportData{{Service: "theme", Problems: errs(1)}}, 1},
		{"profile counts across reports", "profile", []reportData{{Service: "settings", Problems: errs(2)}, {Service: "theme", Problems: errs(1)}, {Service: "secrets", Problems: warning}}, 3},
		{"plugin", "plugin", []reportData{{Service: "keybinding", Problems: errs(1)}}, 1},
		{"warnings only", "startup", []reportData{{Service: "settings", Problems: warning}}, 0},
		{"cli reload shows nothing", "cli", []reportData{{Service: "theme", Problems: errs(4)}}, 0},
		{"settings requester holds reports", "settings", []reportData{{Service: "theme", Problems: errs(4)}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := fe.LatestConfigNotice()
			reloaded(t, h, tc.reason, tc.reports...)
			seq, last := fe.LatestConfigNotice()
			if tc.want == 0 {
				if seq != before {
					t.Fatalf("notice %+v raised, want none", last)
				}
				return
			}
			if seq != before+1 || last != (contracts.ConfigNotice{Reason: tc.reason, Errors: tc.want}) {
				t.Fatalf("notice seq %d->%d last %+v, want one %s with %d errors", before, seq, last, tc.reason, tc.want)
			}
		})
	}
}
