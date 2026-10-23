package protocol

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// LegacyRejectionMessage is the text of the failure recorded for a plugin that
// speaks protocol major 1, and of the line it is answered with.
func LegacyRejectionMessage() string {
	return "unsupported protocol major 1: host speaks " + Version
}

// LegacyRejectionLine is the one major-1 shaped line Husk answers a v1 `hello`
// with, newline included, on a plugin's stdin or on its socket connection. It
// is not a v2 message: a v1 plugin can log it, nothing else reads it.
func LegacyRejectionLine() []byte {
	b, _ := json.Marshal(struct {
		Type          string `json:"type"`
		ProtocolError struct {
			MessageType string `json:"message_type"`
			Message     string `json:"message"`
		} `json:"protocol_error"`
	}{Type: "protocol_error", ProtocolError: struct {
		MessageType string `json:"message_type"`
		Message     string `json:"message"`
	}{"hello", LegacyRejectionMessage()}})
	return append(b, '\n')
}

// IsLegacyHello reports whether line, without its newline, is a major-1
// `hello` envelope: valid UTF-8 within MaxLineBytes (a trailing CR does not
// count), a JSON object with no `id` and no `method` whose `type` is "hello".
// pluginID is the envelope's hello.plugin_id, empty when absent or mistyped;
// a v1 hello carries no token, and a token does not change the answer.
func IsLegacyHello(line []byte) (pluginID string, ok bool) {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) > MaxLineBytes || !utf8.Valid(line) {
		return "", false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(line, &fields) != nil || fields["id"] != nil || fields["method"] != nil {
		return "", false
	}
	var kind string
	if json.Unmarshal(fields["type"], &kind) != nil || kind != "hello" {
		return "", false
	}
	var hello struct {
		PluginID string `json:"plugin_id"`
	}
	_ = json.Unmarshal(fields["hello"], &hello)
	return hello.PluginID, true
}
