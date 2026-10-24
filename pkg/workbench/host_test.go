package workbench

import (
	"fmt"
	"os"

	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// newHost wires the real host into tests that drive the private model directly.
func newHost(prog *programRef, m *workbenchModel, id, dir string, docs map[string]map[string]any, profile *string, opts ...host.Option) (host.Looped, error) {
	h, err := host.New(contracts.Config{
		InstanceID: id, RuntimeDir: dir,
		Entry:        instance.Entry{ID: id, PID: os.Getpid(), Socket: instance.SocketPath(dir, id), ProtocolVersion: protocol.Version},
		InstanceDocs: docs, ProfileFlag: profile, Post: prog.post, Content: m.content,
	}, append([]host.Option{host.WithLogger(func(level, msg string) { m.logf("%s: %s", level, msg) })}, opts...)...)
	if err != nil {
		return nil, err
	}
	looped, ok := h.(host.Looped)
	if !ok {
		return nil, fmt.Errorf("host does not run on a loop")
	}
	return looped, nil
}

func pluginDocs(path string) map[string]map[string]any {
	return map[string]map[string]any{"config": {"husk": map[string]any{"plugins": map[string]any{"paths": []string{path}}}}}
}
