// Package server owns the instance socket, caller attribution and prefix routing.
// It imports no implementation of Stem, Services, State or Pods lifecycle.
package server

import (
	"context"
	"time"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Caller identifies the authenticated source of one socket message. The Server
// produces only the cli and plugin kinds.
type Caller = protocol.Caller

// CallerKind is the kind of a Caller.
type CallerKind = protocol.CallerKind

const (
	// CallerCLI is the user's uncredentialed client.
	CallerCLI = protocol.CallerCLI
	// CallerPlugin carries a valid per-launch token.
	CallerPlugin = protocol.CallerPlugin
)

// Handler serves a message off-loop. A request returns one reply; a notification
// discards the result except for an error diagnostic. ctx lasts until disconnect.
type Handler interface {
	Serve(context.Context, Caller, protocol.Message, Notifier) protocol.Message
}

// Notifier pushes a notification through the connection's bounded queue.
type Notifier interface{ Notify(protocol.Message) error }

// TokenResolver resolves current launch credentials; it must be worker-safe.
type TokenResolver interface{ Resolve(string) (string, bool) }

// Config supplies instance identity, the checked runtime path and loop bridge.
// Post runs on workers and must return after shutdown. Clock controls queue
// watchdogs, diagnostic rate windows and uptime; native socket probes use deadlines.
type Config struct {
	InstanceID string
	RuntimeDir string
	Entry      instance.Entry
	Tokens     TokenResolver
	Post       func(any) bool
	Clock      protocol.Clock
}

// Server is a single-use listener with pre-start prefix registration.
type Server interface {
	Handle(string, Handler)
	Start() error
	SocketPath() string
	Close() error
}

const (
	// MaxInFlight bounds active request handlers per connection; excess get busy.
	MaxInFlight = 64
	// MaxNotificationQueue bounds ordered notifications, including the active one.
	MaxNotificationQueue = 64
	// MaxConnections bounds simultaneously accepted, authenticated clients.
	MaxConnections = 256
	// QueueCapacity is the maximum number of waiting outbound messages.
	QueueCapacity = 1024
	// WriteTimeout bounds an outbound socket write.
	WriteTimeout = 5 * time.Second
	// DiagnosticInterval bounds coalesced loop notices per connection.
	DiagnosticInterval = time.Second
)

// Notice is a token-free diagnostic posted to the owning loop.
type Notice struct {
	ConnID         uint64
	Level, Message string
	// Count is the number of coalesced diagnostics; zero for lifecycle notices.
	Count uint64
	// LastCode is the final diagnostic code in this batch.
	LastCode protocol.Code
	// Suppressed counts rate-limited protocol.error wire notifications.
	Suppressed uint64
}

type systemClock struct{}

func (systemClock) Now() time.Time                                     { return time.Now() }
func (systemClock) AfterFunc(d time.Duration, f func()) protocol.Timer { return time.AfterFunc(d, f) }
