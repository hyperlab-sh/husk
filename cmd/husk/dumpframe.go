package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// dumpFrameTimeout bounds how long --dump-frame waits for the launched
// plugins to publish. It is a deadline on a context, not a pause: a healthy
// run ends the moment the settle condition holds.
const dumpFrameTimeout = 3 * time.Second

// dumpFrameQuiet is how long the workbench must receive no plugin traffic,
// once every awaited surface has content, before the frame is taken. A plugin
// keeps refining its slots after the first update (live data arrives later),
// and the protocol has no "done" marker, so quiescence is the settle signal.
// It is a program timer re-armed by each plugin message, not a sleep.
const dumpFrameQuiet = 300 * time.Millisecond

// dumpFrameMaxSettle caps how long, measured from the moment every awaited
// surface has content, the frame keeps waiting for the quiet window. A plugin
// that streams updates faster than dumpFrameQuiet (live data) would otherwise
// re-arm the window forever; at the cap the frame is taken as it stands.
const dumpFrameMaxSettle = time.Second

// dumpFrameOptions describes what --dump-frame waits for.
type dumpFrameOptions struct {
	// Launched is the number of plugins the host will launch (its plan, not
	// the number of arguments: an empty directory plans none). Zero means no
	// plugin is awaited; otherwise the frame is taken once the host reported
	// every Pod, none is still starting, and a live plugin published. When
	// every Pod ended without going live the run fails, naming them.
	Launched int
	// RailMode opens the first rail item with this placement (modal,
	// full-canvas, stage) once every launched plugin has registered, and waits
	// for its surface. Empty means no rail item is opened.
	RailMode string
	// Quiet overrides dumpFrameQuiet when positive.
	Quiet time.Duration
	// MaxSettle overrides dumpFrameMaxSettle when positive.
	MaxSettle time.Duration
	// Timeout overrides dumpFrameTimeout when positive.
	Timeout time.Duration
	// Program receives the headless program so the host's Post reaches it.
	Program *programRef
	// Stderr receives host diagnostics (a --rail with no matching item).
	Stderr io.Writer
}

// dumpQuietMsg fires when a quiet window ends; Gen discards stale timers.
type dumpQuietMsg struct{ Gen int }

// dumpCapMsg fires when the maximum settle time since ready has elapsed.
type dumpCapMsg struct{}

// dumpFrameDriver wraps the workbench so the real tea.Program loop runs it:
// every message goes through the model's Update on the program goroutine, and
// the driver asks the program to quit once the model has settled. There is no
// second event loop and no sleep.
type dumpFrameDriver struct {
	*WorkbenchModel
	opt          dumpFrameOptions
	mode         string
	railTriggers bool
	ready        bool // every awaited surface has content
	armed        bool
	gen          int
	quiet        time.Duration
	maxSettle    time.Duration
	capArmed     bool
	settled      bool
	traffic      string // fingerprint of the last seen plugin traffic
	failed       bool   // every launched Pod ended without going live
}

// activity fingerprints what a plugin can change: its Pod status and the
// content it published. A change restarts the quiet window.
func (d *dumpFrameDriver) activity() string {
	m := d.WorkbenchModel
	return fmt.Sprintf("%d/%d/%v", m.totalIPCEvents, len(m.slotUpdates), m.podStatus)
}

func (d *dumpFrameDriver) Init() tea.Cmd {
	return tea.Batch(d.WorkbenchModel.Init(), d.arm())
}

func (d *dumpFrameDriver) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(dumpCapMsg); ok {
		if d.ready {
			d.settled = true
			return d, tea.Quit
		}
		return d, nil
	}
	if q, ok := msg.(dumpQuietMsg); ok {
		if q.Gen == d.gen && d.ready {
			d.settled = true
			return d, tea.Quit
		}
		return d, nil
	}
	_, cmd := d.WorkbenchModel.Update(msg)
	d.advance()
	if d.failed {
		return d, tea.Batch(cmd, tea.Quit)
	}
	if d.traffic != d.activity() {
		// Pod or content traffic restarts the quiet window.
		d.traffic = d.activity()
		return d, tea.Batch(cmd, d.arm())
	}
	return d, tea.Batch(cmd, d.armOnce())
}

// armOnce arms the first quiet window when the model became ready without
// plugin traffic (nothing to wait for, or a rail item opened by advance).
func (d *dumpFrameDriver) armOnce() tea.Cmd {
	if d.armed {
		return nil
	}
	return d.arm()
}

// arm starts a fresh quiet window if the model is ready; the timer message
// carries the generation so earlier windows are ignored.
func (d *dumpFrameDriver) arm() tea.Cmd {
	d.advance()
	if !d.ready {
		return nil
	}
	d.armed = true
	d.gen++
	gen := d.gen
	if d.quiet <= 0 {
		return func() tea.Msg { return dumpQuietMsg{Gen: gen} }
	}
	quiet := tea.Tick(d.quiet, func(time.Time) tea.Msg { return dumpQuietMsg{Gen: gen} })
	if d.capArmed {
		return quiet
	}
	// First time ready: start the one-shot cap that bounds re-arming.
	d.capArmed = true
	return tea.Batch(quiet, tea.Tick(d.maxSettle, func(time.Time) tea.Msg { return dumpCapMsg{} }))
}

// advance opens the requested rail item once all plugins registered and
// records whether every awaited surface has content.
func (d *dumpFrameDriver) advance() {
	m := d.WorkbenchModel
	d.ready = false
	if d.opt.Launched > 0 {
		if len(m.podStatus) < d.opt.Launched {
			return
		}
		live := false
		for _, st := range m.podStatus {
			switch st {
			case contracts.PodStarting:
				return
			case contracts.PodLive:
				live = true
			}
		}
		if !live {
			d.failed = true
			return
		}
	}
	if !d.railTriggers {
		d.railTriggers = true
		if d.mode != "" {
			// Husk knows no specific plugin; open the first rail item with
			// the requested placement.
			if ri, ok := m.firstRailItemWithPlacement(contracts.RailPlacement(d.mode)); ok {
				m.activateRailItem(ri)
			} else {
				if d.opt.Stderr != nil {
					fmt.Fprintf(d.opt.Stderr, "husk: --rail %s: no loaded plugin declares a %s rail item\n", d.mode, d.mode)
				}
				d.mode = "" // nothing to wait for
			}
		}
	}
	if len(m.plugins) > 0 && len(m.slotUpdates) == 0 {
		return
	}
	switch d.mode {
	case "modal":
		d.ready = m.slotUpdates[protocol.SurfaceModal] != nil
	case "full-canvas":
		d.ready = m.slotUpdates[protocol.SurfaceFullCanvas] != nil
	case "stage":
		d.ready = m.slotUpdates[protocol.SurfaceWorkspaceStage] != nil
	default:
		d.ready = true
	}
}

// runDumpFrame runs m under a headless tea.Program (no TTY, no renderer, no
// signal handling) and returns the frame once the model has settled. When the
// deadline passes first, it still returns the frame as it stands together with
// an error saying what was missing, so callers can print both.
func runDumpFrame(m *WorkbenchModel, opt dumpFrameOptions) (string, error) {
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = dumpFrameTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	quiet := opt.Quiet
	if quiet <= 0 {
		quiet = dumpFrameQuiet
	}
	if opt.Launched == 0 {
		quiet = 0 // no plugin can still be talking
	}
	maxSettle := opt.MaxSettle
	if maxSettle <= 0 {
		maxSettle = dumpFrameMaxSettle
	}
	d := &dumpFrameDriver{WorkbenchModel: m, opt: opt, mode: opt.RailMode, quiet: quiet, maxSettle: maxSettle}
	p := tea.NewProgram(d,
		tea.WithContext(ctx),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignals(),
		tea.WithoutSignalHandler(),
		tea.WithWindowSize(m.width, m.height),
	)
	if opt.Program != nil {
		opt.Program.set(p)
	}
	_, err := p.Run()
	frame := m.View().Content
	switch {
	case d.failed:
		return frame, fmt.Errorf("no plugin went live: %s", failedPods(m.podStatus))
	case d.settled:
		return frame, nil
	case errors.Is(err, tea.ErrProgramPanic):
		return frame, err
	case d.ready && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, tea.ErrProgramKilled)):
		// Everything registered and published; the plugin is just still
		// streaming. That is not an error.
		return frame, nil
	case (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, tea.ErrProgramKilled)) && len(m.podStatus) < opt.Launched:
		// The Pods layer refused a spec ("plugin not added" in the log), so
		// it never got a husk.pods entry and the wait cannot complete.
		return frame, fmt.Errorf("%d of %d planned plugins never got a Pod (the host log names why; pods %v)",
			opt.Launched-len(m.podStatus), opt.Launched, m.podStatus)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, tea.ErrProgramKilled):
		return frame, fmt.Errorf("timed out after %s waiting for plugins to publish (pods %v, slots %d)",
			timeout, m.podStatus, len(m.slotUpdates))
	case err != nil:
		return frame, err
	}
	return frame, nil
}

// failedPods lists the Pods and their statuses for an error message.
func failedPods(status map[string]contracts.PodStatus) string {
	ids := make([]string, 0, len(status))
	for id := range status {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = fmt.Sprintf("plugin %s %s", id, status[id])
	}
	return strings.Join(out, ", ")
}
