package workbench

import (
	"context"
	"fmt"

	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

// attachTestHost exercises the production attachment seam. Its constructor
// supplies only fixture-specific configuration, leaving callbacks, logging,
// attachment and cancellation to attachHost.
func attachTestHost(ctx context.Context, prog *programRef, m *workbenchModel, id, dir string, docs map[string]map[string]any, opts ...host.Option) (host.Looped, error) {
	opt := Options{NewHost: func(cfg contracts.Config, uiOpts ...host.Option) (host.Looped, error) {
		cfg.InstanceID, cfg.RuntimeDir, cfg.InstanceDocs = id, dir, docs
		h, err := host.New(cfg, append(opts, uiOpts...)...)
		if err != nil {
			return nil, err
		}
		looped, ok := h.(host.Looped)
		if !ok {
			return nil, fmt.Errorf("host does not run on a loop")
		}
		return looped, nil
	}}
	return attachHost(ctx, m, prog, opt)
}

func pluginDocs(path string) map[string]map[string]any {
	return map[string]map[string]any{"config": {"husk": map[string]any{"plugins": map[string]any{"paths": []string{path}}}}}
}
