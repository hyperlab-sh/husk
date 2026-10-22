package wrapper

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// firstLineReader inspects only the first nonblank physical line. Its capture
// is bounded independently of the protocol reader, including for a missing LF.
type firstLineReader struct {
	r            io.Reader
	line         []byte
	done, legacy bool
}

func (p *firstLineReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	for _, c := range b[:n] {
		if p.done {
			break
		}
		if len(p.line) >= protocol.MaxLineBytes+2 {
			p.done = true
			p.line = nil
			break
		}
		if c == '\n' {
			p.inspect()
		} else {
			p.line = append(p.line, c)
		}
	}
	if err == io.EOF && !p.done && len(p.line) > 0 {
		p.inspect()
	}
	return n, err
}
func (p *firstLineReader) inspect() {
	if len(bytes.TrimSpace(p.line)) == 0 {
		p.line = nil
		return
	}
	var fields map[string]json.RawMessage
	if len(bytes.TrimSuffix(p.line, []byte{'\r'})) <= protocol.MaxLineBytes && utf8.Valid(p.line) && json.Unmarshal(p.line, &fields) == nil && fields["id"] == nil && fields["method"] == nil {
		var kind string
		_ = json.Unmarshal(fields["type"], &kind)
		p.legacy = kind == "hello"
	}
	p.done = true
	p.line = nil
}

type rejectionBudget struct {
	initialized       bool
	start             time.Time
	count, suppressed int
}

func (w *Wrapper) reject(b *rejectionBudget, code protocol.Code, message, method string, data json.RawMessage, notify bool, logText string) {
	now := w.opts.Clock.Now()
	if !b.initialized || now.Sub(b.start) >= time.Second {
		w.flushRejections(b)
		b.initialized = true
		b.start = now
		b.count = 0
		b.suppressed = 0
	}
	if b.count >= protocol.MaxProtocolErrorsPerSecond {
		b.suppressed++
		return
	}
	b.count++
	if logText == "" {
		logText = fmt.Sprintf("husk: protocol error from %s: %s: %s", w.spec.ID, code, message)
	}
	w.log("warn", logText)
	if !notify {
		return
	}
	params, _ := json.Marshal(protocol.ProtocolErrorParams{Code: code, Message: message, Method: method, Data: data})
	w.mu.Lock()
	q := w.queue
	w.mu.Unlock()
	if q != nil {
		if err := q.Send(protocol.Message{Method: protocol.MethodProtocolError, Params: params}); err != nil {
			w.fail(err)
		}
	}
}

func (w *Wrapper) flushRejections(b *rejectionBudget) {
	if b.suppressed > 0 {
		w.log("warn", fmt.Sprintf("husk: protocol error from %s: %d rejections suppressed", w.spec.ID, b.suppressed))
		b.suppressed = 0
	}
}

func (w *Wrapper) readStdout(r io.Reader) {
	defer close(w.readDone)
	probe := &firstLineReader{r: r}
	var legacy sync.Once
	var budget rejectionBudget
	defer w.flushRejections(&budget)
	err := protocol.ReadLoop(probe, func(m protocol.Message) {
		if probe.legacy {
			legacy.Do(w.legacyHello)
			return
		}
		kind, _ := m.Kind()
		if kind == protocol.KindReply {
			if w.opts.Replies != nil {
				w.opts.Replies.Reply(w.spec.ID, m)
			}
			return
		}
		w.reject(&budget, protocol.CodeInvalidMessage, "stdout carries replies only", m.Method, nil, m.Method != protocol.MethodProtocolError, fmt.Sprintf("plugin %s sent a non-reply on stdout", w.spec.ID))
	}, func(e *protocol.LineError) {
		if probe.legacy {
			legacy.Do(w.legacyHello)
			return
		}
		if e.ID != nil && e.Method == "" {
			if w.opts.Replies != nil {
				w.opts.Replies.Reply(w.spec.ID, protocol.Message{ID: e.ID})
			}
			w.reject(&budget, e.Code, lineMessage(e.Err), "", nil, false, "")
			return
		}
		var data json.RawMessage
		if e.Code == protocol.CodeLineTooLong {
			data, _ = json.Marshal(struct {
				Limit int `json:"limit"`
			}{protocol.MaxLineBytes})
		}
		w.reject(&budget, e.Code, lineMessage(e.Err), e.Method, data, e.Method != protocol.MethodProtocolError, "")
	})
	if err != nil {
		w.fail(fmt.Errorf("read stdout: %w", err))
		return
	}
	w.awaitReap(ErrStdoutClosed)
}

func lineMessage(err error) string {
	var wire *protocol.Error
	var typed *json.UnmarshalTypeError
	if errors.As(err, &wire) {
		return wire.Message
	}
	if errors.As(err, &typed) {
		return "message contains a field with an invalid JSON type"
	}
	return err.Error()
}

func (w *Wrapper) legacyHello() {
	failure := &protocol.Error{Code: protocol.CodeVersionMismatch, Message: "unsupported protocol major 1: host speaks " + protocol.Version}
	w.recordFailure(failure)
	w.mu.Lock()
	mux, q := w.stdin, w.queue
	w.mu.Unlock()
	if mux != nil && q != nil {
		line := []byte(`{"type":"protocol_error","protocol_error":{"message_type":"hello","message":"unsupported protocol major 1: host speaks ` + protocol.Version + `"}}` + "\n")
		mux.setTrailer(line)
		_ = q.Close()
		<-mux.closed
	}
	w.stop()
}
