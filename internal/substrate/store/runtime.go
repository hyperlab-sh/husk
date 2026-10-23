package store

import "github.com/hyperlab-sh/husk/internal/instance"

// SocketPath delegates the instance socket naming rule to the instance
// package; "" when the opener passed no runtime directory.
func (s *Store) SocketPath() string {
	if s.paths.Runtime == "" {
		return ""
	}
	return instance.SocketPath(s.paths.Runtime, s.opts.Instance.ID)
}
