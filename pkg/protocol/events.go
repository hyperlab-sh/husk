package protocol

import "strings"

// CLIOnlyEventPattern reports whether an event name or pattern could match a
// secrets. or services. event. secrets.read and services.reloaded are
// delivered to cli watchers only, so a plugin may neither declare a
// subscription to such a pattern nor watch it.
func CLIOnlyEventPattern(pattern string) bool {
	return strings.HasPrefix(pattern, "secrets.") || strings.HasPrefix(pattern, "services.")
}

// EventDeliverParams is the params object of an event.deliver notification:
// the event name, the host-stamped source (a plugin id, "husk" for core
// publishers, "user" for a CLI emit) and the payload object (never null).
// Numbers in Data decode as json.Number so integers above 2^53 and precise
// decimals survive verbatim; decode with Decoder.UseNumber.
type EventDeliverParams struct {
	Name   string         `json:"name"`
	Source string         `json:"source"`
	Data   map[string]any `json:"data"`
}
