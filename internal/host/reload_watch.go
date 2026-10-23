package host

import (
	"encoding/json"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/services"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// servicesReloadedEvent is the coordinator's one reload event.
const servicesReloadedEvent = "services.reloaded"

// reloadWatcher is the host's own subscription to services.reloaded. The bus
// calls Notify on the loop at the end of the tick that published the event.
// It does two things Services must not: push theme.changed to the live Pods
// when the palette changed, and note a reload the user did not start that
// ended with errors, for the frontend's toast.
type reloadWatcher struct{ r *Root }

var _ contracts.Notifier = reloadWatcher{}

// Notify implements contracts.Notifier.
func (w reloadWatcher) Notify(n protocol.Message) error {
	var p struct {
		Data struct {
			Reason  string `json:"reason"`
			Reports []struct {
				Service  string `json:"service"`
				Changed  bool   `json:"changed"`
				Problems []struct {
					Severity string `json:"severity"`
				} `json:"problems"`
			} `json:"reports"`
		} `json:"data"`
	}
	if err := json.Unmarshal(n.Params, &p); err != nil {
		return err
	}
	w.r.warmCatalog()
	errCount := 0
	themeChanged := false
	for _, rep := range p.Data.Reports {
		themeChanged = themeChanged || (rep.Service == "theme" && rep.Changed)
		for _, pr := range rep.Problems {
			if pr.Severity == "error" {
				errCount++
			}
		}
	}
	if themeChanged {
		// The palette is read from Theme's serving snapshot, not the event.
		if err := w.r.pods.Notify(protocol.MethodThemeChanged, protocol.ThemeChangedParams{Palette: w.r.rt.Theme.Wire()}); err != nil {
			w.r.log(pods.LogMsg{Level: "warn", Message: "theme.changed: " + err.Error()})
		}
	}
	switch services.Reason(p.Data.Reason) {
	case services.ReasonStartup, services.ReasonProfile, services.ReasonPlugin:
		if errCount > 0 {
			w.r.noticesMu.Lock()
			w.r.noticeSeq++
			w.r.lastNotice = contracts.ConfigNotice{Reason: p.Data.Reason, Errors: errCount}
			w.r.noticesMu.Unlock()
		}
	}
	return nil
}
