package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/manifest"
)

// Plugin process lifecycle.
//
// Every plugin child gets its stderr written to its own log file under the
// socket directory (logs/<id>.log). When a plugin exits while the host is
// running, the exit status and the last stderr lines are logged and toasted.
// On shutdown the host sends SIGTERM to every child, waits up to stopGrace for
// them to exit, then kills the rest (os/exec does both through the plugin
// context's Cancel and WaitDelay) and waits for those too.

const (
	pluginStopGrace = 2 * time.Second
	// pluginHandoffGrace is how long the manifest declarations of a socket
	// launcher that exited 0 wait for the plugin it started to send hello.
	// Past it they are dropped as if the process had failed. Slow starts
	// (runtimes, first-run builds) fit well inside 30s; a plugin that connects
	// later is still checked against its manifest but must declare for itself.
	pluginHandoffGrace = 30 * time.Second
	// pluginKillWait bounds the wait after SIGKILL; it only matters when a
	// grandchild keeps a stdio pipe open after the plugin itself died.
	pluginKillWait = time.Second
	// stderrTailLines is how many trailing stderr lines an exit report shows.
	stderrTailLines = 3
	pluginLogDir    = "logs"
)

// pluginProcess is one plugin child process the host launched.
type pluginProcess struct {
	id        string
	transport string
	cmd       *exec.Cmd
	logPath   string
	helloSeen atomic.Bool   // stdio only: the process's stream delivered a hello
	done      chan struct{} // closed once Wait has returned
}

// pluginExitedMsg reports a plugin process that exited while the host was
// running.
type pluginExitedMsg struct {
	id          string
	status      string // "exit status 7", "signal: killed", or a Wait error
	beforeHello bool   // known only for stdio, where stream and process match
	// handedOff is set for a socket plugin that exited successfully: it may
	// be a launcher that started the real plugin, which can still connect.
	handedOff  bool
	logPath    string   // stderr log file
	stderrTail []string // last non-empty stderr lines, ANSI stripped
}

// handoffExpiredMsg fires pluginHandoffGrace after a launcher's clean exit.
type handoffExpiredMsg struct{ id string }

// pluginLaunchFailedMsg reports a plugin whose process could not be started.
type pluginLaunchFailedMsg struct {
	id  string
	err error
}

// launchPlugin starts a manifest plugin with its declared transport. A failure
// is reported to the event loop (log and toast) as well as returned.
func (m *WorkbenchModel) launchPlugin(p *manifest.Plugin) error {
	transport := p.Transport
	if transport == "" {
		transport = manifest.TransportSocket
	}
	if m.manifests == nil {
		m.manifests = make(map[string]*manifest.Plugin)
	}
	var err error
	if _, dup := m.manifests[p.ID]; dup {
		err = fmt.Errorf("duplicate plugin id %q: another plugin with this id is already loaded", p.ID)
	} else if err = m.registry.declareFromManifest(p); err != nil {
		err = fmt.Errorf("invalid declaration in %s: %w", manifest.FileName, err)
	} else {
		m.manifests[p.ID] = p
		err = m.spawnPlugin(p.ID, transport, p.WorkDir(), p.ResolvedCommand())
		if err != nil {
			delete(m.manifests, p.ID)
			m.registry.removePlugin(p.ID)
		}
	}
	if err != nil {
		msg := pluginLaunchFailedMsg{id: p.ID, err: err}
		select {
		case m.subChan <- msg:
		default:
			// The event loop has not started and its queue is full; log
			// directly so the failure is still recorded.
			m.logf("failed to launch plugin %s: %v", p.ID, err)
		}
	}
	return err
}

// pluginEnv returns env for a plugin child: HUSK_SOCKET_PATH is set for the
// socket transport and removed for stdio, so a client library that prefers the
// socket when the variable is present really talks over stdin/stdout.
func pluginEnv(base []string, transport, socketPath string) []string {
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if !strings.HasPrefix(kv, "HUSK_SOCKET_PATH=") {
			out = append(out, kv)
		}
	}
	if transport != manifest.TransportStdio {
		out = append(out, "HUSK_SOCKET_PATH="+socketPath)
	}
	return out
}

// spawnPlugin starts one plugin process. For stdio the process's stdin and
// stdout carry the protocol; for socket the plugin connects to
// HUSK_SOCKET_PATH. In both cases stderr goes to the plugin's log file.
func (m *WorkbenchModel) spawnPlugin(id, transport, dir string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	if transport != manifest.TransportSocket && transport != manifest.TransportStdio {
		return fmt.Errorf("unknown transport %q", transport)
	}
	logFile, err := m.openPluginLog(id)
	if err != nil {
		return fmt.Errorf("open stderr log: %w", err)
	}
	logPath := logFile.Name()
	fail := func(err error) error {
		_ = logFile.Close()
		_ = os.Remove(logPath)
		return err
	}
	// The shared pluginCtx is cancelled once, by stopPlugins. Cancel then
	// sends SIGTERM and, WaitDelay later, os/exec kills the process itself;
	// the watcher starts in Start, so this holds even while the stdio stream
	// is still being served and Wait has not been called yet.
	cmd := exec.CommandContext(m.pluginCtx, argv[0], argv[1:]...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = m.stopGrace
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = pluginEnv(os.Environ(), transport, m.socketPath)
	// An *os.File is handed to the child as-is: no copy goroutine, so Wait
	// cannot hang on a grandchild that inherited stderr.
	cmd.Stderr = logFile
	var (
		stdin  io.WriteCloser
		stdout io.ReadCloser
	)
	if transport == manifest.TransportStdio {
		if stdin, err = cmd.StdinPipe(); err != nil {
			return fail(err)
		}
		if stdout, err = cmd.StdoutPipe(); err != nil {
			return fail(err)
		}
	}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	_ = logFile.Close() // the child holds its own descriptor

	proc := &pluginProcess{id: id, transport: transport, cmd: cmd, logPath: logPath, done: make(chan struct{})}
	m.procs = append(m.procs, proc)
	go func() {
		defer close(proc.done)
		if stdout != nil {
			// Wait closes the pipes, so the stream must end first.
			m.serveStreamFor(id, stdout, stdin, stdin, func() { proc.helloSeen.Store(true) })
		}
		werr := cmd.Wait()
		if m.shuttingDown() {
			return
		}
		m.post(pluginExitedMsg{
			id:          id,
			status:      exitStatus(cmd, werr),
			beforeHello: transport == manifest.TransportStdio && !proc.helloSeen.Load(),
			handedOff:   transport == manifest.TransportSocket && cmd.ProcessState != nil && cmd.ProcessState.Success(),
			logPath:     logPath,
			stderrTail:  tailLines(logPath, stderrTailLines),
		})
	}()
	return nil
}

func exitStatus(cmd *exec.Cmd, err error) string {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.String()
	}
	if err != nil {
		return err.Error()
	}
	return "exited"
}

// openPluginLog creates a fresh 0600 log file for id under the socket
// directory's logs/ subdirectory. Two plugins with the same id get distinct
// files.
func (m *WorkbenchModel) openPluginLog(id string) (*os.File, error) {
	if m.socketPath == "" {
		return nil, errors.New("no runtime directory")
	}
	dir := filepath.Join(filepath.Dir(m.socketPath), pluginLogDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	base := sanitizeLogName(id)
	for i := 1; i <= 100; i++ {
		name := base + ".log"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.log", base, i)
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, fmt.Errorf("too many log files for %q", id)
}

func sanitizeLogName(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.TrimLeft(b.String(), ".")
	if s == "" {
		s = "plugin"
	}
	return s
}

// tailLines returns the last n non-empty lines of the file's final 8 KiB with
// ANSI escapes and other control characters removed.
func tailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	const window = 8 << 10
	if st, err := f.Stat(); err == nil && st.Size() > window {
		_, _ = f.Seek(st.Size()-window, io.SeekStart)
	}
	data, _ := io.ReadAll(f)
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 0x20 && r != '\t' || r == 0x7f {
				return -1
			}
			return r
		}, ansi.Strip(line)))
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// handleLifecycleMsg logs and toasts process-level events; it returns the
// toast command, or nil for any other message.
func (m *WorkbenchModel) handleLifecycleMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case pluginExitedMsg:
		when := ""
		if msg.beforeHello {
			when = " before hello"
		}
		m.logf("plugin %s exited%s (%s); stderr log: %s", msg.id, when, msg.status, msg.logPath)
		var expiry tea.Cmd
		if m.plugins[msg.id] == nil {
			// Exited without a registered connection (before hello, or
			// never connected). A socket launcher that exited 0 may have
			// started the real plugin, which connects later: keep the
			// declarations its manifest registered at launch, for at most
			// handoffGrace. A failed exit, or a stdio stream (which ended
			// with its process), drops them. The manifest itself stays
			// either way, so a late hello is still checked against it.
			if msg.handedOff {
				m.logf("plugin %s exited cleanly before connecting; keeping its manifest declarations for %s for a plugin it started", msg.id, m.handoffGrace)
				if m.handoffPending == nil {
					m.handoffPending = make(map[string]struct{})
				}
				m.handoffPending[msg.id] = struct{}{}
				id := msg.id
				expiry = tea.Tick(m.handoffGrace, func(time.Time) tea.Msg { return handoffExpiredMsg{id: id} })
			} else {
				m.deliverPublications(m.registry.removePlugin(msg.id))
			}
		}
		for _, line := range msg.stderrTail {
			m.logf("plugin %s stderr: %s", msg.id, line)
		}
		text := msg.id + ": " + msg.status + when
		if len(msg.stderrTail) > 0 {
			text += ": " + msg.stderrTail[len(msg.stderrTail)-1]
		}
		if msg.handedOff {
			// A launcher returning is the normal start-up path, not a fault:
			// info level, and the log line above keeps the detail.
			return tea.Batch(expiry, m.showLocalToast("info", "Plugin launcher exited", text))
		}
		return m.showLocalToast("warn", "Plugin exited", text)
	case handoffExpiredMsg:
		if _, waiting := m.handoffPending[msg.id]; !waiting {
			return nil // the real plugin connected in time
		}
		delete(m.handoffPending, msg.id)
		if m.plugins[msg.id] != nil {
			return nil
		}
		m.logf("plugin %s did not connect within %s of its launcher exiting; dropping its manifest declarations", msg.id, m.handoffGrace)
		m.deliverPublications(m.registry.removePlugin(msg.id))
		return m.showLocalToast("warn", "Plugin never connected", msg.id+": launcher exited but no plugin connected within "+m.handoffGrace.String())
	case pluginLaunchFailedMsg:
		m.logf("failed to launch plugin %s: %v", msg.id, msg.err)
		return m.showLocalToast("error", "Plugin failed to launch", fmt.Sprintf("%s: %v", msg.id, msg.err))
	case hostNoticeMsg:
		m.logf("%s", msg.message)
		return m.showLocalToast("warn", msg.title, msg.message)
	}
	return nil
}

// cleanup stops the host: it closes the listener, stops every plugin process
// and removes the socket, its directory and empty stderr logs. It is safe to
// call more than once.
func (m *WorkbenchModel) cleanup() {
	if m.shuttingDown() {
		return
	}
	close(m.quit)
	if m.listener != nil {
		_ = m.listener.Close()
	}
	m.stopPlugins()
	m.removeRuntimeFiles()
}

// stopPlugins cancels the shared plugin context, which makes os/exec send
// SIGTERM to every running plugin and kill any still alive stopGrace later.
// It returns once every process has been waited for, or pluginKillWait after
// the kill deadline for processes that cannot be reaped.
func (m *WorkbenchModel) stopPlugins() {
	m.pluginCancel()
	grace := time.NewTimer(m.stopGrace)
	defer grace.Stop()
	for _, p := range m.procs {
		select {
		case <-p.done:
			continue
		case <-grace.C:
		}
		break
	}
	for _, p := range m.procs {
		if !closed(p.done) {
			m.logf("plugin %s did not exit within %s of SIGTERM; killing it", p.id, m.stopGrace)
		}
	}
	kill := time.NewTimer(pluginKillWait)
	defer kill.Stop()
	for _, p := range m.procs {
		select {
		case <-p.done:
		case <-kill.C:
			return
		}
	}
}

func closed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (m *WorkbenchModel) removeRuntimeFiles() {
	for _, p := range m.procs {
		st, err := os.Stat(p.logPath)
		if err != nil {
			continue
		}
		if st.Size() == 0 {
			_ = os.Remove(p.logPath)
		} else {
			m.logf("plugin %s stderr kept at %s", p.id, p.logPath)
		}
	}
	if m.socketPath == "" {
		return
	}
	_ = os.Remove(m.socketPath)
	if filepath.Base(m.socketPath) == socketName {
		dir := filepath.Dir(m.socketPath)
		// Both removals fail, leaving the directory in place, while a
		// non-empty stderr log remains.
		_ = os.Remove(filepath.Join(dir, pluginLogDir))
		_ = os.Remove(dir)
	}
}
