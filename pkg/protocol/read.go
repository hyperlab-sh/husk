package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// MaxLineBytes is the maximum content length, excluding the CRLF terminator.
const MaxLineBytes = 4 * 1024 * 1024

const readBufferBytes = 64 * 1024

// ErrLineTooLong identifies an oversized input or output line.
var ErrLineTooLong = fmt.Errorf("protocol: line exceeds %d bytes", MaxLineBytes)

// LineError describes a rejected line. ID and Method are recovered when readable.
// Receiving code decides whether to respond; replies and protocol.error must
// never be answered. The reader itself does not write, rate-limit or log.
type LineError struct {
	Code   Code
	ID     *ID
	Method string
	Err    error

	reply bool // readable reply fields, even when its id is invalid
}

// Error describes the rejected line.
func (e *LineError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying decode or framing error.
func (e *LineError) Unwrap() error { return e.Err }

// Response builds an error reply for a readable request, or a protocol.error
// notification otherwise. The boolean is false for replies and protocol.error,
// which must never be answered. Caller applies channel logging and rate limits.
func (e *LineError) Response() (Message, bool) {
	if e.reply || e.Method == MethodProtocolError {
		return Message{}, false
	}
	failure := &Error{Code: e.Code, Message: e.Err.Error()}
	var protocolErr *Error
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(e.Err, &protocolErr):
		failure.Message = protocolErr.Message
		failure.Data = protocolErr.Data
	case errors.As(e.Err, &typeErr):
		failure.Message = "message contains a field with an invalid JSON type"
	}
	if e.Code == CodeLineTooLong {
		failure.Data = json.RawMessage(fmt.Sprintf(`{"limit":%d}`, MaxLineBytes))
	}
	if e.ID != nil && e.Method != "" {
		return Message{ID: e.ID, Error: failure}, true
	}
	b, _ := json.Marshal(ProtocolErrorParams{Code: failure.Code, Message: failure.Message, Method: e.Method, Data: failure.Data})
	return Message{Method: MethodProtocolError, Params: b}, true
}

// ReadLoop reads JSON lines using a 64 KiB buffer. Bad lines are reported and
// skipped without closing the stream. EOF, including after an unterminated final
// line, returns nil; other read errors are returned. Either callback may be nil.
func ReadLoop(r io.Reader, onMessage func(Message), onError func(*LineError)) error {
	return ReadLoopFirst(r, nil, onMessage, onError)
}

// ReadLoopFirst is ReadLoop with an observer of the first nonblank line. first
// gets that line's content (no line terminator), once, before the line is
// delivered or reported, and only when it is within MaxLineBytes. When it
// returns true the line is consumed, reading stops and ReadLoopFirst returns
// nil; otherwise the line is processed as usual. A nil first is ReadLoop.
func ReadLoopFirst(r io.Reader, first func(line []byte) (stop bool), onMessage func(Message), onError func(*LineError)) error {
	br := bufio.NewReaderSize(r, readBufferBytes)
	var line []byte
	oversized := false
	for {
		frag, err := br.ReadSlice('\n')
		if len(frag) > 0 && !oversized {
			if len(line)+len(frag) > MaxLineBytes+2 {
				oversized = true
				line = nil
			} else {
				line = append(line, frag...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err == nil || errors.Is(err, io.EOF) && (len(line) > 0 || oversized) {
			content := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'})
			switch {
			case oversized || len(content) > MaxLineBytes:
				first = nil // the first line is not a hello; later lines are never observed
				report(onError, &LineError{Code: CodeLineTooLong, Err: ErrLineTooLong})
			case len(bytes.TrimSpace(content)) != 0:
				if first != nil {
					observe := first
					first = nil
					if observe(content) {
						return nil
					}
				}
				deliver(content, onMessage, onError)
			}
			line = resetLine(line)
			oversized = false
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// resetLine retains ordinary buffers but releases an unusually large line's
// backing array, so one large frame does not pin megabytes per connection.
func resetLine(line []byte) []byte {
	if cap(line) > readBufferBytes {
		return nil
	}
	return line[:0]
}

func deliver(b []byte, onMessage func(Message), onError func(*LineError)) {
	if !utf8.Valid(b) || !json.Valid(b) {
		report(onError, &LineError{Code: CodeParseError, Err: &Error{Code: CodeParseError, Message: "line is not valid UTF-8 JSON"}})
		return
	}
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		e := &LineError{Code: CodeInvalidMessage, Err: err}
		var failure *Error
		if errors.As(err, &failure) {
			e.Code = failure.Code
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(b, &fields) == nil {
			if raw := fields["id"]; raw != nil {
				var id ID
				if id.UnmarshalJSON(raw) == nil {
					e.ID = &id
				}
			}
			_ = json.Unmarshal(fields["method"], &e.Method)
			e.reply = e.Method == "" && (fields["result"] != nil || fields["error"] != nil)
		}
		report(onError, e)
		return
	}
	if onMessage != nil {
		onMessage(m)
	}
}

func report(callback func(*LineError), e *LineError) {
	if callback != nil {
		callback(e)
	}
}
