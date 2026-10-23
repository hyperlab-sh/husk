package host

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	pluginh "github.com/hyperlab-sh/husk/internal/host/handlers/plugin"
	servicesh "github.com/hyperlab-sh/husk/internal/host/handlers/services"
	stemh "github.com/hyperlab-sh/husk/internal/host/handlers/stem"
	portsservices "github.com/hyperlab-sh/husk/internal/host/ports/services"
	portsstem "github.com/hyperlab-sh/husk/internal/host/ports/stem"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/server"
)

// storeCleaner adapts Store's state.clear deletion to the State port.
type storeCleaner struct{ r *Root }

func (c storeCleaner) DeleteSlice(_ context.Context, namespace string) error {
	return c.r.store.ClearSlice(namespace)
}

// buildServer builds every port, registers the adapters on one Mux and the
// Mux on the Server for every inbound prefix the Server does not answer
// itself.
func (r *Root) buildServer() error {
	entry := r.cfg.Entry
	if entry.ID == "" {
		entry.ID = r.cfg.InstanceID
	}
	if entry.PID == 0 {
		entry.PID = os.Getpid()
	}
	if entry.StartedAt.IsZero() {
		entry.StartedAt = r.instStart
	}
	if entry.HostVersion == "" {
		entry.HostVersion = r.opts.version
	}
	scfg := server.Config{
		InstanceID: r.cfg.InstanceID,
		RuntimeDir: r.cfg.RuntimeDir,
		Entry:      entry,
		Tokens:     r.pods,
		Legacy:     r.pods,
		Post:       r.cfg.Post,
		Clock:      r.clock,
	}
	if r.opts.newServer != nil {
		r.srv = r.opts.newServer(scfg)
	} else {
		r.srv = server.New(scfg)
	}

	mux := handlers.NewMux()
	stemh.Register(mux, stemh.Ports{
		Session: &portsstem.Session{
			InstanceID:  r.cfg.InstanceID,
			PID:         entry.PID,
			HostVersion: r.opts.version,
			StartedAt:   entry.StartedAt,
			Clock:       r.clock,
		},
		Calls:   portsstem.NewCalls(r.disp, r.loopSeam.Post),
		Event:   portsstem.NewEvents(r.bus, r.loopSeam),
		Hook:    portsstem.NewHooks(r.runner, r.loopSeam.Post),
		Surface: portsstem.NewSurfacePort(r.engine, r.loopSeam, portsstem.WithContent(r.cfg.Content)),
		State: portsstem.NewStatePort(portsstem.StatePortOptions{
			State:   r.st,
			Loop:    r.loopSeam,
			Handles: pods.NewPodSlices(r.pods),
			Cleaner: storeCleaner{r},
			Hooks:   hookFirer{r},
			Logf: func(level, format string, args ...any) {
				r.log(pods.LogMsg{Level: level, Message: fmt.Sprintf(format, args...)})
			},
		}),
	})
	co := r.rt.Coordinator
	servicesh.Register(mux, servicesh.Ports{
		Settings: portsservices.Settings{Svc: r.rt.Settings, Inputs: co},
		Secrets:  portsservices.Secrets{Svc: r.rt.Secrets},
		Profile:  portsservices.Profile{Svc: r.rt.Profile},
		Theme:    portsservices.Theme{Svc: r.rt.Theme, Settings: r.rt.Settings, Inputs: co},
		Keys:     portsservices.Keys{Svc: r.rt.Keybinding},
		Config:   portsservices.Config{Co: co},
		Limiter:  servicesh.NewWindowLimiter(r.clock, contracts.RateLimitPerSecond),
	})
	mux.Register("plugin", pluginh.Plugin{Port: r.pods})
	r.mux = mux

	for _, prefix := range inboundPrefixes() {
		r.srv.Handle(prefix, handlers.ServerHandler{Handler: mux, Closing: r.closingCtx})
	}
	return nil
}

// inboundPrefixes lists the method prefixes the Server hands to the Mux:
// every prefix of an inbound method in contracts.MethodHomes except session,
// which the Server answers from its own registration entry.
func inboundPrefixes() []string {
	seen := map[string]bool{}
	for method, home := range contracts.MethodHomes() {
		if home.Port == contracts.Outbound || home.Port == contracts.Unrouted {
			continue
		}
		prefix, _, _ := strings.Cut(method, ".")
		if prefix != "session" {
			seen[prefix] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
