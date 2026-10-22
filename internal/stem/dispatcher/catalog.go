package dispatcher

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

// DocSource returns the declared "plugins" documents keyed by source (plugin
// id). It has the shape of Store.Declared("plugins"); the composition root
// adapts Store to it, so this package never imports Store. The source may be
// called from any goroutine the catalog is read on.
type DocSource func() map[string]map[string]any

// DocCatalog is a Catalog over declared plugin documents plus the host's own
// action list. A plugin document contributes only actions whose id is
// "<source>.<name>" with a single-segment name; anything else is ignored, so a
// document cannot claim another owner's id. A source named "husk" is ignored.
type DocCatalog struct {
	docs DocSource
	host func() []ActionDef
}

// NewDocCatalog returns a catalog over docs and host. Either may be nil. Host
// entries are owned by "husk" whatever their Owner field says.
func NewDocCatalog(docs DocSource, host func() []ActionDef) *DocCatalog {
	return &DocCatalog{docs: docs, host: host}
}

// Lookup implements Catalog.
func (c *DocCatalog) Lookup(action string) (ActionDef, bool) {
	for _, h := range c.hostDefs() {
		if h.ID == action {
			return h, true
		}
	}
	at := strings.LastIndexByte(action, '.')
	if at <= 0 || at == len(action)-1 || c.docs == nil {
		return ActionDef{}, false
	}
	owner := action[:at]
	doc, ok := c.docs()[owner]
	if !ok {
		return ActionDef{}, false
	}
	for _, d := range docActions(owner, doc) {
		if d.ID == action {
			return d, true
		}
	}
	return ActionDef{}, false
}

// List implements Catalog. Entries are sorted by id.
func (c *DocCatalog) List() []ActionDef {
	out := c.hostDefs()
	if c.docs != nil {
		for owner, doc := range c.docs() {
			out = append(out, docActions(owner, doc)...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *DocCatalog) hostDefs() []ActionDef {
	if c.host == nil {
		return nil
	}
	defs := c.host()
	out := make([]ActionDef, 0, len(defs))
	for _, d := range defs {
		if d.ID == "" {
			continue
		}
		d.Owner = HostOwner
		out = append(out, d)
	}
	return out
}

func docActions(owner string, doc map[string]any) []ActionDef {
	if owner == HostOwner || owner == "" {
		return nil
	}
	var items []map[string]any
	switch v := doc["actions"].(type) {
	case []any:
		for _, it := range v {
			if m, ok := it.(map[string]any); ok {
				items = append(items, m)
			}
		}
	case []map[string]any:
		items = v
	}
	var out []ActionDef
	for _, m := range items {
		id, _ := m["id"].(string)
		at := strings.LastIndexByte(id, '.')
		if at <= 0 || at == len(id)-1 || id[:at] != owner {
			continue
		}
		public, _ := m["public"].(bool)
		out = append(out, ActionDef{ID: id, Owner: owner, Public: public})
	}
	return out
}

// StateLiveness reads plugin liveness from the husk.pods slice of s. It uses
// State's atomic Snapshot, so it is safe on any goroutine.
func StateLiveness(s *state.State) Liveness { return stateView{s} }

// StateFocus reads the focus context from the husk.focus and husk.surfaces
// slices of s: the focused surface and its occupant.
func StateFocus(s *state.State) FocusSource { return stateView{s} }

type stateView struct{ s *state.State }

func (v stateView) Live(pluginID string) bool {
	e, ok := v.s.Snapshot().Slices[state.PodsNamespace]
	if !ok {
		return false
	}
	var val struct {
		Plugins map[string]struct {
			Status string `json:"status"`
		} `json:"plugins"`
	}
	if json.Unmarshal(e.Value, &val) != nil {
		return false
	}
	return val.Plugins[pluginID].Status == "live"
}

func (v stateView) Focus() FocusContext {
	snap := v.s.Snapshot()
	var f struct {
		Surface string `json:"surface"`
	}
	if e, ok := snap.Slices[state.FocusNamespace]; ok {
		_ = json.Unmarshal(e.Value, &f)
	}
	if f.Surface == "" {
		return FocusContext{}
	}
	var sf struct {
		Occupants map[string]string `json:"occupants"`
	}
	if e, ok := snap.Slices[state.SurfacesNamespace]; ok {
		_ = json.Unmarshal(e.Value, &sf)
	}
	return FocusContext{Surface: f.Surface, Owner: sf.Occupants[f.Surface]}
}
