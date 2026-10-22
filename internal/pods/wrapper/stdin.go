package wrapper

import (
	"io"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// stdinMux is the sole physical stdin writer. Normal lines come from the
// protocol queue; its graceful-close trailer is the one legacy rejection line.
// Failure closes bypass the write lock so watchdogs always unblock a pipe write.
type stdinMux struct {
	raw       io.WriteCloser
	writeMu   sync.Mutex
	mu        sync.Mutex
	trailer   []byte
	queue     *protocol.Writer
	clock     protocol.Clock
	timeout   time.Duration
	closeOnce sync.Once
	closed    chan struct{}
}

func (m *stdinMux) Write(b []byte) (int, error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	return m.raw.Write(b)
}

func (m *stdinMux) setTrailer(line []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trailer = append([]byte(nil), line...)
}

func (m *stdinMux) Close() error {
	var err error
	m.closeOnce.Do(func() {
		defer close(m.closed)
		m.mu.Lock()
		line := m.trailer
		m.mu.Unlock()
		// ErrWriterClosed means the queue finished a requested flush. A failed
		// queue instead closes immediately, without attempting another write.
		if len(line) > 0 && m.queue != nil && m.queue.Err() == protocol.ErrWriterClosed {
			timer := m.clock.AfterFunc(m.timeout, func() { _ = m.raw.Close() })
			m.writeMu.Lock()
			n, werr := m.raw.Write(line)
			m.writeMu.Unlock()
			timer.Stop()
			if werr != nil {
				err = werr
			} else if n != len(line) {
				err = io.ErrShortWrite
			}
		}
		if closeErr := m.raw.Close(); err == nil {
			err = closeErr
		}
	})
	return err
}
