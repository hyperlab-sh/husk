package theme

import (
	"sort"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
)

// List returns a detached serving-snapshot view without Store I/O. The built-in
// themes come first in document order, followed by other themes by exact name.
func (s *Service) List(caller c.Caller) ([]host.ThemeEntry, error) {
	if err := c.CheckCaller(caller); err != nil {
		return nil, err
	}
	snap := s.current.Load()
	names := palette.BuiltInNames()
	builtIn := make(map[string]bool, len(names))
	for _, name := range names {
		builtIn[name] = true
	}
	var extra []string
	for name := range snap.themes {
		if !builtIn[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	names = append(names, extra...)
	out := make([]host.ThemeEntry, 0, len(names))
	for _, name := range names {
		out = append(out, host.ThemeEntry{Name: name, Source: snap.themes[name].layer, Current: snap.palette.Name == name})
	}
	return out, nil
}
