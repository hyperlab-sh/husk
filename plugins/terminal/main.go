// Command terminal is a reference plugin hosting an arbitrary argv on a PTY.
// Add argv after -- in the manifest command; without it the user's SHELL runs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/ptyhost"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := launch(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "terminal:", err)
		os.Exit(1)
	}
}

func launch(ctx context.Context, argv []string) error {
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		argv = []string{shell}
	}
	path, token := os.Getenv("HUSK_SOCKET_PATH"), os.Getenv("HUSK_PLUGIN_TOKEN")
	if path == "" || token == "" {
		return errors.New("terminal requires host connection credentials")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	return run(ctx, argv, token, os.Stdin, conn)
}

type incoming struct {
	msg protocol.Message
	err error
}

func run(ctx context.Context, argv []string, token string, stdin io.Reader, socket io.ReadWriteCloser) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	in := make(chan incoming)
	read := func(r io.Reader) {
		err := protocol.ReadLoop(r, func(msg protocol.Message) {
			select {
			case in <- incoming{msg: msg}:
			case <-ctx.Done():
			}
		}, func(e *protocol.LineError) {
			select {
			case in <- incoming{err: e}:
			case <-ctx.Done():
			}
		})
		if err == nil {
			err = io.EOF
		}
		select {
		case in <- incoming{err: err}:
		case <-ctx.Done():
		}
	}
	go read(stdin)
	go read(socket)
	writer := protocol.NewWriter(socket)
	const surface = protocol.SurfaceMainStage
	request := func(id string, method string, params any) error {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		var rid *protocol.ID
		if id != "" {
			v := protocol.NewStringID(id)
			rid = &v
		}
		return writer.Send(protocol.Message{ID: rid, Method: method, Params: raw, Token: token})
	}
	var child *ptyhost.Child
	defer func() {
		if child != nil {
			_ = child.Close()
			_ = child.Wait()
		}
	}()
	var ended = make(chan error, 1)
	attaching := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-ended:
			if err != nil {
				return err
			}
			if err = request("", protocol.MethodTerminalDetach, protocol.TerminalDetachParams{Surface: surface}); err != nil {
				return err
			}
			if err = request("close", protocol.MethodSurfaceClose, protocol.SurfaceCloseParams{Surface: surface}); err != nil {
				return err
			}
		case entry := <-in:
			if entry.err != nil {
				if errors.Is(entry.err, io.EOF) {
					return nil
				}
				return entry.err
			}
			msg := entry.msg
			if msg.ID != nil && msg.Method == "" {
				if msg.Error != nil {
					return fmt.Errorf("host refused terminal operation: %s", msg.Error.Code)
				}
				switch msg.ID.String() {
				case `"attach"`:
					var size protocol.TerminalAttachResult
					if err := json.Unmarshal(msg.Result, &size); err != nil {
						return err
					}
					var err error
					child, err = ptyhost.Start(argv, ptyhost.Options{Width: size.Width, Height: size.Height})
					if err != nil {
						return err
					}
					go func(c *ptyhost.Child) {
						err := c.Pump(surface, func(p protocol.TerminalWriteParams) error { return request("", protocol.MethodTerminalWrite, p) })
						_ = c.Wait()
						ended <- err
					}(child)
				case `"close"`:
					return nil
				}
				continue
			}
			switch msg.Method {
			case protocol.MethodSurfaceResized:
				var sizes protocol.SurfaceResizedParams
				if err := json.Unmarshal(msg.Params, &sizes); err != nil {
					return err
				}
				for _, p := range sizes.Surfaces {
					if p.Surface != surface || p.Width < 1 || p.Height < 1 {
						continue
					}
					if child != nil {
						if err := child.Resize(p.Width, p.Height); err != nil {
							return err
						}
					} else if !attaching {
						attaching = true
						if err := request("attach", protocol.MethodTerminalAttach, protocol.TerminalAttachParams{Surface: surface, Capture: protocol.CaptureAll}); err != nil {
							return err
						}
					}
				}
			case protocol.MethodTerminalInput:
				var p protocol.TerminalInputParams
				if err := json.Unmarshal(msg.Params, &p); err != nil {
					return err
				}
				if p.Surface == surface && child != nil {
					if err := child.Input(p); err != nil {
						return err
					}
				}
			}
		}
	}
}
