// Command reference is Husk's reference plugin. It is deliberately small and
// exercises every extension point a v2 plugin can use:
//
//   - a frame (border, label and close declared on the surface in the manifest),
//   - a published inline slot (row actions anchored in surface content),
//   - a gate hook the plugin fires before it refreshes (from the r key or the
//     go.reference.refresh action) and an event it emits,
//   - surface.resized for geometry, so nothing is hard-coded,
//   - a declared secret (go.reference.token) and an action
//     (go.reference.secret-check) that reads it with secrets.get and replies
//     with whether it is set, never the value.
//
// It runs in one of two roles from the same binary, because a plugin cannot
// contribute to its own published slot:
//
//	reference                 the publisher: occupies the surfaces its manifest
//	                          claims, publishes the slots, hook and event
//	reference --contributor   fills the publisher's slots, answers its hook and
//	                          listens to its event
//
// Transport (protocol 2.0, no handshake): Husk writes requests and
// notifications to the plugin's stdin; the plugin's stdout carries replies
// only. Everything the plugin starts itself (surface.update, slot.contribute,
// event.emit, hook.call, toast.show) goes over the Unix socket named by
// HUSK_SOCKET_PATH, with HUSK_PLUGIN_TOKEN as the token member of each
// message. Declarations live in the manifests, not on the wire. Diagnostics go
// to stderr, which Husk keeps in the plugin's log file.
//
// The wire types come from pkg/protocol, the only Husk package this plugin
// imports.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// IDs follow the <plugin_id>.<name> rule: the final dot separates the plugin
// ID from one dot-free name.
const (
	publisherID   = "go.reference"
	contributorID = "go.reference-actions"

	rowActionsSlot = publisherID + ".row-actions"
	refreshHook    = publisherID + ".before-refresh"
	selectionEvent = publisherID + ".selection-changed"

	refreshAction = publisherID + ".refresh"
	gateKind      = "gate"

	secretName        = publisherID + ".token"
	secretCheckAction = publisherID + ".secret-check"

	inspectAction  = contributorID + ".inspect"
	inspectContrib = contributorID + ".inspect-button"
)

// surfaces are the host surfaces the publisher's manifest claims.
var surfaces = []protocol.SurfaceID{protocol.SurfaceMainStage}

var rows = []string{"alpha", "bravo", "charlie"}

// retryDelay paces setup retries. A contributor can start before the
// publisher's declarations are in the catalog; the host refuses the
// contribution and the contributor tries again.
const retryDelay = 100 * time.Millisecond

// maxRetries bounds the retries so a missing publisher cannot loop forever.
const maxRetries = 50

func main() {
	contributor := len(os.Args) > 1 && os.Args[1] == "--contributor"
	if err := launch(contributor); err != nil {
		fmt.Fprintln(os.Stderr, "reference:", err)
		os.Exit(1)
	}
}

// launch reads the environment the host provides and runs until stdin closes.
// Neither variable has a default: without them nothing can be rendered.
func launch(contributor bool) error {
	path, token := os.Getenv("HUSK_SOCKET_PATH"), os.Getenv("HUSK_PLUGIN_TOKEN")
	if path == "" || token == "" {
		return errors.New("HUSK_SOCKET_PATH and HUSK_PLUGIN_TOKEN must be set by the host")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	return run(config{
		contributor: contributor,
		token:       token,
		after:       func(f func()) { time.AfterFunc(retryDelay, f) },
	}, os.Stdin, os.Stdout, conn, os.Stderr)
}

// config is everything run needs besides its four streams.
type config struct {
	contributor bool
	token       string
	// after runs f once, later, on any goroutine. It paces setup retries and
	// is injected so tests never sleep.
	after func(f func())
}

type pendingKind int

const (
	pendingContribute pendingKind = iota + 1
	pendingHook
	pendingEmit
	pendingSecret
)

// plugin is single-threaded: run feeds it one message at a time.
type plugin struct {
	cfg    config
	stdout *protocol.Writer // replies only
	sock   *protocol.Writer // everything the plugin initiates
	log    io.Writer

	nextID  int64
	pending map[string]pendingKind
	// secretChecks holds the secret-check requests waiting on secrets.get,
	// keyed like pending.
	secretChecks map[string]protocol.Message

	sizes    map[protocol.SurfaceID]protocol.SurfaceSize
	selected int
	answered bool   // a hook.call reply arrived
	status   string // what the last refresh gate decided, shown in the status line

	// Text mode is per surface: `i` on a surface enters it there, enter or esc
	// leaves it. While a surface is in it every frame for that surface carries
	// capture=true so the host's value is the same whatever frame arrives
	// last; the first frame after leaving carries capture=false once. Outside
	// both, frames omit the field (absent means unchanged).
	textMode map[protocol.SurfaceID]bool
	sendOff  map[protocol.SurfaceID]bool
	typed    []rune // keys typed in text mode, kept after leaving it
	typedAny bool   // a text mode ran, so the echo line is shown

	arm     func()
	retries int
	gaveUp  bool
}

type source int

const (
	fromHost source = iota + 1
	fromSocket
)

type input struct {
	from source
	msg  protocol.Message
	done bool // the stream ended; msg is zero
	err  error
}

// run drives one plugin until stdin reaches EOF. stdin is the host's
// requests and notifications, stdout takes replies, and sock is the connection
// to the host's instance socket.
func run(cfg config, stdin io.Reader, stdout io.Writer, sock io.ReadWriteCloser, errw io.Writer) error {
	p := &plugin{
		cfg:          cfg,
		stdout:       protocol.NewWriter(stdout),
		sock:         protocol.NewWriter(sock),
		log:          errw,
		pending:      map[string]pendingKind{},
		secretChecks: map[string]protocol.Message{},
		sizes:        map[protocol.SurfaceID]protocol.SurfaceSize{},
	}

	in := make(chan input)
	stop := make(chan struct{})
	defer close(stop)
	pump := func(from source, r io.Reader) {
		send := func(i input) {
			select {
			case in <- i:
			case <-stop:
			}
		}
		err := protocol.ReadLoop(r,
			func(m protocol.Message) { send(input{from: from, msg: m}) },
			func(le *protocol.LineError) { fmt.Fprintf(errw, "reference: unreadable line: %v\n", le) })
		send(input{from: from, done: true, err: err})
	}
	go pump(fromHost, stdin)
	go pump(fromSocket, sock)

	tick := make(chan struct{}, 1)
	p.arm = func() {
		cfg.after(func() {
			select {
			case tick <- struct{}{}:
			default:
			}
		})
	}

	if cfg.contributor {
		if err := p.setup(); err != nil {
			return err
		}
	}
	for {
		select {
		case i := <-in:
			if i.done {
				if i.from == fromHost {
					return i.err
				}
				return fmt.Errorf("instance socket closed: %v", i.err)
			}
			var err error
			if i.from == fromHost {
				err = p.fromHost(i.msg)
			} else {
				err = p.fromSocket(i.msg)
			}
			if err != nil {
				return err
			}
		case <-tick:
			if err := p.setup(); err != nil {
				return err
			}
		}
	}
}

// ---- sending

func (p *plugin) sendSock(m protocol.Message) error {
	m.Token = p.cfg.token
	return p.sock.Send(m)
}

func (p *plugin) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return p.sendSock(protocol.Message{Method: method, Params: raw})
}

// call sends a request on the socket and remembers what the reply is for.
func (p *plugin) call(kind pendingKind, method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	p.nextID++
	id, err := protocol.NewIntID(p.nextID)
	if err != nil {
		return err
	}
	p.pending[id.String()] = kind
	return p.sendSock(protocol.Message{ID: &id, Method: method, Params: raw})
}

func (p *plugin) reply(req protocol.Message, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return p.stdout.Send(protocol.Message{ID: req.ID, Result: raw})
}

func (p *plugin) replyError(req protocol.Message, code protocol.Code, msg string) error {
	return p.stdout.Send(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: code, Message: msg}})
}

// ---- contributor setup

// setup is the contributor's work that depends on the publisher's slots being
// in the host catalog. It is safe to repeat: contributions replace by id.
func (p *plugin) setup() error {
	return p.call(pendingContribute, protocol.MethodSlotContribute, protocol.SlotContributeParams{
		SlotID: rowActionsSlot, ID: inspectContrib, Content: "\x1b[32m[inspect]\x1b[0m", OnClick: inspectAction,
	})
}

// ---- host to plugin (stdin)

func (p *plugin) fromHost(m protocol.Message) error {
	kind, err := m.Kind()
	if err != nil {
		return nil // ReadLoop already validated; nothing to answer
	}
	switch kind {
	case protocol.KindRequest:
		return p.request(m)
	case protocol.KindNotification:
		return p.notification(m)
	}
	// A reply on stdin answers nothing this plugin sent there; replies are
	// never answered.
	fmt.Fprintln(p.log, "reference: unexpected reply on stdin")
	return nil
}

func (p *plugin) request(m protocol.Message) error {
	switch m.Method {
	case protocol.MethodActionInvoke:
		var a struct {
			Action string         `json:"action"`
			Args   map[string]any `json:"args"`
		}
		if err := json.Unmarshal(m.Params, &a); err != nil {
			return p.replyError(m, protocol.CodeInvalidParams, "action.invoke params: "+err.Error())
		}
		if !p.cfg.contributor && a.Action == refreshAction {
			if err := p.refresh(); err != nil {
				return err
			}
			return p.reply(m, struct{}{})
		}
		if !p.cfg.contributor && a.Action == secretCheckAction {
			return p.secretCheck(m)
		}
		if !p.cfg.contributor || a.Action != inspectAction {
			return p.replyError(m, protocol.CodeNotFound, fmt.Sprintf("no action %q", a.Action))
		}
		// args is an open object; row_key is the only key this plugin reads and
		// a non-string value is treated as absent rather than failing the call.
		row, _ := a.Args["row_key"].(string)
		if err := p.notify(protocol.MethodToastShow, protocol.ToastShowParams{
			Level: "info", Title: "Inspect", Message: "row " + row,
		}); err != nil {
			return err
		}
		return p.reply(m, struct{}{})
	case protocol.MethodHookCall:
		var h struct {
			Hook string `json:"hook"`
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(m.Params, &h); err != nil {
			return p.replyError(m, protocol.CodeInvalidParams, "hook.call params: "+err.Error())
		}
		if !p.cfg.contributor || h.Hook != refreshHook {
			return p.replyError(m, protocol.CodeNotFound, fmt.Sprintf("not subscribed to %q", h.Hook))
		}
		// The leg names the kind the owner reads. This hook is a gate, and an
		// empty result allows; any other kind is answered the same way (no
		// data, no items, no veto) so the owner is never blocked.
		if h.Kind != gateKind {
			fmt.Fprintf(p.log, "reference: %s asked as %q, expected a gate\n", refreshHook, h.Kind)
		}
		return p.reply(m, struct{}{})
	}
	return p.replyError(m, protocol.CodeUnknownMethod, fmt.Sprintf("unknown method %q", m.Method))
}

// secretCheck asks the host for the declared secret and answers the action once
// the read is decided. The value is never replied or logged. Answer the dialog
// within the host's 10 s action timeout: the dialog itself can wait 60 s, but a
// late action reply is ignored. A longer CLI --timeout (for example 70s) only
// extends the client's wait, not the host's action deadline; use esc to exercise
// dismissal before that deadline.
func (p *plugin) secretCheck(req protocol.Message) error {
	raw, err := json.Marshal(struct {
		Name string `json:"name"`
	}{secretName})
	if err != nil {
		return err
	}
	p.nextID++
	id, err := protocol.NewIntID(p.nextID)
	if err != nil {
		return err
	}
	p.pending[id.String()] = pendingSecret
	p.secretChecks[id.String()] = req
	return p.sendSock(protocol.Message{ID: &id, Method: protocol.MethodSecretsGet, Params: raw})
}

func (p *plugin) notification(m protocol.Message) error {
	switch m.Method {
	case protocol.MethodSurfaceResized:
		if p.cfg.contributor {
			return nil
		}
		var r protocol.SurfaceResizedParams
		if err := json.Unmarshal(m.Params, &r); err != nil {
			fmt.Fprintf(p.log, "reference: bad surface.resized: %v\n", err)
			return nil
		}
		return p.resized(r)
	case protocol.MethodInputKey:
		if p.cfg.contributor {
			return nil
		}
		var k protocol.SurfaceKeyParams
		if err := json.Unmarshal(m.Params, &k); err != nil || k.Validate() != nil || !ours(k.Surface) {
			return nil
		}
		return p.key(k.Surface, k.Key)
	case protocol.MethodEventDeliver:
		var e struct {
			Name string         `json:"name"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(m.Params, &e) == nil {
			fmt.Fprintf(p.log, "reference: event %s row=%v\n", e.Name, e.Data["row"])
		}
	case protocol.MethodProtocolError:
		p.logProtocolError(m)
	}
	// Everything else (theme.changed, input.mouse, input.key_state,
	// slot.contributions) is deliberately ignored: Husk expands the row
	// anchors itself, and a plugin re-renders only when what it shows changes.
	return nil
}

// ---- host to plugin (socket)

func (p *plugin) fromSocket(m protocol.Message) error {
	kind, err := m.Kind()
	if err != nil {
		return nil
	}
	switch kind {
	case protocol.KindReply:
		return p.complete(m)
	case protocol.KindNotification:
		if m.Method == protocol.MethodProtocolError {
			p.logProtocolError(m)
		}
	}
	// Never answer: two strict peers answering each other would loop.
	return nil
}

func (p *plugin) logProtocolError(m protocol.Message) {
	var e protocol.ProtocolErrorParams
	if json.Unmarshal(m.Params, &e) == nil {
		fmt.Fprintf(p.log, "reference: host rejected %q: %s: %s\n", e.Method, e.Code, e.Message)
	}
}

// complete completes the pending request the reply belongs to.
func (p *plugin) complete(m protocol.Message) error {
	key := m.ID.String()
	kind, ok := p.pending[key]
	if !ok {
		fmt.Fprintf(p.log, "reference: reply %s matches no request\n", key)
		return nil
	}
	delete(p.pending, key)
	switch kind {
	case pendingSecret:
		req := p.secretChecks[key]
		delete(p.secretChecks, key)
		if m.Error == nil {
			return p.reply(req, map[string]any{"set": true})
		}
		var data struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(m.Error.Data, &data)
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		return p.stdout.Send(protocol.Message{ID: req.ID, Error: &protocol.Error{
			Code: m.Error.Code, Message: "secret read failed: " + string(m.Error.Code), Data: raw,
		}})
	case pendingContribute:
		if m.Error == nil {
			return nil
		}
		fmt.Fprintf(p.log, "reference: host refused a contribution: %s: %s\n", m.Error.Code, m.Error.Message)
		p.retry()
	case pendingEmit:
		if m.Error != nil {
			fmt.Fprintf(p.log, "reference: event refused: %s: %s\n", m.Error.Code, m.Error.Message)
		}
	case pendingHook:
		p.answered = true
		p.status = p.gateStatus(m)
		return p.renderAll()
	}
	return nil
}

// gateStatus turns the reply to a refresh hook.call into the status line text.
// Only the members this plugin reads are decoded; unknown ones are ignored.
func (p *plugin) gateStatus(m protocol.Message) string {
	if m.Error != nil {
		return "refresh not asked: " + string(m.Error.Code)
	}
	var out struct {
		Kind    string `json:"kind"`
		Allowed *bool  `json:"allowed"`
		Vetoes  []struct {
			PluginID string `json:"plugin_id"`
			Reason   string `json:"reason"`
		} `json:"vetoes"`
		Results []struct {
			Error *protocol.Error `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(m.Result, &out); err != nil {
		fmt.Fprintf(p.log, "reference: bad hook.call result: %v\n", err)
		return "refresh outcome unreadable"
	}
	// Anything that is not a well-formed gate outcome is unreadable, never a
	// veto: a broken answer must not silently block a refresh.
	if out.Kind != gateKind {
		return fmt.Sprintf("refresh outcome of kind %q", out.Kind)
	}
	if out.Allowed == nil {
		fmt.Fprintln(p.log, "reference: gate outcome without allowed")
		return "refresh outcome unreadable"
	}
	if !*out.Allowed {
		if len(out.Vetoes) == 0 {
			return "refresh vetoed"
		}
		v := out.Vetoes[0]
		more := ""
		if n := len(out.Vetoes) - 1; n > 0 {
			more = fmt.Sprintf(" (+%d more)", n)
		}
		return fmt.Sprintf("refresh vetoed by %s: %s%s", v.PluginID, v.Reason, more)
	}
	asked := 0
	for _, r := range out.Results {
		if r.Error == nil {
			asked++
		}
	}
	return fmt.Sprintf("refresh allowed by %d", asked)
}

// retry arms one setup retry unless the budget is spent.
func (p *plugin) retry() {
	if p.gaveUp {
		return
	}
	if p.retries >= maxRetries {
		p.gaveUp = true
		fmt.Fprintf(p.log, "reference: giving up on contributing after %d retries\n", maxRetries)
		return
	}
	p.retries++
	p.arm()
}

// ---- publisher: state and rendering

func ours(s protocol.SurfaceID) bool {
	for _, o := range surfaces {
		if o == s {
			return true
		}
	}
	return false
}

func (s sizeKey) usable() bool { return s.visible && s.w > 0 && s.h > 0 }

type sizeKey struct {
	w, h    int
	visible bool
}

func keyOf(s protocol.SurfaceSize) sizeKey { return sizeKey{s.Width, s.Height, s.Visible} }

// resized stores the new geometry and renders each surface whose size or
// visibility changed; a focus-only change renders nothing.
func (p *plugin) resized(r protocol.SurfaceResizedParams) error {
	for _, s := range r.Surfaces {
		if !ours(s.Surface) {
			continue
		}
		old := keyOf(p.sizes[s.Surface])
		p.sizes[s.Surface] = s
		if now := keyOf(s); now != old && now.usable() {
			if err := p.render(s.Surface); err != nil {
				return err
			}
		}
	}
	return nil
}

// refresh fires the gate hook that precedes a refresh; the r key and the
// refresh action share it.
func (p *plugin) refresh() error {
	return p.call(pendingHook, protocol.MethodHookCall, map[string]any{
		"hook": refreshHook, "data": map[string]any{"reason": "manual"},
	})
}

func (p *plugin) key(s protocol.SurfaceID, key string) error {
	if p.textMode[s] {
		return p.typeKey(s, key)
	}
	switch key {
	case "i":
		if p.textMode == nil {
			p.textMode = map[protocol.SurfaceID]bool{}
		}
		p.textMode[s], p.typedAny, p.typed = true, true, nil
		return p.renderAll()
	case "j", "down":
		p.selected = (p.selected + 1) % len(rows)
	case "k", "up":
		p.selected = (p.selected + len(rows) - 1) % len(rows)
	case "r":
		return p.refresh()
	default:
		return nil
	}
	if err := p.call(pendingEmit, protocol.MethodEventEmit, map[string]any{
		"name": selectionEvent, "data": map[string]any{"row": rows[p.selected]},
	}); err != nil {
		return err
	}
	return p.renderAll()
}

// typeKey handles a key in text mode: enter or esc leaves it, backspace
// deletes, a single rune (the wire space included) is echoed, anything else is
// ignored. Every case re-renders so the frame carries the echo and the bit.
func (p *plugin) typeKey(s protocol.SurfaceID, key string) error {
	switch r := []rune(key); {
	case key == "enter" || key == "esc":
		delete(p.textMode, s)
		if p.sendOff == nil {
			p.sendOff = map[protocol.SurfaceID]bool{}
		}
		p.sendOff[s] = true
	case key == "backspace":
		if len(p.typed) > 0 {
			p.typed = p.typed[:len(p.typed)-1]
		}
	case len(r) == 1:
		p.typed = append(p.typed, r[0])
	default:
		return nil
	}
	return p.renderAll()
}

// captureField is the capture member of the next frame for surface s: true
// while s is in text mode, false once after leaving it, otherwise absent.
func (p *plugin) captureField(s protocol.SurfaceID) *bool {
	switch {
	case p.textMode[s]:
		on := true
		return &on
	case p.sendOff[s]:
		delete(p.sendOff, s)
		off := false
		return &off
	}
	return nil
}

// lines builds the surface content for one surface size. Row actions are OSC
// anchors; Husk replaces each with the slot's contributions.
func (p *plugin) lines(size protocol.SurfaceSize) []string {
	width := size.Width
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
	if p.answered {
		result = p.status
	}
	out = append(out,
		"",
		clip(fmt.Sprintf("size %dx%d · replies: %s", size.Width, size.Height, result), width),
		clip("j/k select (event)  r refresh (hook)  i type", width),
	)
	if p.typedAny {
		out = append(out, clip("> "+string(p.typed), width))
	}
	return out
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

// render draws one surface, if the host has told us it is visible.
func (p *plugin) render(s protocol.SurfaceID) error {
	size := p.sizes[s]
	if !keyOf(size).usable() {
		return nil
	}
	return p.notify(protocol.MethodSurfaceUpdate, protocol.SurfaceUpdateParams{
		Surface: s, Title: "Reference plugin", Lines: p.lines(size), Capture: p.captureField(s),
	})
}

func (p *plugin) renderAll() error {
	for _, s := range surfaces {
		if err := p.render(s); err != nil {
			return err
		}
	}
	return nil
}
