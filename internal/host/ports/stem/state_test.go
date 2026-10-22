package stem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// syncLoop runs every posted closure synchronously, like the Bubble Tea loop
// runs Update to completion before the next message.
type syncLoop struct{ ran int }

func (l *syncLoop) Post(f func()) { l.ran++; f() }

// blockedLoop records closures without running them, modelling a busy loop.
type blockedLoop struct{ pending []func() }

func (l *blockedLoop) Post(f func()) { l.pending = append(l.pending, f) }

// handleSource fakes the Pod-held handle seam: it serves exactly the handles
// the test registered, keyed by plugin id and namespace.
type handleSource struct {
	mu    sync.Mutex
	live  map[string]contracts.SliceHandle
	drops bool
}

func (s *handleSource) Handle(pluginID, namespace string) (contracts.SliceHandle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drops {
		return nil, false
	}
	h, ok := s.live[pluginID+"\x00"+namespace]
	return h, ok
}

type cleaner struct {
	mu      sync.Mutex
	deleted []string
	err     error
}

func (c *cleaner) DeleteSlice(_ context.Context, namespace string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted = append(c.deleted, namespace)
	return c.err
}

func (c *cleaner) deletedNS() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.deleted...)
}

type noteRecorder struct {
	mu  sync.Mutex
	got []protocol.Message
}

func (n *noteRecorder) Notify(m protocol.Message) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.got = append(n.got, m)
	return nil
}

func (n *noteRecorder) messages() []protocol.Message {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]protocol.Message(nil), n.got...)
}

var (
	callerCLI   = contracts.Caller{Kind: contracts.CallerCLI}
	callerStem  = contracts.Caller{Kind: contracts.CallerStem}
	callerAtlas = contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "atlas"}
	callerOther = contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "other"}
	callerTiny  = contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "tiny"}
	ctxBG       = context.Background()

	specAtlas   = state.Spec{Namespace: "atlas.state", Owner: "atlas", Scope: state.ScopeInstance}
	specAtlasWS = state.Spec{Namespace: "atlas.workspace", Owner: "atlas", Scope: state.ScopeWorkspace}
	specTiny    = state.Spec{Namespace: "tiny.state", Owner: "tiny", Scope: state.ScopeInstance, MaxBytes: 24}
	specPersist = state.Spec{Namespace: "keep.state", Owner: "keep", Scope: state.ScopeUser, Persistent: true, Retention: state.Retention{Policy: state.KeepLatest}}
	specFresh   = state.Spec{Namespace: "atlas.state", Owner: "atlas", Scope: state.ScopeInstance, Initial: json.RawMessage(`{"fresh":true}`)}
)

// newPort builds the wrapper over one real State: specs are registered on
// that State and their handles served to their owning plugin through the
// handle seam, exactly as the owning Pod would. The workspace owner is
// atlas, so a ScopeWorkspace spec with Owner atlas is valid.
func newPort(t *testing.T, opts StatePortOptions, specs ...state.Spec) (*State, *state.State, *handleSource) {
	t.Helper()
	if opts.State == nil {
		opts.State = state.New(state.Options{WorkspaceOwner: "atlas"})
	}
	st := opts.State
	hs := &handleSource{live: map[string]contracts.SliceHandle{}}
	for _, spec := range specs {
		h, err := st.Register(spec)
		if err != nil {
			t.Fatal(err)
		}
		hs.live[spec.Owner+"\x00"+spec.Namespace] = h
	}
	if opts.Handles == nil {
		opts.Handles = hs
	}
	if opts.Loop == nil {
		opts.Loop = &syncLoop{}
	}
	return NewStatePort(opts), st, hs
}

func wantPortCode(t *testing.T, err error, code protocol.Code) {
	t.Helper()
	if got := contracts.CodeOf(err); got != string(code) {
		t.Fatalf("code = %s (%v), want %s", got, err, code)
	}
}

func TestStatePortNewPanics(t *testing.T) {
	cases := []struct {
		name string
		opts StatePortOptions
	}{
		{"nil state", StatePortOptions{Loop: &syncLoop{}}},
		{"nil loop", StatePortOptions{State: state.New(state.Options{})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewStatePort must panic")
				}
			}()
			NewStatePort(tc.opts)
		})
	}
}

func TestStatePortGet(t *testing.T) {
	w, _, _ := newPort(t, StatePortOptions{}, state.Spec{
		Namespace: "atlas.state", Owner: "atlas", Scope: state.ScopeInstance,
		Initial: json.RawMessage(`{"list":["x","y"],"a":{"b":2},"k~s":1,"s/l":2}`),
	})
	cases := []struct {
		path     string
		want     string
		notFound bool
		badPath  bool
	}{
		{path: "", want: `{"a":{"b":2},"k~s":1,"list":["x","y"],"s/l":2}`},
		{path: "/a", want: `{"b":2}`},
		{path: "/a/b", want: `2`},
		{path: "/list/1", want: `"y"`},
		{path: "/list/0", want: `"x"`},
		{path: "/k~0s", want: `1`},
		{path: "/s~1l", want: `2`},
		{path: "/missing", notFound: true},
		{path: "/list/5", notFound: true},
		{path: "/list/-", notFound: true},
		{path: "/list/01", notFound: true},
		{path: "/list/x", notFound: true},
		{path: "/a/b/c", notFound: true},
		{path: "noslash", badPath: true},
	}
	for _, tc := range cases {
		t.Run("path "+tc.path, func(t *testing.T) {
			res, err := w.Get(ctxBG, callerAtlas, "atlas.state", tc.path)
			switch {
			case tc.badPath:
				wantPortCode(t, err, protocol.CodeInvalidParams)
			case tc.notFound:
				wantPortCode(t, err, protocol.CodeNotFound)
			default:
				if err != nil {
					t.Fatal(err)
				}
				if string(res.Value) != tc.want {
					t.Fatalf("value = %s, want %s", res.Value, tc.want)
				}
				if res.Slice != "atlas.state" || res.Owner != "atlas" || res.Revision != 1 {
					t.Fatalf("result = %+v", res)
				}
			}
		})
	}
	t.Run("unknown slice", func(t *testing.T) {
		_, err := w.Get(ctxBG, callerAtlas, "nope.state", "")
		wantPortCode(t, err, protocol.CodeNotFound)
	})
	t.Run("null member resolves to null", func(t *testing.T) {
		w, _, hs := newPort(t, StatePortOptions{}, specAtlas)
		hs.mu.Lock()
		h := hs.live["atlas\x00atlas.state"].(*state.Handle)
		hs.mu.Unlock()
		if _, err := h.Set(json.RawMessage(`{"gone":null}`)); err != nil {
			t.Fatal(err)
		}
		res, err := w.Get(ctxBG, callerAtlas, "atlas.state", "/gone")
		if err != nil {
			t.Fatal(err)
		}
		if string(res.Value) != "null" {
			t.Fatalf("value = %s", res.Value)
		}
	})
}

func TestStatePortSetPatch(t *testing.T) {
	t.Run("set replaces the plugin's own slice on the loop", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{}, specAtlas)
		rev, err := w.Set(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{"done":true}`))
		if err != nil {
			t.Fatal(err)
		}
		if rev != 2 {
			t.Fatalf("revision = %d", rev)
		}
	})
	t.Run("patch merges with if_revision", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{}, specAtlas)
		if _, err := w.Set(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{"a":1}`)); err != nil {
			t.Fatal(err)
		}
		rev, err := w.Patch(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{"b":2}`), 2)
		if err != nil {
			t.Fatal(err)
		}
		if rev != 3 {
			t.Fatalf("revision = %d", rev)
		}
		res, err := w.Get(ctxBG, callerAtlas, "atlas.state", "")
		if err != nil {
			t.Fatal(err)
		}
		if string(res.Value) != `{"a":1,"b":2}` {
			t.Fatalf("value = %s", res.Value)
		}
	})
	t.Run("cli and stem callers are denied", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{}, specAtlas)
		for _, c := range []contracts.Caller{callerCLI, callerStem} {
			if _, err := w.Set(ctxBG, c, "atlas.state", json.RawMessage(`{}`)); contracts.CodeOf(err) != string(protocol.CodeDenied) {
				t.Fatalf("set code = %v", err)
			}
			if _, err := w.Patch(ctxBG, c, "atlas.state", json.RawMessage(`{}`), 0); contracts.CodeOf(err) != string(protocol.CodeDenied) {
				t.Fatalf("patch code = %v", err)
			}
		}
	})
	t.Run("another owner's namespace is denied", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{}, specAtlas)
		// atlas cannot reach outside its own namespaces.
		for _, ns := range []string{"other.state", "atlas.secret"} {
			if _, err := w.Set(ctxBG, callerAtlas, ns, json.RawMessage(`{}`)); contracts.CodeOf(err) != string(protocol.CodeDenied) {
				t.Fatalf("ns %s by atlas: %v", ns, err)
			}
		}
		// other's own namespace passes authorization but has no Pod handle.
		_, err := w.Set(ctxBG, callerOther, "other.state", json.RawMessage(`{}`))
		wantPortCode(t, err, protocol.CodeInternal)
	})
	t.Run("workspace namespace is writable", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{}, specAtlasWS)
		if _, err := w.Set(ctxBG, callerAtlas, "atlas.workspace", json.RawMessage(`{"pane":3}`)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing handle source is internal", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{Handles: &handleSource{}}, specAtlas)
		_, err := w.Set(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{}`))
		wantPortCode(t, err, protocol.CodeInternal)
	})
	t.Run("unmapped handle is internal", func(t *testing.T) {
		w, _, hs := newPort(t, StatePortOptions{}, specAtlas)
		hs.mu.Lock()
		hs.drops = true
		hs.mu.Unlock()
		_, err := w.Set(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{}`))
		wantPortCode(t, err, protocol.CodeInternal)
	})
}

func TestStatePortWriteErrors(t *testing.T) {
	t.Run("conflict carries the current revision", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{}, specAtlas)
		_, err := w.Patch(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{}`), 99)
		wantPortCode(t, err, protocol.CodeConflict)
		var de contracts.DataError
		if !errors.As(err, &de) || de.Data()["revision"] != uint64(1) {
			t.Fatalf("data = %+v", de)
		}
	})
	t.Run("size cap carries the cap", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{}, specTiny)
		big := json.RawMessage(`{"pad":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
		_, err := w.Set(ctxBG, callerTiny, "tiny.state", big)
		wantPortCode(t, err, protocol.CodeInvalidParams)
		var de contracts.DataError
		if !errors.As(err, &de) || de.Data()["limit"] != 24 {
			t.Fatalf("data = %+v", de)
		}
	})
	t.Run("non-object value is invalid_params", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{}, specAtlas)
		_, err := w.Set(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`[1,2]`))
		wantPortCode(t, err, protocol.CodeInvalidParams)
	})
	t.Run("unregistered handle is not_found", func(t *testing.T) {
		w, st, hs := newPort(t, StatePortOptions{}, specAtlas)
		hs.mu.Lock()
		h := hs.live["atlas\x00atlas.state"].(*state.Handle)
		hs.mu.Unlock()
		h.Unregister()
		st.Flush()
		_, err := w.Set(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{}`))
		wantPortCode(t, err, protocol.CodeNotFound)
	})
	t.Run("cancelled request when the loop is busy", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{Loop: &blockedLoop{}}, specAtlas)
		wctx, cancel := context.WithCancel(ctxBG)
		cancel()
		_, err := w.Set(wctx, callerAtlas, "atlas.state", json.RawMessage(`{}`))
		wantPortCode(t, err, protocol.CodeCancelled)
	})
}

func TestStatePortSnapshot(t *testing.T) {
	w, _, _ := newPort(t, StatePortOptions{}, specAtlas, specAtlasWS, specPersist)
	if _, err := w.Set(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	t.Run("descriptors only", func(t *testing.T) {
		res, err := w.Snapshot(ctxBG, callerCLI, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.Seq != 4 {
			t.Fatalf("seq = %d", res.Seq)
		}
		var names []string
		for _, s := range res.Slices {
			names = append(names, s.Slice)
			if s.Value != nil {
				t.Fatalf("slice %s carried a value", s.Slice)
			}
		}
		if fmt.Sprint(names) != "[atlas.state atlas.workspace keep.state]" {
			t.Fatalf("slices = %v", names)
		}
		for _, s := range res.Slices {
			if s.Slice == "keep.state" && (!s.Persistent || s.Retention == nil || s.Retention.Policy != string(state.KeepLatest)) {
				t.Fatalf("retention = %+v", s.Retention)
			}
		}
	})
	t.Run("values included on request", func(t *testing.T) {
		res, err := w.Snapshot(ctxBG, callerCLI, true)
		if err != nil {
			t.Fatal(err)
		}
		byName := map[string]contracts.StateSlice{}
		for _, s := range res.Slices {
			byName[s.Slice] = s
		}
		if string(byName["atlas.state"].Value) != `{"a":1}` {
			t.Fatalf("value = %s", byName["atlas.state"].Value)
		}
		if string(byName["atlas.workspace"].Value) != `{}` {
			t.Fatalf("value = %s", byName["atlas.workspace"].Value)
		}
	})
}

func TestStatePortWatchAndFanOut(t *testing.T) {
	w, st, hs := newPort(t, StatePortOptions{}, specAtlas, specAtlasWS)

	all := &noteRecorder{}
	own := &noteRecorder{}
	dead := &noteRecorder{}
	wctx, wcancel := context.WithCancel(ctxBG)

	base, err := w.Watch(ctxBG, callerCLI, nil, all)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Watch(ctxBG, callerAtlas, []string{"atlas.state"}, own); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Watch(wctx, callerCLI, []string{"atlas.state"}, dead); err != nil {
		t.Fatal(err)
	}
	if len(base.Slices) != 2 || base.Slices[0].Value == nil {
		t.Fatalf("baseline = %+v", base)
	}

	// The dead watcher's context ends before anything changes; the flush
	// must drop it even if its unregister callback has not run yet.
	wcancel()

	// One tick: two writes, one flush — namespace-ordered, one change each.
	if _, err := w.Set(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Set(ctxBG, callerAtlas, "atlas.workspace", json.RawMessage(`{"w":true}`)); err != nil {
		t.Fatal(err)
	}
	st.Flush()

	got := all.messages()
	if len(got) != 2 {
		t.Fatalf("all watcher got %d notifications: %+v", len(got), got)
	}
	if got[0].Method != protocol.MethodStateChanged {
		t.Fatalf("method = %s", got[0].Method)
	}
	var first, second changedParams
	if err := json.Unmarshal(got[0].Params, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got[1].Params, &second); err != nil {
		t.Fatal(err)
	}
	if first.Slice != "atlas.state" || first.Owner != "atlas" || first.Revision != 2 || string(first.Value) != `{"a":1}` || first.Removed {
		t.Fatalf("first change = %+v", first)
	}
	if second.Slice != "atlas.workspace" || string(second.Value) != `{"w":true}` {
		t.Fatalf("second change = %+v", second)
	}

	// The filtered watcher saw only its slice; the cancelled one saw nothing.
	ownGot := own.messages()
	if len(ownGot) != 1 {
		t.Fatalf("own watcher got %d notifications", len(ownGot))
	}
	var only changedParams
	_ = json.Unmarshal(ownGot[0].Params, &only)
	if only.Slice != "atlas.state" {
		t.Fatalf("own change = %+v", only)
	}
	if got := dead.messages(); len(got) != 0 {
		t.Fatalf("dead watcher got %d notifications", len(got))
	}

	// Unregistration fans out a removed change.
	hs.mu.Lock()
	h := hs.live["atlas\x00atlas.state"].(*state.Handle)
	hs.mu.Unlock()
	h.Unregister()
	st.Flush()
	got = all.messages()
	if len(got) != 3 {
		t.Fatalf("after unregister: %d notifications", len(got))
	}
	var removed changedParams
	_ = json.Unmarshal(got[2].Params, &removed)
	if removed.Slice != "atlas.state" || !removed.Removed || removed.Value != nil {
		t.Fatalf("removed change = %+v", removed)
	}
}

func TestStatePortClear(t *testing.T) {
	t.Run("user clear resets to initial and deletes persisted copies", func(t *testing.T) {
		cl := &cleaner{}
		w, _, _ := newPort(t, StatePortOptions{Cleaner: cl}, specFresh)
		if _, err := w.Set(ctxBG, callerAtlas, "atlas.state", json.RawMessage(`{"dirty":1}`)); err != nil {
			t.Fatal(err)
		}
		if err := w.Clear(ctxBG, callerCLI, "atlas.state"); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(cl.deletedNS()) != "[atlas.state]" {
			t.Fatalf("cleaner deleted = %v", cl.deletedNS())
		}
		res, err := w.Get(ctxBG, callerCLI, "atlas.state", "")
		if err != nil {
			t.Fatal(err)
		}
		if string(res.Value) != `{"fresh":true}` || res.Revision != 3 {
			t.Fatalf("after clear = %+v", res)
		}
	})
	t.Run("stem callers clear as the user", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{}, specAtlas)
		if err := w.Clear(ctxBG, callerStem, "atlas.state"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("plugin callers are denied", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{}, specAtlas)
		if err := w.Clear(ctxBG, callerAtlas, "atlas.state"); contracts.CodeOf(err) != string(protocol.CodeDenied) {
			t.Fatalf("code = %v", err)
		}
	})
	t.Run("cleaner failure is internal", func(t *testing.T) {
		cl := &cleaner{err: errors.New("disk on fire")}
		w, _, _ := newPort(t, StatePortOptions{Cleaner: cl}, specAtlas)
		err := w.Clear(ctxBG, callerCLI, "atlas.state")
		wantPortCode(t, err, protocol.CodeInternal)
	})
	t.Run("unknown namespace is not_found", func(t *testing.T) {
		w, _, _ := newPort(t, StatePortOptions{}, specAtlas)
		err := w.Clear(ctxBG, callerCLI, "nope.state")
		wantPortCode(t, err, protocol.CodeNotFound)
	})
}
