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

func writeLine(w io.Writer, b []byte) error {
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}
