// Package workbench provides Husk's Bubble Tea workbench UI.
package workbench

import (
	"context"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

// Options configures a workbench without exposing its model or event loop.
type Options struct {
	// NewHost constructs the host before the loop starts. The supplied Config
	// contains the loop's Post and Content callbacks; the caller fills its
	// instance, runtime and flag-derived fields and applies the supplied host
	// options after its own defaults. The workbench owns the returned host and
	// closes it after closing the post gate. Required except for demo frames.
	NewHost func(contracts.Config, ...host.Option) (host.Looped, error)
	// Log receives host diagnostics. Nil uses the process's stderr. Interactive
	// callers should buffer diagnostics until Run returns to protect the TUI.
	Log io.Writer
	// Width and Height override the initial 144 by 40 size when positive.
	// Interactive terminal resize messages still determine the actual size.
	Width, Height int
	// HoldAlt latches Alt on startup, as if the modifier were held down.
	HoldAlt bool
	// RailMode selects the initial dump-frame rail placement: modal,
	// full-canvas or stage. Empty leaves the default placement.
	RailMode string
	// DemoFrame renders the built-in framed surface instead of starting a host.
	DemoFrame bool
	// Demo selects the built-in published-slot fixture ("pubslots").
	Demo string
}

func configuredModel(opt Options) *workbenchModel {
	m := newModel()
	if opt.Width > 0 {
		m.width = opt.Width
	}
	if opt.Height > 0 {
		m.height = opt.Height
	}
	if opt.HoldAlt {
		m.altLatched = true
		m.modifiers.Alt = true
	}
	if opt.Log != nil {
		m.logOut = opt.Log
	}
	return m
}

func attachHost(ctx context.Context, m *workbenchModel, prog *programRef, opt Options, opts ...host.Option) (host.Looped, error) {
	if opt.NewHost == nil {
		return nil, fmt.Errorf("workbench requires a host constructor")
	}
	opts = append([]host.Option{host.WithLogger(func(level, msg string) { m.logf("%s: %s", level, msg) })}, opts...)
	h, err := opt.NewHost(contracts.Config{Post: prog.post, Content: m.content}, opts...)
	if err != nil {
		prog.closeGate()
		return nil, err
	}
	m.attach(h, prog.closeGate)
	m.stopRun()
	m.runCtx, m.stopRun = context.WithCancel(ctx)
	return h, nil
}

// Run starts the interactive workbench and blocks until it exits or ctx ends.
// It owns the Bubble Tea program, post gate and host shutdown; it never exits
// the process or prints an error returned to the caller. Returned errors carry
// the binary's diagnostic prefix and can be printed directly.
func Run(ctx context.Context, opt Options) error {
	m := configuredModel(opt)
	prog := newProgramRef()
	p := tea.NewProgram(m, tea.WithContext(ctx))
	prog.set(p)
	if _, err := attachHost(ctx, m, prog, opt, host.WithPrompt(secretPromptFunc(prog))); err != nil {
		return fmt.Errorf("husk: %w", err)
	}
	_, err := p.Run()
	prog.closeGate()
	m.shutdownHost()
	if err != nil {
		return fmt.Errorf("husk error: %w", err)
	}
	if m.hostErr != nil {
		return fmt.Errorf("husk: host stopped: %w", m.hostErr)
	}
	return nil
}

// DumpFrame renders one settled frame without a terminal or signal handler.
// It returns the current frame even when settling fails, so the caller can
// print it alongside the error and choose the process exit status. DemoFrame
// and Demo render their fixtures without constructing a host.
func DumpFrame(ctx context.Context, opt Options) (string, error) {
	m := configuredModel(opt)
	if opt.Demo != "" {
		if opt.Demo != "pubslots" {
			return "", fmt.Errorf("unknown workbench demo %q", opt.Demo)
		}
		// The published-slot fixture has never inherited the Alt latch.
		m.altLatched = false
		m.modifiers.Alt = false
		m.recomputeLayout()
		m.seedPubslotsDemo()
		return m.View().Content, nil
	}
	if opt.DemoFrame {
		m.demoFrame = true
		m.setupDemoFrame(opt.RailMode)
		return m.View().Content, nil
	}
	prog := newProgramRef()
	h, err := attachHost(ctx, m, prog, opt)
	if err != nil {
		return "", err
	}
	frame, err := runDumpFrame(m, dumpFrameOptions{Context: ctx, Launched: h.Planned(), RailMode: opt.RailMode, Program: prog})
	prog.closeGate()
	m.shutdownHost()
	return frame, err
}
