package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/internal/host/ports/launch"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/server"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/internal/services"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/internal/stem/dispatcher"
	"github.com/hyperlab-sh/husk/internal/stem/eventbus"
	"github.com/hyperlab-sh/husk/internal/stem/hooks"
	"github.com/hyperlab-sh/husk/internal/stem/surfaces"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// storeCloseTimeout bounds the final Store flush at shutdown.
const storeCloseTimeout = 10 * time.Second

// loopWait bounds how long Close waits for a loop that accepted a closure but
// never ran it (a wedged loop); past it the closure runs on Close's goroutine.
const loopWait = 5 * time.Second

// Root is the composition root New returns. It implements contracts.Host and
// Looped; the loop forwards every message it does not own to Update.
type Root struct {
	cfg   contracts.Config
	opts  options
	clock protocol.Clock

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // workers the host started (commands, reloads)

	// Stages, in dependency order. Each is nil until built.
	store     *store.Store
	st        *state.State
	rt        *services.Runtime
	table     *calls.Table
	disp      *dispatcher.Dispatcher
	runner    *hooks.Runner
	bus       *eventbus.Bus
	engine    *surfaces.Engine
	pods      *pods.Host
	srv       server.Server
	mux       *handlers.Mux
	catalog   *catalogView
	decl      *declarations
	specs     []manifest.Spec
	loopSeam  loopSeam
	instStart time.Time

	// Lifecycle.
	live       atomicBool // New returned: the loop may be running
	mu         sync.Mutex
	running    bool
	srvStarted bool
	closeOnce  sync.Once
	closeErr   error
	done       chan struct{} // closed when Close finishes
	trace      []string      // shutdown steps in the order they ran (tests)
}

var (
	_ contracts.Host = (*Root)(nil)
	_ Looped         = (*Root)(nil)
)

// Looped is what the host's loop owner needs beyond contracts.Host: the loop
// forwards every message it does not recognize to Update, on the loop
// goroutine, and the host runs its per-tick work (State, Surfaces and Event
// bus flushes, Calls sweep scheduling) there.
type Looped interface {
	contracts.Host
	// Update applies one posted message and reports whether the host owned
	// it. It is loop-only.
	Update(msg any) bool
}

// New builds Store, State, Services, Calls, Pods and the Server in dependency
// order and registers every adapter on the Server; nothing listens and no
// plugin starts until Run. The result also implements Looped. New runs
// before the loop does (cfg.Post may already accept messages but nothing
// consumes them until the loop runs), on a goroutine that happens-before the
// loop's start. On any failure everything already built is closed again.
func New(cfg contracts.Config, opts ...Option) (contracts.Host, error) {
	r, err := build(cfg, opts)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func build(cfg contracts.Config, optFns []Option) (_ *Root, err error) {
	if cfg.Post == nil {
		return nil, errors.New("host: Config.Post is required")
	}
	if err := instance.ValidID(cfg.InstanceID); err != nil {
		return nil, fmt.Errorf("host: instance id: %w", err)
	}
	if cfg.RuntimeDir == "" {
		return nil, errors.New("host: Config.RuntimeDir is required")
	}
	var o options
	for _, fn := range optFns {
		fn(&o)
	}
	clock := cfg.Clock
	if clock == nil {
		clock = wrapper.SystemClock{}
	}
	cfg.Clock = clock
	ctx, cancel := context.WithCancel(context.Background())
	r := &Root{cfg: cfg, opts: o, clock: clock, ctx: ctx, cancel: cancel, done: make(chan struct{}), instStart: clock.Now()}
	r.loopSeam = loopSeam{r}
	defer func() {
		if err != nil {
			_ = r.shutdown()
		}
	}()

	if err = r.stage("manifests", r.loadSpecs); err != nil {
		return nil, err
	}
	if err = r.stage("store", r.buildStore); err != nil {
		return nil, err
	}
	if err = r.stage("state", r.buildState); err != nil {
		return nil, err
	}
	if err = r.stage("services", r.buildServices); err != nil {
		return nil, err
	}
	if err = r.stage("stem", r.buildStem); err != nil {
		return nil, err
	}
	if err = r.stage("pods", r.buildPods); err != nil {
		return nil, err
	}
	if err = r.stage("server", r.buildServer); err != nil {
		return nil, err
	}
	r.live.Store(true)
	return r, nil
}

// stage runs one build step, then the injected build hook (tests fail a
// stage after it succeeded to prove everything already built is closed).
func (r *Root) stage(name string, fn func() error) error {
	if err := fn(); err != nil {
		return fmt.Errorf("host: %s: %w", name, err)
	}
	if r.opts.afterStage != nil {
		if err := r.opts.afterStage(name); err != nil {
			return fmt.Errorf("host: %s: %w", name, err)
		}
	}
	return nil
}

// loadSpecs converts the --plugin and --plugin-dir arguments before anything
// is built, so a bad argument fails with no files, socket or goroutine
// behind it. An explicit --plugin that cannot load is fatal; a bad plugin
// inside a --plugin-dir fails only itself (logged), like the converter's
// LoadDir contract.
func (r *Root) loadSpecs() error {
	reg := manifest.NewRegistry()
	seen := map[string]bool{}
	add := func(spec *manifest.Spec, warns []manifest.Warning) {
		for _, w := range warns {
			r.log(pods.LogMsg{PluginID: specID(spec), Level: "warn", Message: fmt.Sprintf("manifest %s: %s", w.Field, w.Message)})
		}
		if spec == nil || seen[spec.ID] {
			if spec != nil {
				r.log(pods.LogMsg{PluginID: spec.ID, Level: "warn", Message: "duplicate plugin id ignored; the first loaded wins"})
			}
			return
		}
		seen[spec.ID] = true
		r.specs = append(r.specs, *spec)
	}
	for _, arg := range r.cfg.Plugins {
		spec, warns, err := reg.Load(arg)
		if err != nil {
			return fmt.Errorf("--plugin %q: %w", arg, err)
		}
		add(spec, warns)
	}
	for _, dir := range r.cfg.PluginDirs {
		results, err := reg.LoadDir(dir)
		if err != nil {
			return fmt.Errorf("--plugin-dir %q: %w", dir, err)
		}
		for _, res := range results {
			if res.Err != nil {
				r.log(pods.LogMsg{Level: "warn", Message: fmt.Sprintf("--plugin-dir %q: %v", dir, res.Err)})
				continue
			}
			add(res.Spec, res.Warnings)
		}
	}
	return nil
}

func specID(s *manifest.Spec) string {
	if s == nil {
		return ""
	}
	return s.ID
}

func (r *Root) buildStore() error {
	s, err := store.Open(store.Options{
		Instance: store.InstanceRef{ID: r.cfg.InstanceID, Named: instance.ValidName(r.cfg.InstanceID) == nil},
		Env:      store.Env{RuntimeDir: func() (string, error) { return r.cfg.RuntimeDir, nil }},
		Post:     r.post,
	})
	if err != nil {
		return err
	}
	r.store = s
	return nil
}

func (r *Root) buildState() error {
	r.st = state.New(state.Options{Hydrator: r.store})
	return r.store.Attach(r.st)
}

func (r *Root) buildServices() error {
	secretsDoc, err := r.store.Secrets("husk-secrets")
	if err != nil {
		return fmt.Errorf("secrets namespace: %w", err)
	}
	prompt := r.opts.prompt
	if prompt == nil {
		// No dialog is wired: a grant prompt is refused, never granted.
		prompt = denyPrompt
	}
	rt, err := services.New(services.RuntimeOptions{
		Store:     r.store,
		State:     r.st,
		Post:      r.post,
		Publisher: publisher{r},
		Clock:     realClock{},
		Crypto:    secretsDoc,
		Prompt:    prompt,
		Schema:    settings.Builtins(),
		Warn: func(p services.Problem) {
			r.log(pods.LogMsg{Level: "warn", Message: "services: " + p.Message})
		},
		Context: r.ctx,
	})
	if rt == nil {
		return err
	}
	r.rt = rt
	if err != nil { // construction problems that left a usable runtime
		r.log(pods.LogMsg{Level: "warn", Message: "services: " + err.Error()})
	}
	return nil
}

func (r *Root) buildStem() error {
	r.catalog = newCatalogView(r.store)
	r.decl = newDeclarations(r.st)
	live := dispatcher.StateLiveness(r.st)

	r.table = calls.New(lateOutbox{r}, calls.TableOptions{Clock: r.clock, Logger: r.logf})

	var err error
	r.bus, err = eventbus.New(eventbus.Options{Liveness: live, Sinks: sinks{r}, Logger: r.logf})
	if err != nil {
		return fmt.Errorf("event bus: %w", err)
	}
	r.disp, err = dispatcher.New(dispatcher.Options{
		Catalog:  dispatcher.NewDocCatalog(func() map[string]map[string]any { return r.store.Declared(store.DeclaredPlugins) }, nil),
		Liveness: live,
		Focus:    dispatcher.StateFocus(r.st),
		Calls:    r.table,
		Logger:   r.logf,
	})
	if err != nil {
		return fmt.Errorf("dispatcher: %w", err)
	}
	r.runner, err = hooks.New(hooks.Options{Subscriptions: r.catalog, Liveness: live, Calls: r.table, Logger: r.logf})
	if err != nil {
		return fmt.Errorf("hook runner: %w", err)
	}
	r.engine, err = surfaces.New(r.st, surfaces.Options{
		Clock:        r.clock,
		Liveness:     live,
		Logger:       printfLog{r},
		Publications: r.catalog,
		Declarations: r.decl,
	})
	if err != nil {
		return fmt.Errorf("surfaces: %w", err)
	}
	r.catalog.onSlotsChanged = r.engine.CatalogChanged
	return nil
}

func (r *Root) buildPods() error {
	registry, err := launch.NewPodRegistry(r.st)
	if err != nil {
		return err
	}
	binPath, _ := os.Executable()
	loop := hostLoop{
		Loop: launch.Loop{
			Context:  r.ctx,
			Registry: registry,
			Preparer: launch.Preparer{
				Catalog: launch.CatalogDeclarer{Store: r.store},
				Slices:  launch.SliceRegistrar{State: r.st},
				Reload:  launch.Reloader{Coordinator: r.rt.Coordinator},
			},
			PostFn:    func(m tea.Msg) { r.cfg.Post(m) },
			OpeningFn: r.opening,
			LogFn:     r.log,
			DoFn:      r.do,
		},
		r: r,
	}
	h, err := pods.New(loop, pods.Options{
		Registry: registry,
		Factory:  r.opts.factory,
		Wrapper: wrapper.Options{
			Context:    r.ctx,
			Clock:      r.clock,
			SocketPath: instance.SocketPath(r.cfg.RuntimeDir, r.cfg.InstanceID),
			InstanceID: r.cfg.InstanceID,
			BinPath:    binPath,
			LogPath:    r.openPluginLog,
		},
	})
	if err != nil {
		return err
	}
	r.pods = h
	return nil
}

// openPluginLog creates the plugin's log through Store's exclusive-create
// rule and hands the wrapper the path; the wrapper reopens it for stderr.
func (r *Root) openPluginLog(pluginID string) (string, error) {
	f, err := r.store.OpenPluginLog(pluginID)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// Run starts the Server (listening, registered with internal/instance) and
// launches the configured plugins through the launch sequence, then blocks
// until ctx ends or Close is called. A startup failure closes everything
// already started before returning the error. Run does not close the host on
// a clean end; the caller calls Close.
func (r *Root) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errors.New("host: Run called twice")
	}
	select {
	case <-r.done:
		r.mu.Unlock()
		return errors.New("host: closed")
	default:
	}
	r.running = true
	r.mu.Unlock()

	if err := r.srv.Start(); err != nil {
		_ = r.Close()
		return fmt.Errorf("host: start server: %w", err)
	}
	r.mu.Lock()
	r.srvStarted = true
	r.mu.Unlock()
	if err := r.launchPlugins(ctx); err != nil {
		_ = r.Close()
		return err
	}
	select {
	case <-ctx.Done():
	case <-r.done:
	}
	return nil
}

// launchPlugins adds every configured plugin as one batch and requests its
// launch sequence. With no plugin to launch there is no launch reload, so the
// initial Services load runs here instead.
func (r *Root) launchPlugins(ctx context.Context) error {
	if r.opts.afterStage != nil {
		if err := r.opts.afterStage("run"); err != nil {
			return fmt.Errorf("host: run: %w", err)
		}
	}
	var cmd tea.Cmd
	if err := r.do(ctx, func() {
		for _, spec := range r.specs {
			if _, err := r.pods.Add(spec); err != nil {
				r.log(pods.LogMsg{PluginID: spec.ID, Level: "warn", Message: "plugin not added: " + err.Error()})
			}
		}
		cmd = r.pods.Prepare()
	}); err != nil {
		return fmt.Errorf("host: launch plugins: %w", err)
	}
	if cmd != nil {
		r.spawn(cmd)
		return nil
	}
	r.spawn(func() tea.Msg {
		_, _ = r.rt.Coordinator.Reload(r.ctx, services.ReasonPlugin)
		return nil
	})
	return nil
}

// spawn runs a Bubble Tea style command on a host-owned worker and posts its
// message to the loop.
func (r *Root) spawn(cmd tea.Cmd) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if msg := cmd(); msg != nil {
			r.cfg.Post(msg)
		}
	}()
}

// Close stops everything in the order pods.md fixes and is idempotent: the
// Calls table first (waiting socket callers get cancelled), the Server (stops
// accepting, unregisters the instance), the plugin processes, then the final
// State flush and the Store flush. Concurrent and repeated calls share one
// shutdown and its result.
func (r *Root) Close() error {
	r.closeOnce.Do(func() {
		r.closeErr = r.shutdown()
		close(r.done)
	})
	<-r.done
	return r.closeErr
}

// shutdown tolerates a partially built root: every step skips what was never
// built, so New's failure path and Close share it.
func (r *Root) shutdown() error {
	var errs []error
	note := func(step string) {
		r.mu.Lock()
		r.trace = append(r.trace, step)
		r.mu.Unlock()
		if r.opts.onClose != nil {
			r.opts.onClose(step)
		}
	}

	var stop tea.Cmd
	if r.table != nil || r.pods != nil {
		r.exec(func() {
			if r.table != nil {
				r.complete(r.table.Close())
			}
			if r.pods != nil {
				stop = r.pods.Stop()
			}
		})
		note("calls")
	}
	if r.srv != nil {
		if err := r.srv.Close(); err != nil {
			errs = append(errs, fmt.Errorf("server: %w", err))
		}
		note("server")
	}
	if stop != nil {
		if sm, ok := stop().(pods.StoppedMsg); ok && sm.Err != nil {
			errs = append(errs, sm.Err)
		}
		note("pods")
	}
	if r.st != nil {
		r.exec(r.tick) // final flush: Store sees the last writes before it closes
	}
	r.cancel()
	if r.rt != nil {
		r.rt.Close()
	}
	r.wg.Wait()
	if r.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), storeCloseTimeout)
		if err := r.store.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("store: %w", err))
		}
		cancel()
		note("store")
	}
	return errors.Join(errs...)
}
