package main

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// hostNoticeMsg carries a host-side problem found off the event loop (accept
// loop, peer checks) to Update, which logs it and shows a warning toast.
// Background goroutines never write the log directly: while the TUI runs the
// log is a buffer owned by the event loop.
type hostNoticeMsg struct {
	title   string
	message string
}

// post delivers msg to the event loop unless the host is shutting down. It
// never blocks after cleanup has started, so plugin goroutines cannot hang on
// a channel nobody reads any more.
func (m *WorkbenchModel) post(msg tea.Msg) bool {
	select {
	case <-m.quit:
		return false
	default:
	}
	select {
	case m.subChan <- msg:
		return true
	case <-m.quit:
		return false
	}
}

func (m *WorkbenchModel) shuttingDown() bool {
	select {
	case <-m.quit:
		return true
	default:
		return false
	}
}

func (m *WorkbenchModel) startAcceptLoop() {
	go func() {
		for {
			conn, err := m.listener.Accept()
			if err != nil {
				if !m.shuttingDown() {
					m.post(hostNoticeMsg{
						title:   "IPC listener stopped",
						message: fmt.Sprintf("accept failed: %v; socket plugins can no longer connect", err),
					})
				}
				return
			}
			if err := authorizePeer(conn); err != nil {
				_ = conn.Close()
				m.post(hostNoticeMsg{title: "IPC connection rejected", message: fmt.Sprintf("rejected IPC connection: %v", err)})
				continue
			}
			pid := 0
			if uc, ok := conn.(*net.UnixConn); ok {
				pid, _ = peerPID(uc) // unknown pid: the connection is matched by claimed id only
			}
			go m.serveStreamOrigin(streamOrigin{peerPID: pid}, conn, conn, conn, nil)
		}
	}()
}

// handleStream serves one plugin connection. Outbound messages go through a
// queued writer so Update never blocks on a slow or hung plugin; whichever of
// the reader or the writer fails first reports the disconnect, exactly once,
// with its reason.
func (m *WorkbenchModel) handleStream(r io.Reader, w io.Writer, closer io.Closer) {
	m.serveStream(r, w, closer, nil)
}

// serveStream is handleStream with an optional onHello callback, run when the
// stream delivers its hello (the stdio spawn path uses it to tell an exit
// before hello from a later one).
//
// Until a hello arrives every other message is answered with a protocol_error
// naming its type and otherwise ignored, so a plugin that talks before saying
// hello learns why nothing happens.
func (m *WorkbenchModel) serveStream(r io.Reader, w io.Writer, closer io.Closer, onHello func()) {
	m.serveStreamFor("", r, w, closer, onHello)
}

// serveStreamFor is serveStream for a stream known to belong to the launched
// manifest plugin expectID (empty when the stream is anonymous). A hello on
// such a stream must carry that plugin_id.
func (m *WorkbenchModel) serveStreamFor(expectID string, r io.Reader, w io.Writer, closer io.Closer, onHello func()) {
	m.serveStreamOrigin(streamOrigin{expectID: expectID}, r, w, closer, onHello)
}

// streamOrigin says what the host knows about who is on the other end of a
// stream: the manifest id for a stdio pipe, the peer pid for a socket.
type streamOrigin struct {
	expectID string
	peerPID  int
}

func (m *WorkbenchModel) serveStreamOrigin(origin streamOrigin, r io.Reader, w io.Writer, closer io.Closer, onHello func()) {
	var (
		registered atomic.Pointer[ConnectedPlugin]
		once       sync.Once
	)
	disconnect := func(reason error) {
		once.Do(func() {
			if cp := registered.Load(); cp != nil {
				m.post(pluginDisconnectedMsg{plugin: cp, reason: reason})
			} else {
				m.post(connectionClosedMsg{reason: reason})
			}
		})
	}
	writer := huskipc.NewQueuedConnWriter(w, huskipc.QueueOptions{
		Capacity:     m.sendQueueCapacity,
		WriteTimeout: m.writeTimeout,
		Closer:       closer,
		OnFail:       disconnect,
	})

	// A malformed or oversized line is answered with a protocol_error and
	// skipped; the connection stays open. Send is safe from this goroutine.
	onLineError := func(lerr *huskipc.LineError) {
		_ = writer.Send(huskipc.Envelope{Type: "protocol_error", ProtocolError: lerr.Payload()})
		m.post(lineRejectedMsg{plugin: registered.Load(), err: lerr})
	}
	readErr := huskipc.ReadLoopReporting(r, func(env huskipc.Envelope) {
		cp := registered.Load()
		if cp == nil {
			if env.Type == "hello" && env.Hello != nil {
				cp = &ConnectedPlugin{
					Info:       *env.Hello,
					Writer:     writer,
					Closer:     closer,
					Connected:  time.Now(),
					ExpectedID: origin.expectID,
					PeerPID:    origin.peerPID,
				}
				registered.Store(cp)
				if onHello != nil {
					onHello()
				}
				m.post(pluginRegisteredMsg{plugin: cp})
				return
			}
			msg := "hello required before " + env.Type
			if env.Type == "hello" {
				msg = "hello payload required"
			}
			_ = writer.Send(huskipc.Envelope{Type: "protocol_error", ProtocolError: &huskipc.ProtocolErrorPayload{MessageType: env.Type, Message: msg}})
			return
		}
		if env.Type != "hello" {
			m.post(ipcEventMsg{pluginID: cp.Info.PluginID, env: env, connection: cp})
		}
	}, onLineError)
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
