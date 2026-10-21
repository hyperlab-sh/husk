package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func (m *WorkbenchModel) startAcceptLoop() {
	go func() {
		for {
			conn, err := m.listener.Accept()
			if err != nil {
				return
			}
			if err := authorizePeer(conn); err != nil {
				fmt.Fprintf(os.Stderr, "husk: rejected IPC connection: %v\n", err)
				_ = conn.Close()
				continue
			}
			go m.handleStream(conn, conn, conn)
		}
	}()
}

// handleStream serves one plugin connection. Outbound messages go through a
// queued writer so Update never blocks on a slow or hung plugin; whichever of
// the reader or the writer fails first reports the disconnect, exactly once,
// with its reason.
func (m *WorkbenchModel) handleStream(r io.Reader, w io.Writer, closer io.Closer) {
	var (
		registered atomic.Pointer[ConnectedPlugin]
		once       sync.Once
	)
	disconnect := func(reason error) {
		once.Do(func() {
			if cp := registered.Load(); cp != nil {
				m.subChan <- pluginDisconnectedMsg{plugin: cp, reason: reason}
			}
		})
	}
	writer := huskipc.NewQueuedConnWriter(w, huskipc.QueueOptions{
		Capacity:     m.sendQueueCapacity,
		WriteTimeout: m.writeTimeout,
		Closer:       closer,
		OnFail:       disconnect,
	})

	readErr := huskipc.ReadLoop(r, func(env huskipc.Envelope) {
		cp := registered.Load()
		if env.Type == "hello" && env.Hello != nil && cp == nil {
			cp = &ConnectedPlugin{
				Info:      *env.Hello,
				Writer:    writer,
				Closer:    closer,
				Connected: time.Now(),
			}
			registered.Store(cp)
			m.subChan <- pluginRegisteredMsg{plugin: cp}
			return
		}
		if cp != nil && env.Type != "hello" {
			m.subChan <- ipcEventMsg{pluginID: cp.Info.PluginID, env: env, connection: cp}
		}
	})
	// A writer failure closes the connection, which ends the read loop with
	// a "closed" error; report the writer's reason instead.
	reason := writer.Err()
	if reason == nil {
		reason = readErr
	}
	if reason == nil {
		reason = io.EOF
	}
	writer.Abort()
	if closer != nil {
		_ = closer.Close()
	}
	disconnect(reason)
}

func (m *WorkbenchModel) spawnSocketCommand(dir string, argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "HUSK_SOCKET_PATH="+m.socketPath)
	if err := cmd.Start(); err != nil {
		return err
	}
	m.childCmds = append(m.childCmds, cmd)
	go func() { _ = cmd.Wait() }()
	return nil
}

func (m *WorkbenchModel) spawnStdioCommand(dir string, argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "HUSK_SOCKET_PATH="+m.socketPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	m.childCmds = append(m.childCmds, cmd)
	go func() {
		m.handleStream(stdout, stdin, stdin)
		_ = cmd.Wait()
	}()
	return nil
}

func (m *WorkbenchModel) cleanup() {
	if m.listener != nil {
		_ = m.listener.Close()
	}
	for _, cmd := range m.childCmds {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	if m.socketPath != "" {
		_ = os.Remove(m.socketPath)
		if filepath.Base(m.socketPath) == socketName {
			_ = os.Remove(filepath.Dir(m.socketPath))
		}
	}
}
