// Command terminal hosts arbitrary argv on a plugin-owned PTY.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
type outputChunk struct {
	params protocol.TerminalWriteParams
	ack    chan error
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
	request := func(id, method string, params any) error {
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
	var surface protocol.SurfaceID
	var child *ptyhost.Child
	var inputDone <-chan struct{}
	defer func() {
		cancel()
		if child != nil {
			_ = child.Close()
			if inputDone != nil {
				<-inputDone
			}
			_ = child.Wait()
		}
	}()
	ended := make(chan error, 1)
	output := make(chan outputChunk)
	input := make(chan protocol.TerminalInputParams, 16)
	inputErr := make(chan error, 1)
	var pending chan error
	attaching := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-inputErr:
			return err
		case chunk := <-output:
			// Exactly one acknowledged output chunk is in flight. This both applies
			// PTY backpressure and preserves order through concurrent server handlers.
			pending = chunk.ack
			if err := request("write", protocol.MethodTerminalWrite, chunk.params); err != nil {
				return err
			}
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
			if msg.Method == protocol.MethodProtocolError {
				return errors.New("host rejected terminal transport message")
			}
			if msg.ID != nil && msg.Method == "" {
				if msg.Error != nil {
					return fmt.Errorf("host refused terminal operation: %s", msg.Error.Code)
				}
				switch msg.ID.String() {
				case `"write"`:
					if pending != nil {
						pending <- nil
						pending = nil
					}
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
					done := make(chan struct{})
					inputDone = done
					go func(c *ptyhost.Child) {
						defer close(done)
						for {
							select {
							case <-ctx.Done():
								return
							case p := <-input:
								if err := c.Input(p); err != nil {
									select {
									case inputErr <- err:
									case <-ctx.Done():
									}
									return
								}
							}
						}
					}(child)
					go func(c *ptyhost.Child) {
						err := c.Pump(surface, func(p protocol.TerminalWriteParams) error {
							ack := make(chan error, 1)
							select {
							case output <- outputChunk{p, ack}:
							case <-ctx.Done():
								return ctx.Err()
							}
							select {
							case err := <-ack:
								return err
							case <-ctx.Done():
								return ctx.Err()
							}
						})
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
					if !p.Surface.Valid() || p.Width < 1 || p.Height < 1 {
						continue
					}
					if surface == "" {
						surface = p.Surface
					}
					if p.Surface != surface {
						continue
					}
					if child != nil {
						if err := child.Resize(p.Width, p.Height); err != nil {
							return err
						}
					} else if !attaching {
						attaching = true
						if err := request("attach", protocol.MethodTerminalAttach, protocol.TerminalAttachParams{Surface: surface, Capture: protocol.CaptureValueAll.Ptr()}); err != nil {
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
					select {
					case input <- p:
					default:
						log.Printf("terminal: dropped input: child input queue full")
					}
				}
			}
		}
	}
}
