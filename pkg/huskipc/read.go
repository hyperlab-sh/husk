package huskipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxLineBytes is the largest message line (excluding the newline) a reader
// accepts. Longer lines are skipped up to the next newline and reported.
const MaxLineBytes = 4 * 1024 * 1024

var (
	// ErrLineTooLong reports a line longer than MaxLineBytes.
	ErrLineTooLong = fmt.Errorf("huskipc: line exceeds %d bytes", MaxLineBytes)
	// ErrMalformed reports a line that is not a valid envelope (invalid JSON,
	// or a field of the wrong JSON type).
	ErrMalformed = errors.New("huskipc: malformed message")
)

// LineError describes one input line the reader rejected. The loop keeps
// reading after it: a bad line never ends the stream.
type LineError struct {
	// MessageType is the line's "type" when it could still be read (a
	// well-formed envelope with a mistyped field), otherwise "".
	MessageType string
	// Err wraps ErrLineTooLong or ErrMalformed.
	Err error
}

func (e *LineError) Error() string { return e.Err.Error() }
func (e *LineError) Unwrap() error { return e.Err }

// Payload renders the error as the protocol_error sent back to the peer.
func (e *LineError) Payload() *ProtocolErrorPayload {
	return &ProtocolErrorPayload{
		MessageType: e.MessageType,
		Message:     strings.TrimPrefix(e.Err.Error(), "huskipc: "),
	}
}

// ReadLoop reads newline-delimited envelopes from r and calls onMessage for
// each. Blank lines are skipped; malformed and oversized lines are skipped
// silently (use ReadLoopReporting to learn about them). It returns nil at EOF
// and the read error otherwise.
func ReadLoop(r io.Reader, onMessage func(Envelope)) error {
	return ReadLoopReporting(r, onMessage, nil)
}

// ReadLoopReporting is ReadLoop with a callback for every rejected line:
// invalid JSON, a field of the wrong type, or a line over MaxLineBytes. An
// oversized line is discarded up to its newline, so the stream stays in sync
// and later messages are still delivered. onError may be nil.
func ReadLoopReporting(r io.Reader, onMessage func(Envelope), onError func(*LineError)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var line []byte
	oversized := false
	for {
		frag, err := br.ReadSlice('\n')
		if len(frag) > 0 && !oversized {
			// Room for the content plus "\r\n".
			if len(line)+len(frag) > MaxLineBytes+2 {
				oversized = true
				line = line[:0]
			} else {
				line = append(line, frag...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err == nil || (errors.Is(err, io.EOF) && (len(line) > 0 || oversized)) {
			content := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'})
			switch {
			case oversized || len(content) > MaxLineBytes:
				report(onError, &LineError{Err: ErrLineTooLong})
			case len(content) > 0:
				deliver(content, onMessage, onError)
			}
			line = line[:0]
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

func deliver(content []byte, onMessage func(Envelope), onError func(*LineError)) {
	var env Envelope
	if err := json.Unmarshal(content, &env); err != nil {
		// A mistyped field still leaves the type readable; name it so the
		// peer knows which message was rejected.
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(content, &head)
		report(onError, &LineError{MessageType: head.Type, Err: fmt.Errorf("%w: %v", ErrMalformed, err)})
		return
	}
	onMessage(env)
}

func report(onError func(*LineError), e *LineError) {
	if onError != nil {
		onError(e)
	}
}
