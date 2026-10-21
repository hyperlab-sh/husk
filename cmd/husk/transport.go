package main

import (
	"io"
	"os"
	"os/exec"
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
			go m.handleStream(conn, conn, conn)
		}
	}()
}

func (m *WorkbenchModel) handleStream(r io.Reader, w io.Writer, closer io.Closer) {
	writer := huskipc.NewConnWriter(w)
	var cp *ConnectedPlugin
	defer func() {
		if closer != nil {
			_ = closer.Close()
		}
		if cp != nil {
			m.subChan <- pluginDisconnectedMsg{plugin: cp}
		}
	}()

	_ = huskipc.ReadLoop(r, func(env huskipc.Envelope) {
		if env.Type == "hello" && env.Hello != nil && cp == nil {
			cp = &ConnectedPlugin{
				Info:      *env.Hello,
				Writer:    writer,
				Closer:    closer,
				Connected: time.Now(),
			}
			m.subChan <- pluginRegisteredMsg{plugin: cp}
			return
		}
		if cp != nil && env.Type != "hello" {
			m.subChan <- ipcEventMsg{pluginID: cp.Info.PluginID, env: env, connection: cp}
		}
	})
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
	_ = os.Remove(m.socketPath)
}
