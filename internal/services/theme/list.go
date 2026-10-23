package theme

import (
	"sort"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
)

// List returns a detached serving-snapshot view without Store I/O. The built-in
// theme comes first, followed by named themes sorted by their exact names.
func (s *Service) List(caller c.Caller) ([]host.ThemeEntry, error) {
	if err := c.CheckCaller(caller); err != nil {
		return nil, err
	}
	snap := s.current.Load()
	out := []host.ThemeEntry{{Name: palette.Default().Name, Source: "built-in", Current: snap.palette.Name == palette.Default().Name}}
	names := make([]string, 0, len(snap.themes))
	for name := range snap.themes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, host.ThemeEntry{Name: name, Source: snap.themes[name].layer, Current: snap.palette.Name == name})
	}
	return out, nil
}
