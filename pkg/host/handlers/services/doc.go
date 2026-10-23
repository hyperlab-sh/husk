// Package services hosts the Server handler adapters for the user-facing
// method families the Services unit owns: settings, secrets, profile, theme,
// keys and config. Each adapter is a contracts.Handler that admits the caller
// against contracts.MethodHomes, decodes the protocol.Message params, calls
// its port and encodes the reply; ports never see wire envelopes. The port
// implementations themselves — the wrappers over pkg/services — live in
// pkg/host/ports/services, so this package depends only on contracts.
//
// The adapters of every plugin-callable Services method (settings.get,
// secrets.get, secrets.names, theme.get, keys.list) apply the one shared
// contracts.Limiter (20 calls per second per plugin id, services.md) after
// admission and before params are decoded, so malformed requests count; a
// refusal replies busy with data.reason rate_limited. CLI callers are never
// limited.
package services
