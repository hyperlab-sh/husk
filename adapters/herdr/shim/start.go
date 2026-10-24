package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func correlationID(id *protocol.ID) string {
	var s string
	if json.Unmarshal([]byte(id.String()), &s) == nil {
		return s
	}
	return id.String()
}

func startStateDirectory(env map[string]string) (string, error) {
	stateHome := env["XDG_STATE_HOME"]
	if stateHome == "" {
		stateHome = filepath.Join(env["HOME"], ".local", "state")
	}
	if !filepath.IsAbs(stateHome) {
		return "", fmt.Errorf("absolute state directory is required to start server")
	}
	return filepath.Join(stateHome, "husk", "herdr"), nil
}

func startServer(ctx context.Context, o Options) error {
	dir, err := startStateDirectory(envMap(o.Env))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "start.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := o.Clock.AfterFunc(10*time.Second, cancel)
	defer timer.Stop()
	wait := func() bool {
		ready := make(chan struct{})
		t := o.Clock.AfterFunc(100*time.Millisecond, func() { close(ready) })
		defer t.Stop()
		select {
		case <-deadline.Done():
			return false
		case <-ready:
			return true
		}
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		if !wait() {
			return deadline.Err()
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if _, err = o.Client.Ping(deadline); err == nil {
		return nil
	}
	args := []string{"server"}
	if o.Settings.Session != "" {
		args = []string{"--session", o.Settings.Session, "server"}
	}
	// A detached server is the user's session from now on; never signal it on
	// shim shutdown or give it a plugin credential/private invocation context.
	serverEnv := []string{}
	for _, v := range o.Env {
		k, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(k, "HERDR_PLUGIN_") {
			continue
		}
		if strings.HasPrefix(k, "HUSK_") && k != "HUSK_SOCKET_PATH" && k != "HUSK_INSTANCE" && k != "HUSK_BIN_PATH" {
			continue
		}
		if k == "HERDR_SOCKET_PATH" {
			continue
		}
		serverEnv = append(serverEnv, v)
	}
	serverEnv = append(serverEnv, "HERDR_SOCKET_PATH="+o.Socket)
	launch := o.ServerLaunch
	if launch == nil {
		launch = launchServer
	}
	if err = launch(o.Binary, args, serverEnv); err != nil {
		return err
	}
	for {
		if _, err = o.Client.Ping(deadline); err == nil {
			return nil
		}
		if !wait() {
			return fmt.Errorf("ping after server start: %w", deadline.Err())
		}
	}
}

func launchServer(binary string, args, env []string) error {
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
