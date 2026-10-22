package stem_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/ports/stem"
	"github.com/hyperlab-sh/husk/internal/stem/surfaces"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// portClock, portLiveness, portPublications, portLogger and portDeclarations
// are the minimal seams the port tests inject: the engine takes no defaults.
// portClock is a fixed clock. When probe is set (the queue loop's inLoop
// flag), each Now() records its answer so a test can prove the engine body
// ran on the loop goroutine.
type portClock struct {
	now    time.Time
	probe  func() bool
	onLoop atomic.Bool
}

func (c *portClock) Now() time.Time {
	if c.probe != nil {
		c.onLoop.Store(c.probe())
	}
	return c.now
}

type portLiveness struct{}

func (portLiveness) Live(string) bool { return true }

type portPublications struct{ owners map[string]string }

func (p portPublications) Owner(slot string) (string, bool) {
	owner, ok := p.owners[slot]
	return owner, ok
}

type portLogger struct{ t *testing.T }

func (l portLogger) Printf(format string, v ...any) { l.t.Logf(format, v...) }

// portDeclarations is a no-op Declarations seam: the port tests never
// exercise slot.contribute retraction, but the engine requires the seam.
type portDeclarations struct{}

func (portDeclarations) Contribute(string, string, surfaces.Contribution) error { return nil }

func (portDeclarations) Release(string, protocol.SurfaceID) error { return nil }

// newPort wires a real engine behind the port, crossing the shared queue
// loop (calls_test.go) exactly as the host loop would. It returns the clock
// so tests can observe loop placement through the engine's Now() calls.
func newPort(t *testing.T) (*stem.SurfacePort, *surfaces.Engine, *state.State, *loop, *portClock) {
	t.Helper()
	st := state.New(state.Options{})
	occ, err := st.Register(state.Spec{
		Namespace: "alpha.files.occupants", Owner: "alpha.files", Scope: state.ScopeInstance,
	})
	if err != nil {
		t.Fatalf("register occupants: %v", err)
	}
	if _, err := occ.Set(json.RawMessage(`{"main-stage":{"surface":"main-stage","plugin":"alpha.files"}}`)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	l := newLoop(t)
	clk := &portClock{now: time.Unix(0, 0), probe: l.inLoop.Load}
	eng, err := surfaces.New(st, surfaces.Options{
		Clock:        clk,
		Logger:       portLogger{t: t},
		Liveness:     portLiveness{},
		Publications: portPublications{owners: map[string]string{}},
		Declarations: portDeclarations{},
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	st.Flush()
	eng.Flush()
	return stem.NewSurfacePort(eng, l), eng, st, l, clk
}

func TestCallerKindsEnforced(t *testing.T) {
	port, _, _, _, _ := newPort(t)
	ctx := context.Background()
	plugin := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "alpha.files"}
	other := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "beta.notes"}
	cli := contracts.Caller{Kind: contracts.CallerCLI}
	stem := contracts.Caller{Kind: contracts.CallerStem}

	update := protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage}
	closeP := protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage}
	contribute := protocol.SlotContributeParams{SlotID: "s", ID: "alpha.files.x"}
	toast := protocol.ToastShowParams{Level: "info", Title: "t"}

	if err := port.SurfaceUpdate(ctx, plugin, update); err != nil {
		t.Fatalf("plugin update: %v", err)
	}
	if err := port.SurfaceUpdate(ctx, other, update); contracts.CodeOf(err) != "denied" {
		t.Fatalf("non-occupant update: %v", err)
	}
	for name, err := range map[string]error{
		"cli update":     port.SurfaceUpdate(ctx, cli, update),
		"cli close":      port.SurfaceClose(ctx, cli, closeP),
		"cli contribute": port.SlotContribute(ctx, cli, contribute),
		"stem update":    port.SurfaceUpdate(ctx, stem, update),
	} {
		if code := contracts.CodeOf(err); code != "denied" {
			t.Fatalf("%s: code = %q, want denied", name, code)
		}
	}
	if err := port.SurfaceClose(ctx, plugin, closeP); err != nil {
		t.Fatalf("plugin close: %v", err)
	}
	if code := contracts.CodeOf(port.SlotContribute(ctx, plugin, contribute)); code != "not_found" {
		t.Fatalf("plugin contribute code = %q, want not_found (no catalog in this fixture)", code)
	}
	if err := port.ToastShow(ctx, plugin, toast); err != nil {
		t.Fatalf("plugin toast: %v", err)
	}
	if err := port.ToastShow(ctx, cli, toast); err != nil {
		t.Fatalf("CLI toast: %v", err)
	}
}

func TestErrorsAreCoded(t *testing.T) {
	port, _, _, _, _ := newPort(t)
	ctx := context.Background()
	other := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "beta.notes"}

	err := port.SurfaceUpdate(ctx, other, protocol.SurfaceUpdateParams{
		Surface: protocol.SurfaceMainStage, PluginID: "alpha.files", // spoofed: ignored
	})
	if err == nil {
		t.Fatal("expected denial")
	}
	coded, ok := err.(contracts.CodedError)
	if !ok {
		t.Fatalf("error %v is not a CodedError", err)
	}
	if contracts.CodeOf(err) != "denied" {
		t.Fatalf("code = %q", contracts.CodeOf(err))
	}
	if coded.Error() == "" {
		t.Fatal("empty message")
	}

	if code := contracts.CodeOf(port.SurfaceUpdate(ctx, other, protocol.SurfaceUpdateParams{Surface: "bogus"})); code != "invalid_params" {
		t.Fatalf("invalid surface code = %q, want invalid_params", code)
	}
}

func TestToastAttributionFromCaller(t *testing.T) {
	port, eng, _, _, _ := newPort(t)
	cli := contracts.Caller{Kind: contracts.CallerCLI}
	if err := port.ToastShow(context.Background(), cli, protocol.ToastShowParams{
		Level: "info", Title: "from CLI", PluginID: "spoofed",
	}); err != nil {
		t.Fatalf("CLI toast: %v", err)
	}
	active, ok := eng.ActiveToast()
	if !ok || active.Title != "from CLI" || active.Plugin != "" {
		t.Fatalf("active = %+v, want host-attributed toast", active)
	}
}

// TestUnauthenticatedPluginCallerDenied pins S4: a plugin-kind caller with
// an empty PluginID has no identity to authorize, so every method denies it
// like the Calls port's callerOf.
func TestUnauthenticatedPluginCallerDenied(t *testing.T) {
	port, _, _, _, _ := newPort(t)
	anon := contracts.Caller{Kind: contracts.CallerPlugin}
	ctx := context.Background()
	calls := map[string]func() error{
		"surface.update": func() error {
			return port.SurfaceUpdate(ctx, anon, protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage})
		},
		"surface.close": func() error {
			return port.SurfaceClose(ctx, anon, protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage})
		},
		"slot.contribute": func() error {
			return port.SlotContribute(ctx, anon, protocol.SlotContributeParams{SlotID: "s", ID: "p.x"})
		},
		"toast.show": func() error {
			return port.ToastShow(ctx, anon, protocol.ToastShowParams{Level: "info", Title: "t"})
		},
	}
	for name, call := range calls {
		if code := contracts.CodeOf(call()); code != "denied" {
			t.Fatalf("%s with empty plugin id: code = %q, want denied", name, code)
		}
	}
}

// TestZeroAndUnknownCallerKindsDenied pins S5: a zero-value or unlisted
// CallerKind is not any method's home, so all four methods deny it — and
// the denial names the kind, never panics.
func TestZeroAndUnknownCallerKindsDenied(t *testing.T) {
	port, _, _, _, _ := newPort(t)
	ctx := context.Background()
	for name, c := range map[string]contracts.Caller{
		"zero kind": {},
		"kind 9":    {Kind: contracts.CallerKind(9)},
	} {
		calls := map[string]func() error{
			"surface.update": func() error {
				return port.SurfaceUpdate(ctx, c, protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage})
			},
			"surface.close": func() error {
				return port.SurfaceClose(ctx, c, protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage})
			},
			"slot.contribute": func() error {
				return port.SlotContribute(ctx, c, protocol.SlotContributeParams{SlotID: "s", ID: "p.x"})
			},
			"toast.show": func() error {
				return port.ToastShow(ctx, c, protocol.ToastShowParams{Level: "info", Title: "t"})
			},
		}
		for method, call := range calls {
			if code := contracts.CodeOf(call()); code != "denied" {
				t.Fatalf("%s on %s: code = %q, want denied", name, method, code)
			}
		}
	}
}

// TestBadContributionIdIsInvalidParams pins the id rule's wire codes through
// the port: a malformed id is invalid_params, another plugin's namespace is
// denied — never unavailable, which only the loop-down path answers.
func TestBadContributionIdIsInvalidParams(t *testing.T) {
	port, _, _, _, _ := newPort(t)
	plugin := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "alpha.files"}
	ctx := context.Background()
	malformed := port.SlotContribute(ctx, plugin, protocol.SlotContributeParams{SlotID: "s", ID: "no-dot"})
	if code := contracts.CodeOf(malformed); code != "invalid_params" {
		t.Fatalf("malformed id code = %q, want invalid_params", code)
	}
	foreign := port.SlotContribute(ctx, plugin, protocol.SlotContributeParams{SlotID: "s", ID: "beta.notes.x"})
	if code := contracts.CodeOf(foreign); code != "denied" {
		t.Fatalf("foreign-namespace id code = %q, want denied", code)
	}
}

// TestCancelledCtxAnswersByCause pins run's error mapping: a request
// context that ended answers timeout or cancelled — the codes every port
// uses — while a live context on a shut-down loop keeps unavailable. In
// every case the engine call never ran.
func TestCancelledCtxAnswersByCause(t *testing.T) {
	port, _, _, _, clk := newPort(t)
	cli := contracts.Caller{Kind: contracts.CallerCLI}
	show := func(ctx context.Context) error {
		return port.ToastShow(ctx, cli, protocol.ToastShowParams{Level: "info", Title: "t"})
	}

	deadline, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if code := contracts.CodeOf(show(deadline)); code != string(protocol.CodeTimeout) {
		t.Fatalf("expired deadline: code = %q, want timeout", code)
	}

	cancelled, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if code := contracts.CodeOf(show(cancelled)); code != string(protocol.CodeCancelled) {
		t.Fatalf("cancelled: code = %q, want cancelled", code)
	}
	if clk.onLoop.Load() {
		t.Fatal("engine body ran on a dead request context")
	}
}

// TestEngineCallsRunOnTheLoop pins the seam: a port call made off the loop
// still runs its engine body on the loop goroutine, observed where the
// engine's own clock tick runs.
func TestEngineCallsRunOnTheLoop(t *testing.T) {
	port, _, _, _, clk := newPort(t)
	plugin := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "alpha.files"}
	if err := port.ToastShow(context.Background(), plugin, protocol.ToastShowParams{
		Level: "info", Title: "t",
	}); err != nil {
		t.Fatalf("toast: %v", err)
	}
	// The reply is already back, so the observation is ordered after the
	// engine call without touching the loop again.
	if !clk.onLoop.Load() {
		t.Fatal("engine body ran off the loop goroutine")
	}
}

// TestLoopShutdownAnswersUnavailable pins the Post-false path: once the loop
// no longer runs, every method answers unavailable and never touches the
// engine.
func TestLoopShutdownAnswersUnavailable(t *testing.T) {
	port, eng, _, l, _ := newPort(t)
	l.closed.Store(true)
	plugin := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "alpha.files"}
	ctx := context.Background()
	calls := map[string]func() error{
		"surface.update": func() error {
			return port.SurfaceUpdate(ctx, plugin, protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage})
		},
		"surface.close": func() error {
			return port.SurfaceClose(ctx, plugin, protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage})
		},
		"slot.contribute": func() error {
			return port.SlotContribute(ctx, plugin, protocol.SlotContributeParams{SlotID: "s", ID: "x"})
		},
		"toast.show": func() error {
			return port.ToastShow(ctx, plugin, protocol.ToastShowParams{Level: "info", Title: "t"})
		},
	}
	for name, call := range calls {
		if code := contracts.CodeOf(call()); code != string(protocol.CodeUnavailable) {
			t.Fatalf("%s after shutdown: code = %q, want unavailable", name, code)
		}
	}
	if _, ok := eng.ActiveToast(); ok {
		t.Fatal("toast.show ran on the engine after shutdown")
	}
}
