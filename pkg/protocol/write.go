package protocol

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
)

// Writer serializes complete JSON lines. NewWriter is synchronous;
// NewQueuedWriter marshals on the caller and queues without waiting for I/O.
type Writer struct {
	w  io.Writer
	mu sync.Mutex
	q  *sendQueue
}

// NewWriter creates a synchronous, concurrency-safe writer.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Send validates and writes or queues m. It never silently drops a message.
// Queued Send never waits for the peer, but encoding still runs on the caller.
func (w *Writer) Send(m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // retain raw id spelling, including '<', '>' and '&'
	if err := enc.Encode(m); err != nil {
		return err
	}
	if b.Len()-1 > MaxLineBytes {
		return ErrLineTooLong
	}
	if w.q != nil {
		return w.q.enqueue(b.Bytes())
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeLine(w.w, b.Bytes())
}

// SendLine writes or queues one pre-encoded line. line must hold a single JSON
// line of at most MaxLineBytes; a missing final newline is added. Used for the
// major-1 rejection, which is not a v2 Message. It exists for the host;
// plugins send Messages.
func (w *Writer) SendLine(line []byte) error {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	if len(line) > MaxLineBytes {
		return ErrLineTooLong
	}
	b := append(append(make([]byte, 0, len(line)+1), line...), '\n')
	if w.q != nil {
		return w.q.enqueue(b)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeLine(w.w, b)
}

func writeLine(w io.Writer, b []byte) error {
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}
