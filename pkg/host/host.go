package host

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/bundle"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/host/ports/launch"
	"github.com/hyperlab-sh/husk/pkg/instance"
	"github.com/hyperlab-sh/husk/pkg/pods"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/pods/server"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/services"
	"github.com/hyperlab-sh/husk/pkg/services/secrets"
	"github.com/hyperlab-sh/husk/pkg/services/settings"
	"github.com/hyperlab-sh/husk/pkg/stem/calls"
	"github.com/hyperlab-sh/husk/pkg/stem/dispatcher"
	"github.com/hyperlab-sh/husk/pkg/stem/eventbus"
	"github.com/hyperlab-sh/husk/pkg/stem/hooks"
	"github.com/hyperlab-sh/husk/pkg/stem/surfaces"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

// storeCloseTimeout bounds the final Store flush at shutdown.
const storeCloseTimeout = 10 * time.Second

// loopWait bounds how long Close waits for the loop to run a loop-confined
// shutdown step. A loop that does not run it in time is wedged: the step is
// skipped and Close reports it, because running loop-confined work on Close's
// goroutine while the loop may still be alive would be a data race.
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
	bundle         *bundle.Bundle
	bundleWarnings []bundle.Warning
	store          *store.Store
	st             *state.State
	rt             *services.Runtime
	table          *calls.Table
	disp           *dispatcher.Dispatcher
	runner         *hooks.Runner
	bus            *eventbus.Bus
	engine         *surfaces.Engine
	pods           *pods.Host
	srv            server.Server
	mux            *handlers.Mux
	catalog        *catalogView
	decl           *declarations
	specs          []manifest.Spec
	converter      *manifest.Loader
	loopSeam       loopSeam
	instStart      time.Time

	// Frontend support.
	pluginCatalog  catalogCache
	frontendSlices *contracts.FrontendSlices // set once by OwnSlices
	hostActions    map[string]bool           // ids bound by RegisterHostAction
	catMu          sync.Mutex                // guards the cached declared catalog
	cat            declaredCatalog
	catRev         uint64
	catValid       bool

	secretLive map[string]bool                   // Pods live at the previous State flush
	podWatch   map[string]func()                 // cancels each Pod's surface.resized watcher
	sizesDirty bool                              // geometry, focus or visibility changed this tick
	lastSizes  map[protocol.SurfaceID]pushedSize // what each surface's occupant was last told
	refusalsMu sync.Mutex                        // guards refusals
	refusals   []contracts.PluginRefusal         // plugins refused before a Pod existed
	afterState []func()                          // lifecycle announcements published after State change events
	reloadSeq  atomic.Uint64                     // keys services.reloaded events apart
	noticesMu  sync.Mutex                        // guards noticeSeq and lastNotice
	noticeSeq  uint64                            // reloads that ended with errors
	lastNotice contracts.ConfigNotice            // the newest of them
	declWarnMu sync.Mutex                        // guards declWarned
	declWarned map[string]bool                   // plugins whose declared document failed to decode

	servicesRevision atomic.Uint64 // reload events delivered to the loop

	// Before New returns, the builder owns State and drains posted callbacks.
	postMu         sync.Mutex
	buildPosts     []func()
	startupReports []services.Report // immutable construction result for the frontend

	// Lifecycle.
	live      atomicBool   // New returned: the loop may be running
	loopGID   atomic.Int64 // goroutine id of the loop, learned on its first Update
	mu        sync.Mutex   // guards running and closing
	running   bool
	closing   bool         // shutdown began: spawn refuses new workers
	workers   atomic.Int32 // host workers not yet finished (for the shutdown error)
	closeOnce sync.Once
	closeErr  error
	// closingCtx ends when shutdown reaches the Server: socket requests still
	// waiting on the loop fail fast instead of holding the Server's drain.
	closingCtx    context.Context
	endClosingCtx context.CancelFunc
	done          chan struct{} // closed when Close finishes
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
	// it. It is loop-only. Close may be called from Update (see Root.Close).
	Update(msg any) bool
	// Tick runs the end-of-Update flushes. A loop owner calls it at the end
	// of an Update that wrote frontend-owned State without posting a host
	// message, so those writes flush and reach the occupants in the same
	// Update. Loop-only.
	Tick()
	// Planned is the number of plugins the configured --plugin and
	// --plugin-dir arguments resolved to, fixed when the host was built.
	// Unlike the argument count it is zero for an empty directory.
	Planned() int
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
	r.closingCtx, r.endClosingCtx = context.WithCancel(context.Background())
	r.loopSeam = loopSeam{r}
	defer func() {
		if err != nil {
			_ = r.shutdown()
			if r.opts.afterFail != nil {
				r.opts.afterFail(r)
			}
		}
	}()

	if err = r.stage("bundle", r.buildBundle); err != nil {
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
	if err = r.stage("startup", r.startup); err != nil {
		return nil, err
	}
	if err = r.stage("manifests", r.loadSpecs); err != nil {
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
	// Finish the initial worker's post while the builder still owns the loop.
	// Later refreshes never wait; construction must not leave a stray Update
	// racing the caller's permitted pre-Run OwnSlices writes.
	<-r.warmCatalog()
	r.drainBuildPosts(true)
	return r, nil
}

// stage runs one build step, then the injected build hook (tests fail a
// stage after it succeeded to prove everything already built is closed).
func (r *Root) stage(name string, fn func() error) error {
	if err := fn(); err != nil {
		var launch *bundleStartError
		if errors.As(err, &launch) {
			return err
		}
		return fmt.Errorf("host: %s: %w", name, err)
	}
	if r.opts.afterStage != nil {
		if err := r.opts.afterStage(name); err != nil {
			return fmt.Errorf("host: %s: %w", name, err)
		}
	}
	return nil
}

// loadSpecs converts launch arguments from the startup Settings snapshot.
// Bad explicit paths or unreadable directories are fatal after Store, State
// and Services exist; New's failure defer closes them. A bad plugin inside a
// directory fails only itself (logged), like the converter's LoadDir contract.
func (r *Root) loadSpecs() error {
	for _, w := range r.bundleWarnings {
		r.log(pods.LogMsg{Level: "warn", Message: w.Message})
	}
	r.converter = manifest.NewRegistry()
	reg := r.converter
	snap := r.rt.Settings.Snapshot()
	var converters []*manifest.ExecConverter
	registered := map[string]bool{}
	supplied := map[string]bool{}
	for _, path := range append(append([]string(nil), r.opts.converters...), snap.Strings("husk.plugins.converters")...) {
		if supplied[path] {
			continue
		}
		supplied[path] = true
		converter, err := manifest.NewExecConverter(path, manifest.ExecOptions{Clock: r.clock})
		if err != nil {
			r.log(pods.LogMsg{Level: "warn", Message: err.Error()})
			r.refuse("", err.Error())
			continue
		}
		if registered[converter.Path()] {
			continue
		}
		registered[converter.Path()] = true
		reg.Register(converter)
		converters = append(converters, converter)
	}
	seen := map[string]bool{}
	attempted := map[string]bool{}
	remember := func(dir string) {
		if abs, err := filepath.Abs(dir); err == nil {
			attempted[filepath.Clean(abs)] = true
		}
	}
	add := func(spec *manifest.Spec, warns []manifest.Warning) {
		for _, w := range warns {
			r.log(pods.LogMsg{PluginID: specID(spec), Level: "warn", Message: fmt.Sprintf("manifest %s: %s", w.Field, w.Message)})
		}
		if spec == nil || seen[spec.ID] {
			if spec != nil {
				r.log(pods.LogMsg{PluginID: spec.ID, Level: "warn", Message: "duplicate plugin id ignored; the first loaded wins"})
				r.refuse(spec.ID, "duplicate plugin id")
			}
			return
		}
		seen[spec.ID] = true
		r.specs = append(r.specs, *spec)
	}
	for i, arg := range snap.Strings("husk.plugins.paths") {
		if info, err := os.Stat(arg); err == nil {
			if info.IsDir() {
				remember(arg)
			} else {
				remember(filepath.Dir(arg))
			}
		}
		spec, warns, err := reg.Load(arg)
		if err != nil {
			var failure *manifest.ConverterError
			if errors.As(err, &failure) {
				r.log(pods.LogMsg{Level: "warn", Message: err.Error()})
				r.refuse("", err.Error())
				continue
			}
			return fmt.Errorf("husk.plugins.paths[%d] %q (layer %s): %w", i, arg, snap.Source("husk.plugins.paths"), err)
		}
		add(spec, warns)
	}
	dirs := snap.Strings("husk.plugins.dirs")
	if r.bundle != nil && r.bundle.PluginsDir != "" {
		dirs = append(dirs, r.bundle.PluginsDir)
	}
	for i, dir := range dirs {
		results, err := reg.LoadDir(dir)
		if err != nil {
			return fmt.Errorf("husk.plugins.dirs[%d] %q (layer %s): not a readable directory: %w", i, dir, snap.Source("husk.plugins.dirs"), err)
		}
		for _, res := range results {
			remember(res.Dir)
			if res.Err != nil {
				r.log(pods.LogMsg{Level: "warn", Message: fmt.Sprintf("husk.plugins.dirs %q: %v", dir, res.Err)})
				r.refuse("", res.Err.Error())
				continue
			}
			add(res.Spec, res.Warnings)
		}
	}
	for _, converter := range converters {
		discovered, err := converter.Discover()
		if err != nil {
			r.log(pods.LogMsg{Level: "warn", Message: err.Error()})
			r.refuse("", err.Error())
			continue
		}
		for _, dir := range discovered {
			if attempted[dir] {
				continue
			}
			remember(dir)
			spec, warnings, err := reg.LoadPath(dir)
			if err != nil {
				r.log(pods.LogMsg{Level: "warn", Message: err.Error()})
				r.refuse("", err.Error())
				continue
			}
			add(spec, warnings)
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

type bundleStartError struct {
	dir string
	err error
}

func (e *bundleStartError) Error() string {
	if e.dir == "" {
		return e.err.Error()
	}
	return fmt.Sprintf("bundle %s: %v", e.dir, e.err)
}
func (e *bundleStartError) Unwrap() error { return e.err }

func (r *Root) instanceDocs() map[store.DocName]store.Doc {
	docs := make(map[store.DocName]store.Doc, len(r.cfg.InstanceDocs))
	for name, doc := range r.cfg.InstanceDocs {
		docs[store.DocName(name)] = doc
	}
	return docs
}

func (r *Root) buildBundle() error {
	bootstrap, err := store.ReadBootstrap(store.Env{}, store.InstanceRef{ID: r.cfg.InstanceID}, r.instanceDocs())
	if err != nil {
		return &bundleStartError{err: err}
	}
	if bootstrap.Bundle == "" {
		return nil
	}
	r.bundle, r.bundleWarnings, err = bundle.Load(bootstrap.Bundle)
	if err != nil {
		return &bundleStartError{dir: bootstrap.Bundle, err: err}
	}
	return nil
}

func (r *Root) buildStore() error {
	// This is deliberately after bundle validation, including in CLI start
	// mode, so fatal bundle errors leave the user's directory tree untouched.
	if err := instance.CheckRuntimeDir(r.cfg.RuntimeDir); err != nil {
		return err
	}
	var config fs.FS
	if r.bundle != nil {
		config = r.bundle.Config
	}
	docs := r.instanceDocs()
	s, err := store.Open(store.Options{
		Bundle:       config,
		InstanceDocs: docs,
		Instance:     store.InstanceRef{ID: r.cfg.InstanceID},
		RuntimeDir:   r.cfg.RuntimeDir,
		Clock:        storeClock{r.clock},
		Post:         r.post,
		Keyring:      r.opts.keyring,
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
	// No dialog wired (a headless host): the service answers every ungranted
	// read user_denied and remembers nothing.
	var prompt func(context.Context, secrets.Prompt) secrets.Answer
	if show := r.opts.prompt; show != nil {
		prompt = func(ctx context.Context, p secrets.Prompt) secrets.Answer {
			// The line names who asks for what, never the purpose.
			r.log(pods.LogMsg{Level: "info", Message: "secrets: dialog shown to grant " + p.Name + " to " + p.Plugin})
			return show(ctx, p)
		}
	}
	rt, err := services.New(services.RuntimeOptions{
		Writer:          r.store,
		Store:           r.store,
		State:           r.st,
		Post:            r.post,
		Publisher:       publisher{r},
		Clock:           storeClock{r.clock},
		Crypto:          secretsDoc,
		Prompt:          prompt,
		ProfileFlag:     r.cfg.ProfileFlag,
		PluginAdditions: r.opts.pluginAdditions,
		Audit: func(name string) {
			r.log(pods.LogMsg{Level: "info", Message: "secrets: " + name + " revealed to cli"})
		},
		Warn: func(msg string) {
			r.log(pods.LogMsg{Level: "warn", Message: msg})
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

// startup performs the one launch-time load, before any manifests are declared.
// Flight failures and invalid plugin launch settings are fatal; other service
// validation failures retain each Settings key's independently validated value.
func (r *Root) startup() error {
	reports, err := r.rt.Coordinator.Reload(r.ctx, services.ReasonStartup)
	r.startupReports = reports
	r.drainBuildPosts(false)
	problems := 0
	for _, report := range reports {
		for _, p := range report.Problems {
			if p.Severity == "error" {
				problems++
			}
		}
	}
	if startupFatal(reports, err, r.opts.pluginAdditions) {
		return startupError(reports, err)
	}
	r.rt.Settings.FreezeStartConsumed()
	if problems > 0 {
		r.log(pods.LogMsg{Level: "warn", Message: fmt.Sprintf("services: startup reload: %d problems", problems)})
	}
	return nil
}

// startupFatal rejects failed flights and invalid plugin launch settings.
// Other service validation remains nonfatal; a flight may fail after earlier
// services have already applied their snapshots.
func startupFatal(reports []services.Report, err error, additions map[string][]string) bool {
	if len(reports) == 0 && err != nil {
		return true
	}
	for _, report := range reports {
		for _, p := range report.Problems {
			switch p.Code {
			case "store_error", "timed_out", "cancelled":
				return true
			case "invalid_value":
				// Additive flags supply a valid array while retaining config
				// diagnostics, including a named-instance file's invalid value.
				// A flag path resolution failure has no source file and stays fatal.
				if report.Service == "settings" && strings.HasPrefix(p.Key, "husk.plugins.") && (p.Layer != "instance" || p.Source != nil && p.Source.File != "") && len(additions[strings.TrimPrefix(p.Key, "husk.plugins.")]) > 0 {
					continue
				}
				if report.Service == "settings" && settings.IsStartConsumed(p.Key) {
					return true
				}
			}
		}
	}
	return false
}

func startupError(reports []services.Report, err error) error {
	if err == nil {
		err = errors.New("startup reload failed")
	}
	var details, codes []string
	seen := map[string]bool{}
	sameMessage := true
	for _, report := range reports {
		for _, p := range report.Problems {
			text := fmt.Sprintf("%s: %s", p.Code, p.Message)
			if p.Key != "" {
				text += fmt.Sprintf(" (key %s)", p.Key)
			}
			// A message that already names its file (store_error does) carries
			// its provenance; repeating it would name the file twice.
			provenance := p.Source != nil && !strings.Contains(p.Message, "file "+p.Source.File)
			if provenance {
				text += fmt.Sprintf(" (layer %s, file %s)", p.Source.Layer, p.Source.File)
			}
			// Do not discard keys or provenance that exist only on the problem.
			if p.Message != err.Error() || p.Key != "" || provenance {
				sameMessage = false
			}
			if !seen[text] {
				seen[text] = true
				details = append(details, text)
				codes = append(codes, p.Code)
			}
		}
	}
	if len(details) == 0 {
		return err
	}
	if sameMessage {
		return fmt.Errorf("%s: %w", strings.Join(codes, ", "), err)
	}
	return fmt.Errorf("%w: %s", err, strings.Join(details, "; "))
}

func (r *Root) buildStem() error {
	r.catalog = newCatalogView(r.store)
	r.catalog.logf = r.logf
	r.decl = newDeclarations(r.st)
	live := dispatcher.StateLiveness(r.st)

	r.table = calls.New(lateOutbox{r}, calls.TableOptions{Clock: r.clock, Logger: r.logf})

	var err error
	r.bus, err = eventbus.New(eventbus.Options{Liveness: live, Sinks: sinks{r}, Logger: r.logf})
	if err != nil {
		return fmt.Errorf("event bus: %w", err)
	}
	r.disp, err = dispatcher.New(dispatcher.Options{
		Catalog:  dispatcher.NewDocCatalog(func() map[string]map[string]any { return r.store.Declared(store.DeclaredPlugins) }, r.hostActionDefs),
		Liveness: live,
		Focus:    dispatcher.StateFocus(r.st),
		Calls:    r.table,
		Logger:   r.logf,
	})
	if err != nil {
		return fmt.Errorf("dispatcher: %w", err)
	}
	r.runner, err = hooks.New(hooks.Options{Catalog: r.catalog, Liveness: live, Calls: r.table, Logger: r.logf})
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
	r.st.OnFlush(r.publishStateChanges)
	r.st.OnFlush(r.markSizesDirty)
	r.st.OnFlush(r.markLivenessChanged)
	return nil
}

func (r *Root) buildPods() error {
	registry, err := launch.NewPodRegistry(r.st)
	if err != nil {
		return err
	}
	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
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
		Registry:  registry,
		Publisher: publisher{r},
		Workers:   lifecycle{r},
		Converter: r.converter,
		Factory:   r.opts.factory,
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
	// Pods own the theme.changed push: an in-process watcher of the same
	// event cli watchers get, not a callback Services make.
	if _, err := r.bus.Subscribe(reloadWatcher{r}, servicesReloadedEvent); err != nil {
		return fmt.Errorf("services.reloaded watcher: %w", err)
	}
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

// Run starts the Server (listening, registered with pkg/instance) and
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
// launch sequence. The startup flight already ran during construction.
func (r *Root) launchPlugins(ctx context.Context) error {
	if r.opts.afterStage != nil {
		if err := r.opts.afterStage("run"); err != nil {
			return fmt.Errorf("host: run: %w", err)
		}
	}
	var cmd tea.Cmd
	if err := r.do(ctx, func() {
		var ids []string
		for _, spec := range r.specs {
			if _, err := r.pods.AddPod(spec); err != nil {
				r.log(pods.LogMsg{PluginID: spec.ID, Level: "warn", Message: "plugin not added: " + err.Error()})
				r.refuse(spec.ID, err.Error())
				continue
			}
			r.watchPod(spec.ID)
			ids = append(ids, spec.ID)
		}
		cmd = r.pods.Prepare(ids...)
	}); err != nil {
		if r.ctx.Err() != nil {
			// Close ran before the launch batch was added: nothing is left to launch.
			return nil
		}
		return fmt.Errorf("host: launch plugins: %w", err)
	}
	if cmd != nil {
		r.spawn(cmd)
	}
	return nil
}

// spawn runs a Bubble Tea style command on a host-owned worker and posts its
// message to the loop. It reports whether a worker was accepted.
func (r *Root) spawn(cmd tea.Cmd) bool {
	r.mu.Lock()
	if r.closing {
		// Shutdown is waiting on (or past) wg: a late worker would race
		// Wait and run against a closed Runtime and a cancelled ctx.
		r.mu.Unlock()
		if r.opts.onSpawn != nil {
			r.opts.onSpawn(false)
		}
		return false
	}
	r.wg.Add(1)
	r.workers.Add(1)
	r.mu.Unlock()
	if r.opts.onSpawn != nil {
		r.opts.onSpawn(true)
	}
	go func() {
		defer r.wg.Done()
		defer r.workers.Add(-1)
		if msg := cmd(); msg != nil {
			r.cfg.Post(msg)
		}
	}()
	return true
}

// Close stops everything in the order pods.md fixes and is idempotent: the
// Calls table first (waiting socket callers get cancelled), the Server (stops
// accepting, unregisters the instance), the plugin processes, then the final
// State flush and the Store flush. Concurrent and repeated calls share one
// shutdown and its result.
//
// Close may be called from any goroutine, including the loop's own (from
// Update): loop-confined steps run directly when the caller is the loop
// goroutine and are posted to the loop otherwise. If the loop is alive but
// does not run a posted step within loopWait it is wedged; Close then skips
// that step and the final flush (one wait, not two) and returns an error
// saying so (plugin processes may outlive the host in that case), rather than
// racing the loop. Stopping the loop before Close is also fine: nothing else
// touches the modules then, so the steps run inline.
//
// Close from Update blocks the loop goroutine while the plugin processes stop
// and the workers finish, and those workers end by calling Config.Post. So the
// loop owner must close its post gate before calling Close from Update: Post
// then returns false instead of blocking, which is what Config.Post requires
// of a closed gate. The pods.Stop kill path and the wrapper's exit notice also
// call Post on the loop goroutine and need the gate closed; nothing bounds
// them. Only the wait for host workers is bounded (storeCloseTimeout): when it
// expires Close returns an error with the count of stuck workers and does not
// close the Store, so a worker still inside a command never sees a closed
// Store. The Store then stays open until the process exits.
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
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	note := func(step string) {
		if r.opts.onClose != nil {
			r.opts.onClose(step)
		}
	}

	var stop tea.Cmd
	wedged := false
	if r.table != nil || r.pods != nil {
		if err := r.exec(func() {
			if r.table != nil {
				r.complete(r.table.Close())
			}
			if r.engine != nil {
				r.engine.CloseTerminals()
			}
			if r.pods != nil {
				stop = r.pods.Stop()
			}
		}); err != nil {
			wedged = true
			errs = append(errs, fmt.Errorf("calls and pods not stopped: %w", err))
		}
		note("calls")
	}
	if r.endClosingCtx != nil {
		// Requests posted to a loop that is busy closing the host will never
		// be answered: end their waits so the Server's drain only waits for
		// replies that exist.
		r.endClosingCtx()
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
	if r.st != nil && !wedged { // a loop that did not answer once will not answer again
		if err := r.exec(r.tick); err != nil { // final flush: Store sees the last writes before it closes
			errs = append(errs, fmt.Errorf("final state flush skipped: %w", err))
		}
	}
	r.cancel()
	if r.rt != nil {
		r.rt.Close()
	}
	workersStuck := false
	workersDone := make(chan struct{})
	go func() { r.wg.Wait(); close(workersDone) }()
	wctx, wcancel := context.WithTimeout(context.Background(), r.opts.workerWaitOr(storeCloseTimeout))
	select {
	case <-workersDone:
	case <-wctx.Done():
		workersStuck = true
		errs = append(errs, fmt.Errorf("%d workers still running after %v: store left open", r.workers.Load(), r.opts.workerWaitOr(storeCloseTimeout)))
	}
	wcancel()
	if r.store != nil && !workersStuck {
		ctx, cancel := context.WithTimeout(context.Background(), storeCloseTimeout)
		if err := r.store.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("store: %w", err))
		}
		cancel()
		note("store")
	}
	return errors.Join(errs...)
}

// Planned implements Looped.
func (r *Root) Planned() int { return len(r.specs) }
