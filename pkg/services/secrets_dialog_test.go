package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/services/secrets"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

const sentinel = "SENTINEL-v4lue-é-do-not-leak"

var (
	cliCaller   = Caller{Kind: protocol.CallerCLI}
	hostCaller  = Caller{Kind: protocol.CallerHost}
	inputCaller = Caller{Kind: protocol.CallerInput}
)

func pluginCaller(id string) Caller { return Caller{Kind: protocol.CallerPlugin, PluginID: id} }

// promptCall is one dialog the fake shows: the test answers it through reply.
type promptCall struct {
	p      secrets.Prompt
	reply  chan secrets.Answer
	closed chan struct{} // closed when the service cancels the prompt's ctx
}

type event struct{ Name, PluginID, Outcome string }

type dialogJoin struct {
	plugin  string
	waiters int
	queued  bool
}

// grantTree can lower the real State cap and retain its write capability for
// storage-failure tests. Reads are counted to pin Names' single grant decode.
type grantTree struct {
	*state.State
	maxBytes int
	handle   *state.Handle
	reads    atomic.Int32
}

func (s *grantTree) Register(spec state.Spec) (*state.Handle, error) {
	if spec.Namespace == state.GrantsNamespace {
		spec.MaxBytes = s.maxBytes
	}
	h, err := s.State.Register(spec)
	s.handle = h
	return h, err
}

func (s *grantTree) Snapshot() *state.Snapshot {
	s.reads.Add(1)
	return s.State.Snapshot()
}

// secretRig is a Secrets service with a blocking prompt fake, a recording publisher
// and audit hook, and the package's manual clock.
type secretRig struct {
	t       *testing.T
	s       *secrets.Service
	clk     *manualClock
	crypto  *memoryCrypto
	shown   chan *promptCall
	mu      sync.Mutex
	events  []event
	audits  []string
	tree    *grantTree
	joins   chan dialogJoin
	prompts atomic.Int32
	conc    atomic.Int32
	maxc    atomic.Int32
}

type rigOpts struct {
	noPrompt bool
	post     func(func()) bool
	crypto   *memoryCrypto
	maxBytes int
}

func newSecretRig(t *testing.T, o rigOpts) *secretRig {
	t.Helper()
	r := &secretRig{
		t: t, tree: &grantTree{State: state.New(state.Options{}), maxBytes: o.maxBytes},
		clk: clock(), shown: make(chan *promptCall, 16), crypto: o.crypto,
		joins: make(chan dialogJoin, 64),
	}
	if r.crypto == nil {
		r.crypto = &memoryCrypto{data: map[string][]byte{"git.token": []byte(sentinel)}}
	}
	post := o.post
	if post == nil {
		post = func(f func()) bool { f(); return true }
	}
	opts := secrets.Options{
		State: r.tree, Post: post, Clock: r.clk, Crypto: r.crypto,
		Publisher: c.PublishFunc(func(n string, d any) {
			m := d.(map[string]string)
			if n != "secrets.read" {
				t.Errorf("event %q", n)
			}
			r.mu.Lock()
			r.events = append(r.events, event{m["name"], m["plugin_id"], m["outcome"]})
			r.mu.Unlock()
		}),
		Audit: func(name string) { r.mu.Lock(); r.audits = append(r.audits, name); r.mu.Unlock() },
		WaiterJoined: func(plugin string, waiters int, queued bool) {
			r.joins <- dialogJoin{plugin, waiters, queued}
		},
	}
	if !o.noPrompt {
		opts.Prompt = func(ctx context.Context, p secrets.Prompt) secrets.Answer {
			r.prompts.Add(1)
			n := r.conc.Add(1)
			defer r.conc.Add(-1)
			for {
				m := r.maxc.Load()
				if n <= m || r.maxc.CompareAndSwap(m, n) {
					break
				}
			}
			pc := &promptCall{p: p, reply: make(chan secrets.Answer), closed: make(chan struct{})}
			r.shown <- pc
			select {
			case a := <-pc.reply:
				return a
			case <-ctx.Done():
				close(pc.closed)
				return secrets.Dismissed
			}
		}
	}
	s, err := secrets.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	r.s = s
	t.Cleanup(s.Close)
	return r
}

func (r *secretRig) declare(plugins ...string) { apply(r.t, r.s, catalogs(plugins...)) }

// catalogs declares git.token (required, purpose "Push") for each plugin.
func catalogs(plugins ...string) ReloadInput {
	in := input(`{}`)
	in.Declared[store.DeclaredPlugins] = map[string]store.Doc{}
	for _, p := range plugins {
		in.Declared[store.DeclaredPlugins][p] = doc(fmt.Sprintf(`{"id":%q,"name":"Display %s","secret_requests":[{"name":"git.token","purpose":"Push","required":true}]}`, p, p))
	}
	return in
}

// catalogWith declares the given secret_requests JSON for one plugin.
func catalogWith(plugin, requests string) ReloadInput {
	in := input(`{}`)
	in.Declared[store.DeclaredPlugins] = map[string]store.Doc{plugin: doc(fmt.Sprintf(`{"id":%q,"name":"Display","secret_requests":%s}`, plugin, requests))}
	return in
}

func (r *secretRig) next() *promptCall {
	r.t.Helper()
	return <-r.shown
}

func (r *secretRig) joined(plugin string, waiters int, queued bool) {
	r.t.Helper()
	want := dialogJoin{plugin, waiters, queued}
	for got := range r.joins {
		if got == want {
			return
		}
	}
	r.t.Fatal("dialog join stream closed")
}

func (r *secretRig) log() []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event(nil), r.events...)
}

func (r *secretRig) lastEvent() event {
	r.t.Helper()
	l := r.log()
	if len(l) == 0 {
		r.t.Fatal("no event published")
	}
	return l[len(l)-1]
}

type readResult struct {
	value string
	err   error
}

func (r *secretRig) readAsync(ctx context.Context, plugin, name string) <-chan readResult {
	out := make(chan readResult, 1)
	go func() {
		v, err := r.s.Read(ctx, pluginCaller(plugin), name)
		out <- readResult{v, err}
	}()
	return out
}

func (r *secretRig) read(plugin, name string) readResult {
	v, err := r.s.Read(context.Background(), pluginCaller(plugin), name)
	return readResult{v, err}
}

// TestReadBlocksOnTheDialog drives an ungranted plugin read through every way
// the dialog can end, and what each leaves behind.
func TestReadBlocksOnTheDialog(t *testing.T) {
	for _, tc := range []struct {
		name         string
		answer       func(r *secretRig, pc *promptCall)
		wantValue    bool
		wantErr      string
		wantEvent    string
		reprompts    bool // a second read shows a dialog again (nothing remembered)
		granted      bool
		noPromptSeam bool
	}{
		{name: "accept", answer: func(r *secretRig, pc *promptCall) { pc.reply <- secrets.Accepted }, wantValue: true, wantEvent: "allowed", granted: true},
		{name: "decline", answer: func(r *secretRig, pc *promptCall) { pc.reply <- secrets.Declined }, wantErr: "denied/user_denied", wantEvent: "user_denied"},
		{name: "dismiss", answer: func(r *secretRig, pc *promptCall) { pc.reply <- secrets.Dismissed }, wantErr: "denied/user_denied", wantEvent: "user_denied", reprompts: true},
		{name: "timeout", answer: func(r *secretRig, pc *promptCall) { r.clk.advance(secrets.DialogTimeout) }, wantErr: "denied/user_denied", wantEvent: "user_denied", reprompts: true},
		{name: "headless", noPromptSeam: true, wantErr: "denied/user_denied", wantEvent: "user_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSecretRig(t, rigOpts{noPrompt: tc.noPromptSeam})
			r.declare("a")
			if tc.noPromptSeam {
				res := r.read("a", "git.token")
				if reason(t, res.err) != tc.wantErr || res.value != "" || r.lastEvent().Outcome != tc.wantEvent {
					t.Fatalf("headless read = %+v %v", res, r.log())
				}
				if res2 := r.read("a", "git.token"); reason(t, res2.err) != tc.wantErr {
					t.Fatalf("second headless read = %+v", res2)
				}
				return
			}
			got := r.readAsync(context.Background(), "a", "git.token")
			pc := r.next()
			if pc.p.Plugin != "a" || pc.p.PluginName != "Display a" || pc.p.Name != "git.token" || pc.p.Purpose != "Push" || !pc.p.Required {
				t.Fatalf("prompt = %+v", pc.p)
			}
			select {
			case res := <-got:
				t.Fatalf("read returned before the dialog was answered: %+v", res)
			default:
			}
			tc.answer(r, pc)
			res := <-got
			if tc.wantValue != (res.value == sentinel) || reason(t, res.err) != tc.wantErr {
				t.Fatalf("read = %+v (%v)", res, res.err)
			}
			if ev := r.lastEvent(); ev != (event{"git.token", "a", tc.wantEvent}) {
				t.Fatalf("event = %+v", ev)
			}
			// A second read: granted reads straight through, a decline is remembered
			// (no dialog), anything else asks again.
			again := r.readAsync(context.Background(), "a", "git.token")
			if tc.reprompts {
				pc2 := r.next()
				pc2.reply <- secrets.Dismissed
			}
			res2 := <-again
			switch {
			case tc.granted:
				if res2.value != sentinel {
					t.Fatalf("granted read = %+v", res2)
				}
			default:
				if reason(t, res2.err) != "denied/user_denied" {
					t.Fatalf("second read = %+v", res2)
				}
			}
			if !tc.reprompts && len(r.shown) != 0 {
				t.Fatal("a dialog was shown for a remembered or granted read")
			}
		})
	}
}

// A CLI grant or a digest change clears the memory of a decline.
func TestDeclineMemoryClearedByGrantAndDigestChange(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	r.declare("a")
	got := r.readAsync(context.Background(), "a", "git.token")
	r.next().reply <- secrets.Declined
	<-got
	if res := r.read("a", "git.token"); reason(t, res.err) != "denied/user_denied" || len(r.shown) != 0 {
		t.Fatalf("decline not remembered: %+v", res)
	}
	// A new purpose is a new digest: the decline no longer applies.
	apply(t, r.s, catalogWith("a", `[{"name":"git.token","purpose":"Fetch","required":true}]`))
	got = r.readAsync(context.Background(), "a", "git.token")
	pc := r.next()
	if pc.p.Purpose != "Fetch" {
		t.Fatalf("prompt = %+v", pc.p)
	}
	pc.reply <- secrets.Declined
	<-got
	if res := r.read("a", "git.token"); reason(t, res.err) != "denied/user_denied" || len(r.shown) != 0 {
		t.Fatalf("second decline not remembered: %+v", res)
	}
	// A CLI grant clears it and lets the read through.
	if err := r.s.Grant(context.Background(), cliCaller, "a", "git.token"); err != nil {
		t.Fatal(err)
	}
	if res := r.read("a", "git.token"); res.value != sentinel {
		t.Fatalf("read after grant = %+v", res)
	}
	// Revoking then asks again: the decline did not survive the grant.
	if err := r.s.Revoke(context.Background(), cliCaller, "a", "git.token"); err != nil {
		t.Fatal(err)
	}
	got = r.readAsync(context.Background(), "a", "git.token")
	r.next().reply <- secrets.Dismissed
	<-got
}

// Dialogs are shown one at a time in request order, each with its own 60 s
// clock that starts when it is shown.
func TestDialogsQueueInOrderWithTimersStartingWhenShown(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	r.declare("a", "b")
	ra := r.readAsync(context.Background(), "a", "git.token")
	pa := r.next()
	if pa.p.Plugin != "a" {
		t.Fatalf("first = %+v", pa.p)
	}
	rb := r.readAsync(context.Background(), "b", "git.token")
	r.joined("b", 1, true)
	r.clk.advance(30e9)
	pa.reply <- secrets.Accepted
	if res := <-ra; res.value != sentinel {
		t.Fatalf("a = %+v", res)
	}
	pb := r.next()
	if pb.p.Plugin != "b" {
		t.Fatalf("second = %+v", pb.p)
	}
	// b's minute started when it was shown (+30s), not when it was requested.
	ta, tb := <-r.clk.created, <-r.clk.created
	if shownAt := r.clk.Now(); !tb.at.Equal(shownAt.Add(secrets.DialogTimeout)) || tb.at.Equal(ta.at) {
		t.Fatalf("b's timer fires at %v, want show time %v + 60s", tb.at, shownAt)
	}
	// +45s later the minute has not elapsed: the accept still wins, which it
	// could not if the timer had fired at request time (+60s).
	r.clk.advance(45e9)
	pb.reply <- secrets.Accepted
	if res := <-rb; res.value != sentinel {
		t.Fatalf("b = %+v", res)
	}
	if r.maxc.Load() != 1 {
		t.Fatalf("%d dialogs were shown at once", r.maxc.Load())
	}
}

// Concurrent reads of one triple share one dialog; a different ungranted name
// from the same plugin is busy/dialog_pending at once.
func TestDialogSharedAndPerPluginBusy(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	apply(t, r.s, catalogWith("a", `[{"name":"git.token","purpose":"Push","required":true},{"name":"other.key","purpose":"Other"}]`))
	r.crypto.data["other.key"] = []byte("o")
	first := r.readAsync(context.Background(), "a", "git.token")
	pc := r.next()
	second := r.readAsync(context.Background(), "a", "git.token")
	r.joined("a", 2, false)
	busy := r.read("a", "other.key")
	if reason(t, busy.err) != "busy/dialog_pending" {
		t.Fatalf("other name = %+v", busy)
	}
	if ev := r.lastEvent(); ev != (event{"other.key", "a", "dialog_pending"}) {
		t.Fatalf("event = %+v", ev)
	}
	pc.reply <- secrets.Accepted
	for _, ch := range []<-chan readResult{first, second} {
		if res := <-ch; res.value != sentinel {
			t.Fatalf("shared read = %+v", res)
		}
	}
	if len(r.shown) != 0 {
		t.Fatal("a second dialog was shown for the same triple")
	}
}

// A CLI grant while the dialog is up closes it and the read continues.
func TestCLIGrantClosesPendingDialog(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	r.declare("a")
	got := r.readAsync(context.Background(), "a", "git.token")
	pc := r.next()
	if err := r.s.Grant(context.Background(), cliCaller, "a", "git.token"); err != nil {
		t.Fatal(err)
	}
	<-pc.closed
	res := <-got
	if res.value != sentinel || r.lastEvent().Outcome != "allowed" {
		t.Fatalf("read = %+v %+v", res, r.log())
	}
}

// A digest change or removal on Rebuild closes the dialog as user_denied and
// remembers nothing.
func TestRebuildClosesPendingDialog(t *testing.T) {
	for name, next := range map[string]ReloadInput{
		"purpose changed": catalogWith("a", `[{"name":"git.token","purpose":"Changed"}]`),
		"removed":         catalogWith("a", `[]`),
	} {
		t.Run(name, func(t *testing.T) {
			r := newSecretRig(t, rigOpts{})
			r.declare("a")
			got := r.readAsync(context.Background(), "a", "git.token")
			pc := r.next()
			apply(t, r.s, next)
			<-pc.closed
			res := <-got
			if reason(t, res.err) != "denied/user_denied" || r.lastEvent().Outcome != "user_denied" {
				t.Fatalf("read = %+v %+v", res, r.log())
			}
			if name == "purpose changed" {
				again := r.readAsync(context.Background(), "a", "git.token")
				pc2 := r.next()
				if pc2.p.Purpose != "Changed" {
					t.Fatalf("prompt = %+v", pc2.p)
				}
				pc2.reply <- secrets.Dismissed
				<-again
			}
		})
	}
}

// A closed connection drops the read: no event, nothing remembered, and the
// dialog goes away with its last waiter.
func TestContextCancelDropsRead(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	r.declare("a")
	ctx, cancel := context.WithCancel(context.Background())
	got := r.readAsync(ctx, "a", "git.token")
	pc := r.next()
	cancel()
	res := <-got
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("err = %v", res.err)
	}
	<-pc.closed
	if len(r.log()) != 0 {
		t.Fatalf("a dropped read published %+v", r.log())
	}
	// Nothing is remembered and the plugin may ask again.
	again := r.readAsync(context.Background(), "a", "git.token")
	pc2 := r.next()
	pc2.reply <- secrets.Accepted
	if res := <-again; res.value != sentinel {
		t.Fatalf("read = %+v", res)
	}
	// A read whose ctx is already done never opens a dialog: no prompt call, no
	// timer. Both counts are synchronous with the read's return.
	if err := r.s.Revoke(context.Background(), cliCaller, "a", "git.token"); err != nil {
		t.Fatal(err)
	}
	dead, stop := context.WithCancel(context.Background())
	stop()
	promptsBefore, timersBefore := r.prompts.Load(), len(r.clk.created)
	if _, err := r.s.Read(dead, pluginCaller("a"), "git.token"); !errors.Is(err, context.Canceled) {
		t.Fatalf("dead ctx read = %v", err)
	}
	if r.prompts.Load() != promptsBefore || len(r.clk.created) != timersBefore || len(r.shown) != 0 {
		t.Fatalf("a dead-ctx read opened a dialog: prompts %d->%d timers %d->%d", promptsBefore, r.prompts.Load(), timersBefore, len(r.clk.created))
	}
}

// One of two waiters leaving keeps the dialog for the other.
func TestOneWaiterLeavingKeepsTheDialog(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	r.declare("a")
	ctx, cancel := context.WithCancel(context.Background())
	gone := r.readAsync(ctx, "a", "git.token")
	pc := r.next()
	stay := r.readAsync(context.Background(), "a", "git.token")
	r.joined("a", 2, false)
	cancel()
	<-gone
	select {
	case <-pc.closed:
		t.Fatal("one waiter leaving closed the shared dialog")
	default:
	}
	select {
	case pc.reply <- secrets.Accepted:
	case <-pc.closed:
		t.Fatal("one waiter leaving closed the shared dialog")
	}
	if res := <-stay; res.value != sentinel {
		t.Fatalf("remaining waiter = %+v", res)
	}
}

// A revoke landing between the accept and the value read does not fail the read.
func TestRevokeBetweenAcceptAndReadDoesNotFailRead(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	r.declare("a")
	revoked := make(chan struct{})
	r.crypto.onGet = func() {
		select {
		case <-revoked:
		default:
			close(revoked)
			if err := r.s.Revoke(context.Background(), cliCaller, "a", "git.token"); err != nil {
				t.Error(err)
			}
		}
	}
	got := r.readAsync(context.Background(), "a", "git.token")
	r.next().reply <- secrets.Accepted
	if res := <-got; res.value != sentinel || res.err != nil {
		t.Fatalf("read = %+v", res)
	}
	<-revoked
	r.crypto.onGet = nil
	// The revoke stood: the next read asks again.
	again := r.readAsync(context.Background(), "a", "git.token")
	r.next().reply <- secrets.Dismissed
	<-again
}

// cli get and reveal share one audited path; host and input never get a value.
func TestCLIGetAndRevealAreAudited(t *testing.T) {
	for _, via := range []string{"get", "reveal"} {
		t.Run(via, func(t *testing.T) {
			r := newSecretRig(t, rigOpts{})
			r.declare("a")
			var v string
			var err error
			if via == "get" {
				v, err = r.s.Read(context.Background(), cliCaller, "git.token")
			} else {
				v, err = r.s.Reveal(context.Background(), cliCaller, "git.token")
			}
			if err != nil || v != sentinel {
				t.Fatalf("%s = %q, %v", via, v, err)
			}
			if got := r.log(); len(got) != 1 || got[0] != (event{"git.token", "", "revealed"}) {
				t.Fatalf("events = %+v", got)
			}
			if len(r.audits) != 1 || r.audits[0] != "git.token" {
				t.Fatalf("audits = %v", r.audits)
			}
			if len(r.shown) != 0 {
				t.Fatal("a cli read opened a dialog")
			}
		})
	}
}

func TestHostAndInputNeverReadValues(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	r.declare("a")
	for name, k := range map[string]Caller{"host": hostCaller, "input": inputCaller} {
		if _, err := r.s.Read(context.Background(), k, "git.token"); reason(t, err) != "denied/user_only" {
			t.Fatalf("%s read = %v", name, err)
		}
		if _, err := r.s.Reveal(context.Background(), k, "git.token"); reason(t, err) != "denied/user_only" {
			t.Fatalf("%s reveal = %v", name, err)
		}
	}
	if len(r.log()) != 0 || len(r.audits) != 0 {
		t.Fatalf("denied callers left records: %+v %v", r.log(), r.audits)
	}
}

// Every documented event outcome and error reason, with the event's name and
// plugin_id fields.
func TestReadOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, r *secretRig) readResult
		err  string
		ev   event
	}{
		{"allowed", func(t *testing.T, r *secretRig) readResult {
			r.declare("a")
			_ = r.s.Grant(context.Background(), cliCaller, "a", "git.token")
			return r.read("a", "git.token")
		}, "", event{"git.token", "a", "allowed"}},
		{"not_declared", func(t *testing.T, r *secretRig) readResult { r.declare("a"); return r.read("a", "x.y") }, "not_found/not_declared", event{"x.y", "a", "not_declared"}},
		{"malformed name omitted", func(t *testing.T, r *secretRig) readResult { r.declare("a"); return r.read("a", "Bad Name\n") }, "not_found/not_declared", event{"", "a", "not_declared"}},
		{"unset", func(t *testing.T, r *secretRig) readResult {
			r.declare("a")
			delete(r.crypto.data, "git.token")
			_ = r.s.Grant(context.Background(), cliCaller, "a", "git.token")
			return r.read("a", "git.token")
		}, "not_found/unset", event{"git.token", "a", "unset"}},
		{"unavailable", func(t *testing.T, r *secretRig) readResult {
			r.declare("a")
			_ = r.s.Grant(context.Background(), cliCaller, "a", "git.token")
			r.crypto.err = errors.New("disk on fire " + sentinel)
			return r.read("a", "git.token")
		}, "unavailable/storage", event{"git.token", "a", "unavailable"}},
		{"stored invalid utf8", func(t *testing.T, r *secretRig) readResult {
			r.declare("a")
			r.crypto.data["git.token"] = []byte{0xff, 0xfe}
			_ = r.s.Grant(context.Background(), cliCaller, "a", "git.token")
			return r.read("a", "git.token")
		}, "unavailable/storage", event{"git.token", "a", "unavailable"}},
		{"cli unset", func(t *testing.T, r *secretRig) readResult {
			delete(r.crypto.data, "git.token")
			v, err := r.s.Reveal(context.Background(), cliCaller, "git.token")
			return readResult{v, err}
		}, "not_found/unset", event{"git.token", "", "unset"}},
		{"cli malformed name", func(t *testing.T, r *secretRig) readResult {
			v, err := r.s.Reveal(context.Background(), cliCaller, "Bad Name")
			return readResult{v, err}
		}, "invalid_params/invalid_secret_name", event{"", "", "not_declared"}},
		{"cli unavailable", func(t *testing.T, r *secretRig) readResult {
			r.crypto.err = errors.New("x")
			v, err := r.s.Reveal(context.Background(), cliCaller, "git.token")
			return readResult{v, err}
		}, "unavailable/storage", event{"git.token", "", "unavailable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSecretRig(t, rigOpts{})
			before := len(r.log())
			res := tc.run(t, r)
			if got := reason(t, res.err); got != tc.err {
				t.Fatalf("error = %q, want %q", got, tc.err)
			}
			if tc.err != "" && res.value != "" {
				t.Fatalf("value %q beside an error", res.value)
			}
			log := r.log()
			if len(log) <= before || log[len(log)-1] != tc.ev {
				t.Fatalf("events = %+v, want last %+v", log, tc.ev)
			}
		})
	}
}

func TestSetValidation(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	for _, tc := range []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{"ok", "git.token", "v", ""},
		{"empty value is valid", "empty.one", "", ""},
		{"exactly 64 KiB", "big.one", strings.Repeat("a", secrets.MaxValue), ""},
		{"over 64 KiB", "big.two", strings.Repeat("a", secrets.MaxValue+1), "invalid_params/secret_limit"},
		{"not utf8", "bin.one", "\xff\xfe", "invalid_params/not_utf8"},
		{"bad name", "Bad Name", "v", "invalid_params/invalid_secret_name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reason(t, r.s.Set(cliCaller, tc.key, tc.value)); got != tc.want {
				t.Fatalf("Set = %q, want %q", got, tc.want)
			}
			if tc.want != "" {
				if _, stored := r.crypto.data[tc.key]; stored {
					t.Fatal("a rejected value was stored")
				}
			}
		})
	}
	names, err := r.s.Names(cliCaller)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range names.Secrets {
		if i.Name == "empty.one" && (i.Set == nil || !*i.Set) {
			t.Fatalf("empty value lists %+v", i)
		}
	}
	r.crypto.err = errors.New("x")
	if got := reason(t, r.s.Set(cliCaller, "git.token", "v")); got != "unavailable/storage" {
		t.Fatalf("Set on failing storage = %q", got)
	}
}

func TestSecretDeclarationProblems(t *testing.T) {
	var reqs []string
	for i := 0; i < 18; i++ {
		reqs = append(reqs, fmt.Sprintf(`{"name":"ok.n%d","purpose":"p"}`, i))
	}
	reqs[1] = `{"name":"Bad Name","purpose":"p"}`
	reqs[2] = `{"name":"ok.n0","purpose":"p"}`
	reqs[3] = `{"name":"ok.n3","purpose":"` + sentinel + "\\u0007" + `"}`
	reqs[4] = `{"name":"ok.n4","purpose":""}`
	s, err := secrets.New(secrets.Options{State: state.New(state.Options{}), Post: func(f func()) bool { f(); return true }, Clock: clock(), Crypto: &memoryCrypto{data: map[string][]byte{}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cand, rep, err := s.Rebuild(context.Background(), catalogWith("p.q", "["+strings.Join(reqs, ",")+"]"))
	if err != nil || cand == nil {
		t.Fatalf("Rebuild = %v %+v", err, rep.Problems)
	}
	cand.Apply()
	type want struct {
		code  string
		order int
	}
	var got []want
	for _, p := range rep.Problems {
		if p.Severity != "warning" || p.Layer != "plugin:p.q" || p.Source == nil || p.Source.Plugin != "p.q" || p.Source.Layer != "plugin:p.q" {
			t.Fatalf("problem = %+v", p)
		}
		if strings.Contains(p.Message, sentinel) || strings.Contains(p.Message, "Push") {
			t.Fatalf("message carries a purpose: %q", p.Message)
		}
		got = append(got, want{p.Code, p.Source.Order})
	}
	exp := []want{{"invalid_secret_name", 1}, {"duplicate_secret", 2}, {"invalid_purpose", 3}, {"invalid_purpose", 4}, {"too_many_secrets", 16}, {"too_many_secrets", 17}}
	if fmt.Sprint(got) != fmt.Sprint(exp) {
		t.Fatalf("problems = %v, want %v", got, exp)
	}
	// The other declarations stand: 18 minus 6 dropped.
	names, _ := s.Names(cliCaller)
	var declared int
	for _, i := range names.Secrets {
		if len(i.Declared) > 0 {
			declared++
		}
	}
	if declared != 12 {
		t.Fatalf("%d declarations stand, want 12", declared)
	}
}

// secrets.names for each caller, decoded from the JSON a handler would write.
func TestNamesByCaller(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	apply(t, r.s, catalogWith("a", `[{"name":"git.token","purpose":"Push","required":true},{"name":"zed.key","purpose":"Z"}]`))
	r.crypto.data["stored.only"] = []byte("v")
	r.crypto.data["zed.key"] = []byte("z")
	if err := r.s.Grant(context.Background(), cliCaller, "a", "git.token"); err != nil {
		t.Fatal(err)
	}
	decode := func(caller Caller) map[string]any {
		t.Helper()
		res, err := r.s.Names(caller)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	rows := func(m map[string]any) []map[string]any {
		var out []map[string]any
		for _, x := range m["secrets"].([]any) {
			out = append(out, x.(map[string]any))
		}
		return out
	}
	cli := decode(cliCaller)
	if cli["storage"] != "ok" {
		t.Fatalf("storage = %v", cli["storage"])
	}
	want := `[{"declared":["a"],"granted":["a"],"name":"git.token","required":true,"set":true},{"declared":[],"granted":[],"name":"stored.only","required":false,"set":true},{"declared":["a"],"granted":[],"name":"zed.key","required":false,"set":true}]`
	gotRaw, _ := json.Marshal(rows(cli))
	if string(gotRaw) != want {
		t.Fatalf("cli names =\n%s\nwant\n%s", gotRaw, want)
	}
	granted := rows(decode(pluginCaller("a")))
	if len(granted) != 2 || granted[0]["name"] != "git.token" || granted[0]["set"] != true || granted[1]["name"] != "zed.key" {
		t.Fatalf("plugin names = %v", granted)
	}
	if _, has := granted[1]["set"]; has {
		t.Fatal("set shown for an ungranted name to its plugin")
	}
	stranger := decode(pluginCaller("b"))
	if len(rows(stranger)) != 0 || fmt.Sprint(stranger["secrets"]) != "[]" {
		t.Fatalf("stranger sees %v", stranger)
	}
	r.crypto.err = errors.New("keyring locked")
	down, err := r.s.Names(cliCaller)
	if err != nil || down.Storage != "unavailable" {
		t.Fatalf("failing storage = %+v, %v", down, err)
	}
	for _, i := range down.Secrets {
		if i.Name == "git.token" && len(i.Declared) != 1 {
			t.Fatalf("declared names lost on storage failure: %+v", i)
		}
	}
}

// A rejected State write is not a grant, including when a dialog was accepted.
func TestGrantWriteFailure(t *testing.T) {
	for _, via := range []string{"cli", "dialog"} {
		t.Run(via, func(t *testing.T) {
			// The empty slice fits, but no grant does. Use the real State cap
			// enforcement rather than replacing Set with a fake error.
			r := newSecretRig(t, rigOpts{maxBytes: len(`{"grants":[]}`)})
			r.declare("a")
			before := r.tree.Snapshot()
			got := r.readAsync(context.Background(), "a", "git.token")
			pc := r.next()
			if via == "cli" {
				if err := r.s.Grant(context.Background(), cliCaller, "a", "git.token"); reason(t, err) != "unavailable/storage" {
					t.Fatalf("grant = %v", err)
				}
				// Failure must not resolve the pending read as allowed.
				pc.reply <- secrets.Dismissed
			} else {
				pc.reply <- secrets.Accepted
			}
			res := <-got
			want := "denied/user_denied"
			if via == "dialog" {
				want = "unavailable/storage"
				if ev := r.lastEvent(); ev != (event{"git.token", "a", "unavailable"}) {
					t.Fatalf("failed accept event = %+v", ev)
				}
			}
			if res.value != "" || reason(t, res.err) != want {
				t.Fatalf("read after failed grant = %+v", res)
			}
			if r.tree.Snapshot() != before {
				t.Fatal("failed grant changed the slice or its revision")
			}
		})
	}
}

func TestRevokeWriteFailure(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	r.declare("a")
	// A revoke normally shrinks the document, so simulate a lost capability
	// to exercise Set's failure rather than its size cap.
	r.tree.handle.Unregister()
	before := r.tree.Snapshot()
	if err := r.s.Revoke(context.Background(), cliCaller, "a", "git.token"); reason(t, err) != "unavailable/storage" {
		t.Fatalf("revoke = %v", err)
	}
	if r.tree.Snapshot() != before {
		t.Fatal("failed revoke changed State")
	}
}

// Reload must invalidate even a dialog already accepted but waiting on its
// posted grant write. No old consent or decline memory may survive it.
func TestRebuildBeforeAcceptedWrite(t *testing.T) {
	for _, tc := range []struct {
		name, declarations string
	}{
		{"purpose changed", `[{"name":"git.token","purpose":"Changed"}]`},
		{"removed", `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posted := make(chan func(), 1)
			r := newSecretRig(t, rigOpts{post: func(f func()) bool { posted <- f; return true }})
			r.declare("a")
			before := r.tree.Snapshot()
			got := r.readAsync(context.Background(), "a", "git.token")
			r.next().reply <- secrets.Accepted
			write := <-posted // accepted, but no State write has executed
			apply(t, r.s, catalogWith("a", tc.declarations))
			// Reconcile closes the accepting dialog without waiting for the post.
			if res := <-got; res.value != "" || reason(t, res.err) != "denied/user_denied" {
				t.Fatalf("superseded accept = %+v", res)
			}
			if ev := r.lastEvent(); ev != (event{"git.token", "a", "user_denied"}) {
				t.Fatalf("event = %+v", ev)
			}
			write()
			if r.tree.Snapshot() != before {
				t.Fatal("stale accepted grant changed State")
			}
			if tc.name == "removed" {
				if res := r.read("a", "git.token"); reason(t, res.err) != "not_found/not_declared" {
					t.Fatalf("removed declaration = %+v", res)
				}
				// Reintroducing it must prompt, not remember a denial.
				apply(t, r.s, catalogWith("a", `[{"name":"git.token","purpose":"Changed"}]`))
			}
			again := r.readAsync(context.Background(), "a", "git.token")
			pc := r.next()
			if pc.p.Purpose != "Changed" {
				t.Fatalf("new prompt = %+v", pc.p)
			}
			pc.reply <- secrets.Dismissed
			<-again
		})
	}
}

func TestNamesReadsGrantsOnce(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	r.declare("a", "b", "c")
	if err := r.s.Grant(context.Background(), cliCaller, "b", "git.token"); err != nil {
		t.Fatal(err)
	}
	before := r.tree.reads.Load()
	got, err := r.s.Names(cliCaller)
	if err != nil {
		t.Fatal(err)
	}
	if reads := r.tree.reads.Load() - before; reads != 1 {
		t.Fatalf("Names read the grants slice %d times, want once", reads)
	}
	if len(got.Secrets) != 1 || len(got.Secrets[0].Granted) != 1 || got.Secrets[0].Granted[0] != "b" {
		t.Fatalf("Names = %+v", got)
	}
}

// Grant and revoke return only after the posted write has executed.
func TestGrantRevokeReturnAfterPostedWrite(t *testing.T) {
	posted := make(chan func(), 4)
	var hold atomic.Bool
	r := newSecretRig(t, rigOpts{post: func(f func()) bool {
		if hold.Load() {
			posted <- f
		} else {
			f()
		}
		return true
	}})
	r.declare("a")
	for name, call := range map[string]func() error{
		"grant":  func() error { return r.s.Grant(context.Background(), cliCaller, "a", "git.token") },
		"revoke": func() error { return r.s.Revoke(context.Background(), cliCaller, "a", "git.token") },
	} {
		t.Run(name, func(t *testing.T) {
			hold.Store(true)
			type outcome struct {
				err error
				ran bool // the posted write had executed when the call returned
			}
			var ran atomic.Bool
			done := make(chan outcome, 1)
			go func() {
				err := call()
				done <- outcome{err, ran.Load()}
			}()
			f := <-posted
			ran.Store(true)
			f()
			got := <-done
			if got.err != nil {
				t.Fatal(got.err)
			}
			if !got.ran {
				t.Fatalf("%s returned before the posted write ran", name)
			}
			hold.Store(false)
		})
	}
	// Revoking a grant that is not there succeeds; granting an undeclared name does not.
	if err := r.s.Revoke(context.Background(), cliCaller, "a", "git.token"); err != nil {
		t.Fatal(err)
	}
	if got := reason(t, r.s.Grant(context.Background(), cliCaller, "a", "nope.x")); got != "not_found/not_declared" {
		t.Fatalf("grant undeclared = %q", got)
	}
}

// The sentinel value reaches exactly the authorized replies: a cli reveal and a
// granted plugin read. Events, prompts, audit names, problems, names listings
// and every error string must not carry it.
func TestValueNeverLeavesAuthorizedReplies(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	r.declare("a", "b")
	var outbound []string
	add := func(label string, v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		outbound = append(outbound, label+":"+string(raw))
	}
	addErr := func(label string, err error) {
		if err != nil {
			outbound = append(outbound, label+":"+err.Error())
			var f *c.Error
			if errors.As(err, &f) {
				add(label+".failure", f)
			}
		}
	}
	// Refused and failing paths.
	_, e := r.s.Read(context.Background(), hostCaller, "git.token")
	addErr("host", e)
	_, e = r.s.Read(context.Background(), pluginCaller("zzz"), "git.token")
	addErr("undeclared plugin", e)
	_, e = r.s.Read(context.Background(), pluginCaller("a"), "x.y")
	addErr("undeclared name", e)
	// A declined dialog, with the prompt itself recorded.
	got := r.readAsync(context.Background(), "a", "git.token")
	pc := r.next()
	add("prompt", pc.p)
	pc.reply <- secrets.Declined
	addErr("declined", (<-got).err)
	// Storage failing with the value in the underlying error.
	_ = r.s.Grant(context.Background(), cliCaller, "b", "git.token")
	r.crypto.err = errors.New("backend said " + sentinel)
	_, e = r.s.Read(context.Background(), pluginCaller("b"), "git.token")
	addErr("storage", e)
	_, e = r.s.Reveal(context.Background(), cliCaller, "git.token")
	addErr("reveal storage", e)
	names, _ := r.s.Names(cliCaller)
	add("names", names)
	r.crypto.err = nil
	for _, k := range []Caller{cliCaller, pluginCaller("a"), pluginCaller("b")} {
		n, _ := r.s.Names(k)
		add("names", n)
	}
	_, rep, _ := r.s.Rebuild(context.Background(), catalogs("a", "b"))
	add("report", rep)
	add("events", r.log())
	add("audits", r.audits)
	add("state", r.tree.Snapshot().Slices)
	if g, ok := r.tree.Snapshot().Slices[state.GrantsNamespace]; !ok || !strings.Contains(string(g.Value), `"b"`) {
		t.Fatalf("grants slice missing the grant the test wrote: %+v", g)
	}
	for _, o := range outbound {
		if strings.Contains(o, sentinel) {
			t.Fatalf("value leaked into %s", o)
		}
	}
	// The authorized replies do carry it.
	if v, err := r.s.Reveal(context.Background(), cliCaller, "git.token"); err != nil || v != sentinel {
		t.Fatalf("reveal = %q, %v", v, err)
	}
	if res := r.read("b", "git.token"); res.value != sentinel {
		t.Fatalf("granted read = %+v", res)
	}
	add("events", r.log())
	add("audits", r.audits)
	if strings.Contains(outbound[len(outbound)-1], sentinel) || strings.Contains(outbound[len(outbound)-2], sentinel) {
		t.Fatal("value leaked into an event or audit record")
	}
}

// A read during shutdown is dropped like a closed connection: the error says
// so and no secrets.read event is published for it.
func TestShutdownDropsReadWithoutEvent(t *testing.T) {
	r := newSecretRig(t, rigOpts{})
	r.declare("a")
	r.s.Close()
	_, err := r.s.Read(context.Background(), pluginCaller("a"), "git.token")
	if got := reason(t, err); got != "cancelled/shutdown" {
		t.Fatalf("read after close = %q", got)
	}
	if len(r.log()) != 0 {
		t.Fatalf("a shutdown read published %+v", r.log())
	}
}
