// Package handlers is the inbound routing layer between the pods Server and
// the host's port adapters. The Server attributes each inbound message to a
// CLI or plugin caller and hands it to a Mux, which dispatches by method
// prefix to one contracts.Handler per port family. Adapters authorize caller
// kinds against contracts.MethodHomes before decoding params, decode protocol
// payloads, call their port and encode the reply, so the port implementations
// never see wire envelopes and the wire never depends on port internals.
package handlers
