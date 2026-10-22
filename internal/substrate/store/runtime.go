package store

import "github.com/hyperlab-sh/husk/internal/instance"

// Open resolves private XDG directories, preloads saved values, and starts the
// persistence worker. It resolves runtime paths through the instance leaf;
// Store neither binds sockets nor creates registration entries.
func Open(opts Options) (*Store, error) {
	resolve := opts.Env.RuntimeDir
	if resolve == nil {
		resolve = instance.RuntimeDir
	}
	return openStore(opts, resolve)
}

// SocketPath delegates the instance socket naming rule to the instance package.
func (s *Store) SocketPath() string { return instance.SocketPath(s.paths.Runtime, s.opts.Instance.ID) }
