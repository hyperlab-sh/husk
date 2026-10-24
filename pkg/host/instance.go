package host

import (
	"fmt"
	"os"
	"strconv"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// NewInstance constructs a loop-driven host with process launch defaults.
// An empty InstanceID uses the process pid; an empty RuntimeDir is resolved
// without creating it, so bundle validation still precedes filesystem writes.
// Entry is initialized with the instance identity and current protocol version.
// Other Config fields, including loop callbacks and Distribution, pass through.
func NewInstance(config contracts.Config, opts ...Option) (Looped, error) {
	if config.InstanceID == "" {
		config.InstanceID = strconv.Itoa(os.Getpid())
	}
	if config.RuntimeDir == "" {
		dir, _, err := instance.LookupRuntimeDir()
		if err != nil {
			return nil, fmt.Errorf("runtime directory: %w", err)
		}
		config.RuntimeDir = dir
	}
	config.Entry = instance.Entry{
		ID: config.InstanceID, PID: os.Getpid(),
		Socket: instance.SocketPath(config.RuntimeDir, config.InstanceID), ProtocolVersion: protocol.Version,
	}
	h, err := New(config, opts...)
	if err != nil {
		return nil, err
	}
	return h.(Looped), nil
}
