// Command reference is Husk's reference plugin. It is deliberately small and
// exercises every extension point a plugin can use:
//
//   - a frame (border, label and close declared in hello.frames),
//   - a published inline slot (row actions anchored in slot_update lines),
//   - a hook the plugin calls and an event it emits,
//   - slot_resize for width, so nothing is hard-coded.
//
// It runs in one of two roles from the same binary, because a plugin cannot
// contribute to its own published slot:
//
//	reference                 the publisher: owns main-stage, publishes the slot,
//	                          hook and event
//	reference --contributor   fills the publisher's slot, answers its hook and
//	                          listens to its event
//
// The wire types come from pkg/huskipc, the same package the host uses, so the
// plugin cannot drift from the protocol. It speaks stdio (see its manifest).
// Diagnostics go to stderr, which Husk keeps in the plugin's log file.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// IDs follow the <plugin_id>.<name> rule: the final dot separates the plugin
// ID from one dot-free name.
const (
	publisherID   = "go.reference"
	contributorID = "go.reference-actions"

	rowActionsSlot = publisherID + ".row-actions"
	refreshHook    = publisherID + ".before-refresh"
	selectionEvent = publisherID + ".selection-changed"
	inspectCommand = contributorID + ".inspect"
	inspectContrib = contributorID + ".inspect-button"

	stage = huskipc.SlotMainStage
)

var rows = []string{"alpha", "bravo", "charlie"}

// retryDelay paces setup retries. A contributor can start before the
// publisher has declared what it subscribes to; the host answers with a
// protocol_error and the contributor tries again.
const retryDelay = 100 * time.Millisecond

// maxRetries bounds the retries so a missing publisher cannot loop forever.
const maxRetries = 50

func main() {
	contributor := len(os.Args) > 1 && os.Args[1] == "--contributor"
	if err := run(contributor, os.Stdin, os.Stdout, os.Stderr, retryDelay); err != nil {
		fmt.Fprintln(os.Stderr, "reference:", err)
		os.Exit(1)
	}
}

// plugin is single-threaded: run feeds it one message at a time.
type plugin struct {
	contributor bool
	out         *huskipc.ConnWriter
	log         io.Writer

	width, height int
	selected      int
	requests      int // refresh hook calls made
	results       int // hook results received
	lastResult    string
	lastEvent     string

	retries int
}

// run drives one plugin until in reaches EOF.
func run(contributor bool, in io.Reader, out, errw io.Writer, retry time.Duration) error {
	p := &plugin{contributor: contributor, out: huskipc.NewConnWriter(out), log: errw}

	msgs := make(chan huskipc.Envelope)
	var readErr error
	go func() {
		defer close(msgs)
		readErr = huskipc.ReadLoopReporting(in, func(e huskipc.Envelope) { msgs <- e }, func(le *huskipc.LineError) {
			fmt.Fprintf(errw, "reference: unreadable line: %v\n", le)
		})
	}()

	if err := p.hello(); err != nil {
		return err
	}
	var mu sync.Mutex // guards timer: AfterFunc fires on its own goroutine
	var timer *time.Timer
	tick := make(chan struct{}, 1)
	schedule := func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(retry, func() {
			select {
			case tick <- struct{}{}:
			default:
			}
		})
	}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case env, ok := <-msgs:
			if !ok {
				return readErr
			}
			if err := p.handle(env, schedule); err != nil {
				return err
			}
		case <-tick:
			if err := p.setup(); err != nil {
				return err
			}
		}
	}
}

func (p *plugin) hello() error {
	h := &huskipc.HelloPayload{
		ProtocolVersion: huskipc.ProtocolVersion,
		PluginID:        publisherID,
		Name:            "Reference plugin",
		Version:         "0.1.0",
		Language:        "Go",
		PID:             os.Getpid(),
		Description:     "Frame, published slot, hook and event in one small plugin",
		Slots:           []huskipc.SlotID{stage},
		Frames: map[huskipc.SlotID]huskipc.FrameSpec{
			stage: {Border: true, Label: "Reference plugin", Close: true},
		},
		PublishedSlots: []huskipc.SlotDeclarePayload{{
			ID: rowActionsSlot, Title: "Row actions", Description: "Actions beside each row", Kind: "inline",
		}},
		Hooks: []huskipc.HookDeclarePayload{{
			ID: refreshHook, Title: "Before refresh", Description: "Called when the reference plugin refreshes",
		}},
		Events: []huskipc.EventDeclarePayload{{
			ID: selectionEvent, Title: "Selection changed", Description: "The selected row changed",
		}},
	}
	if p.contributor {
		h = &huskipc.HelloPayload{
			ProtocolVersion: huskipc.ProtocolVersion,
			PluginID:        contributorID,
			Name:            "Reference actions",
			Version:         "0.1.0",
			Language:        "Go",
			PID:             os.Getpid(),
			Description:     "Contributes to the reference plugin's row actions slot",
			Slots:           []huskipc.SlotID{},
		}
	}
	return p.out.Send(huskipc.Envelope{Type: "hello", Hello: h})
}

// setup is the contributor's work that depends on the publisher being
// connected. It is safe to repeat: contributions replace by ID and duplicate
// subscriptions are ignored.
func (p *plugin) setup() error {
	for _, env := range []huskipc.Envelope{
		{Type: "slot_contribute", SlotContribute: &huskipc.SlotContributePayload{
			SlotID: rowActionsSlot, ID: inspectContrib, Content: "\x1b[32m[inspect]\x1b[0m", OnClick: inspectCommand,
		}},
		{Type: "hook_subscribe", HookSubscribe: &huskipc.HookSubscribePayload{ID: refreshHook}},
		{Type: "event_subscribe", EventSubscribe: &huskipc.EventSubscribePayload{ID: selectionEvent}},
	} {
		if err := p.out.Send(env); err != nil {
			return err
		}
	}
	return nil
}

func (p *plugin) handle(env huskipc.Envelope, retry func()) error {
	switch env.Type {
	case "welcome":
		if w := env.Welcome; w != nil {
			p.resize(w.SlotSizes)
		}
		if p.contributor {
			return p.setup()
		}
		return p.render()
	case "slot_resize":
		if env.SlotResize == nil {
			return nil
		}
		w, h := p.width, p.height
		p.resize(env.SlotResize.SlotSizes)
		if p.contributor || (w == p.width && h == p.height) {
			return nil
		}
		return p.render()
	case "slot_key":
		if p.contributor || env.SlotKey == nil {
			return nil
		}
		return p.key(env.SlotKey.Key)
	case "hook_result":
		if p.contributor || env.HookResult == nil {
			return nil
		}
		p.results++
		p.lastResult = env.HookResult.PluginID
		if env.HookResult.Error != "" {
			p.lastResult = env.HookResult.Error
		}
		return p.render()
	case "hook_call":
		if !p.contributor || env.HookCall == nil {
			return nil
		}
		return p.out.Send(huskipc.Envelope{Type: "hook_result", HookResult: &huskipc.HookResultPayload{
			ID: env.HookCall.ID, RequestID: env.HookCall.RequestID, Data: map[string]any{"ready": true},
		}})
	case "event_emit":
		if env.EventEmit != nil {
			p.lastEvent = fmt.Sprint(env.EventEmit.Data["row"])
			fmt.Fprintf(p.log, "reference: event %s row=%s\n", env.EventEmit.Topic, p.lastEvent)
		}
		return nil
	case "command_invoke":
		if !p.contributor || env.CommandInvoke == nil || env.CommandInvoke.CommandID != inspectCommand {
			return nil
		}
		return p.out.Send(huskipc.Envelope{Type: "toast", Toast: &huskipc.ToastPayload{
			PluginID: contributorID, Level: "info", Title: "Inspect",
			Message: "row " + env.CommandInvoke.Args["row_key"],
		}})
	case "protocol_error":
		// Log it and never answer: two strict peers answering each other
		// would loop. A contributor that raced the publisher tries again.
		if e := env.ProtocolError; e != nil {
			fmt.Fprintf(p.log, "reference: host rejected %q: %s\n", e.MessageType, e.Message)
			if p.contributor && p.retries < maxRetries {
				p.retries++
				retry()
			}
		}
	}
	// Every other message, key_state and slot_contributions included, is
	// deliberately ignored: Husk expands the anchors itself, and a plugin
	// re-renders only when what it shows changes.
	return nil
}

func (p *plugin) resize(sizes map[huskipc.SlotID]huskipc.SlotSize) {
	if s, ok := sizes[stage]; ok && s.Width > 0 && s.Height > 0 {
		p.width, p.height = s.Width, s.Height
	}
}

func (p *plugin) key(key string) error {
	switch key {
	case "j", "down":
		p.selected = (p.selected + 1) % len(rows)
	case "k", "up":
		p.selected = (p.selected + len(rows) - 1) % len(rows)
	case "r":
		p.requests++
		return p.out.Send(huskipc.Envelope{Type: "hook_call", HookCall: &huskipc.HookCallPayload{
			ID: refreshHook, RequestID: fmt.Sprintf("%s.refresh-%d", publisherID, p.requests),
			Data: map[string]any{"reason": "manual"},
		}})
	default:
		return nil
	}
	if err := p.out.Send(huskipc.Envelope{Type: "event_emit", EventEmit: &huskipc.EventPayload{
		PluginID: publisherID, Topic: selectionEvent, Data: map[string]any{"row": rows[p.selected]},
	}}); err != nil {
		return err
	}
	return p.render()
}

// lines builds the surface content for the current size. Row actions are OSC
// anchors; Husk replaces each with the slot's contributions.
func (p *plugin) lines() []string {
	width := p.width
	if width <= 0 {
		width = 40 // before the first size: the host clips to the real rect
	}
	out := []string{
		clip("Reference plugin: frame, published slot, hook, event", width),
		strings.Repeat("─", width),
	}
	for i, row := range rows {
		marker := "  "
		if i == p.selected {
			marker = "> "
		}
		out = append(out, clip(marker+row, width-12)+"  \x1b]husk;slot="+rowActionsSlot+";key="+row+"\x1b\\")
	}
	result := "none"
	if p.results > 0 {
		result = fmt.Sprintf("%d, last %s", p.results, p.lastResult)
	}
	return append(out,
		"",
		clip(fmt.Sprintf("size %dx%d · replies: %s", p.width, p.height, result), width),
		clip("j/k select (event)  r refresh (hook)", width),
	)
}

// clip cuts s to at most width runes. The lines here carry no ANSI.
func clip(s string, width int) string {
	if width < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}

func (p *plugin) render() error {
	return p.out.Send(huskipc.Envelope{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{
		PluginID: publisherID, Slot: stage, Title: "Reference plugin", Lines: p.lines(),
	}})
}
