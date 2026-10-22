// Package services hosts the Server handler adapters for the user-facing
// method families the Services unit owns: settings, secrets, profile, theme,
// keys and config. Each adapter is a contracts.Handler that admits the caller
// against contracts.MethodHomes, decodes the protocol.Message params, calls
// its port and encodes the reply; ports never see wire envelopes. The port
// implementations themselves — the wrappers over internal/services — live in
// internal/host/ports/services, so this package depends only on contracts.
//
// The settings.get and secrets.get adapters apply the shared contracts.Limiter
// (20 calls per second per plugin id, services.md) after admission and
// decoding and before the port is called; a refusal replies busy with
// data.reason rate_limited. CLI callers are never limited.
package services
