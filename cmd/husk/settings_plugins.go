package main

import (
	"encoding/json"
	"fmt"
	"image"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

type settingsPluginRow struct {
	ID, Status, Dir, LastError string
	Claims                     []string
}

type settingsPluginWidgetKey struct {
	width, height int
	palette       protocol.Palette
	ansiRevision  uint64
}
type settingsPluginRenderKey struct {
	widget              settingsPluginWidgetKey
	tab, query, surface string
	focus, cursor, top  int
}

type settingsPlugins struct {
	inputs             map[string]uint64
	snapshot           *state.Snapshot
	snapshotSeq        uint64
	catalogRead        bool
	catalogRevision    uint64
	servicesRevision   uint64
	indices            map[string]int
	ansiRevision       uint64
	removeX0, removeX1 int
	surfaceBefore      string
	rows               []settingsPluginRow
	held               map[string]string
	catalog            contracts.CatalogResult
	configDir          string
	filtered           []contracts.CatalogEntry
	query, surface     string
	cursor, top        int
	selectedID         string
	removing           map[string]bool
	problem            string
	confirm            *huh.Confirm
	confirmID          string
	confirmed          bool
	confirmView        string
	filter             *huh.Select[string]
	filterView         string
	lines              []string
	hits               []string
	renderKey          settingsPluginRenderKey
	widgetKey          settingsPluginWidgetKey
	lastTab            string
	positions          map[string][2]int
	dirty              bool
}

func (m *WorkbenchModel) refreshSettingsPlugins() {
	s := m.settings
	s.discardHiddenPluginDialogs()
	if s.tab != "installed" && s.tab != "store" {
		return
	}
	if s.plugins == nil {
		s.plugins = &settingsPlugins{removing: map[string]bool{}, dirty: true}
	}
	p := s.plugins
	if p.lastTab != s.tab {
		if p.positions == nil {
			p.positions = map[string][2]int{}
		}
		p.positions[p.lastTab] = [2]int{p.cursor, p.top}
		pos := p.positions[s.tab]
		p.cursor, p.top = pos[0], pos[1]
		p.lastTab = s.tab
		p.dirty = true
	}
	if !maps.Equal(p.indices, m.ansi) {
		p.indices = maps.Clone(m.ansi)
		p.ansiRevision++
	}
	widgetKey := settingsPluginWidgetKey{m.width, m.height, protocol.Palette(m.served), p.ansiRevision}
	if widgetKey != p.widgetKey {
		p.widgetKey = widgetKey
		if p.confirm != nil {
			yes := p.confirmed
			m.confirmSettingsRemovalFor(p.confirmID)
			p.confirmed = yes
			p.confirmView = p.confirm.View()
		}
		if p.filter != nil {
			before := p.surfaceBefore
			m.openSettingsSurfaceFilter()
			p.surfaceBefore = before
		}
	}
	inputs := p.inputs
	var entries map[string]state.Entry
	var snapshot *state.Snapshot
	if m.fe != nil {
		snapshot = m.fe.Snapshot()
	}
	var seq uint64
	if snapshot != nil {
		seq = snapshot.Seq
	}
	// Immutable snapshots need no namespace scan or revision-map allocation
	// until their publication changes. The sequence also supports loop fakes.
	if snapshot != p.snapshot || seq != p.snapshotSeq {
		p.snapshot, p.snapshotSeq = snapshot, seq
		inputs = map[string]uint64{}
		if snapshot != nil {
			entries = snapshot.Slices
			for ns, entry := range entries {
				if ns == "husk.pods" || ns == "husk.surfaces" || strings.HasSuffix(ns, ".occupants") {
					inputs[ns] = entry.Revision
				}
			}
		}
	}
	if !maps.Equal(inputs, p.inputs) {
		p.inputs = inputs
		var pods struct {
			Plugins map[string]struct {
				Status, Dir string
				LastError   string `json:"last_error"`
			}
		}
		_ = json.Unmarshal(entries["husk.pods"].Value, &pods)
		var res struct {
			Occupants map[string]struct{ Plugin string }
		}
		_ = json.Unmarshal(entries["husk.surfaces"].Value, &res)
		p.held = map[string]string{}
		for surface, occ := range res.Occupants {
			p.held[surface] = occ.Plugin
		}
		selected := ""
		if s.tab == "installed" && p.cursor < len(p.rows) {
			selected = p.rows[p.cursor].ID
		}
		p.rows = nil
		for id, pod := range pods.Plugins {
			row := settingsPluginRow{ID: id, Status: pod.Status, Dir: pod.Dir, LastError: pod.LastError}
			var claims map[string]json.RawMessage
			_ = json.Unmarshal(entries[id+".occupants"].Value, &claims)
			for surface := range claims {
				row.Claims = append(row.Claims, surface)
			}
			slices.Sort(row.Claims)
			p.rows = append(p.rows, row)
		}
		slices.SortFunc(p.rows, func(a, b settingsPluginRow) int { return strings.Compare(a.ID, b.ID) })
		if p.confirm != nil && !slices.ContainsFunc(p.rows, func(row settingsPluginRow) bool { return row.ID == p.confirmID }) {
			p.confirm = nil
		}
		for id := range p.removing {
			if !slices.ContainsFunc(p.rows, func(r settingsPluginRow) bool { return r.ID == id }) {
				delete(p.removing, id)
			}
		}
		if s.tab == "installed" {
			for i, row := range p.rows {
				if row.ID == selected {
					p.cursor = i
					break
				}
			}
			p.cursor = max(0, min(p.cursor, len(p.rows)-1))
		}
		p.dirty = true
	}
	if s.tab == "store" && m.fe != nil {
		// Publications may arrive after the reload event. Tokens avoid copying
		// the detached catalog until a reload or cache publication changes it.
		revision, servicesRevision := m.fe.CatalogRevision(), m.fe.ServicesRevision()
		if !p.catalogRead || revision != p.catalogRevision || servicesRevision != p.servicesRevision {
			p.catalog, p.configDir = m.fe.Catalog(), m.fe.ConfigDir()
			p.catalogRead, p.catalogRevision, p.servicesRevision = true, revision, servicesRevision
			p.dirty = true
		}
	}
	key := settingsPluginRenderKey{widgetKey, s.tab, p.query, p.surface, s.focus, p.cursor, p.top}
	if p.dirty || key != p.renderKey {
		p.dirty = false
		m.cacheSettingsPlugins()
		// Rendering can clamp the cursor and scroll position.
		p.renderKey = settingsPluginRenderKey{widgetKey, s.tab, p.query, p.surface, s.focus, p.cursor, p.top}
	}
}

func (m *WorkbenchModel) settingsPluginKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.settings.plugins
	if p == nil {
		return nil
	}
	key := legacyKeyString(msg)
	p.problem = ""
	p.dirty = true
	if p.confirm != nil {
		switch key {
		case "esc":
			p.confirm = nil
		case "enter":
			p.confirm.Update(msg)
			id, yes := p.confirmID, p.confirmed
			p.confirm = nil
			if yes {
				m.removeSettingsPlugin(id)
			}
		case "left", "right", "up", "down", "h", "l", "y", "n":
			p.confirm.Update(msg)
			p.confirmView = p.confirm.View()
		}
		return nil
	}
	if p.filter != nil {
		switch key {
		case "esc":
			p.surface = p.surfaceBefore
			p.filter = nil
		case "enter":
			p.filter.Update(msg)
			p.filter = nil
			p.cursor, p.top = 0, 0
		case "up", "down", "ctrl+k", "ctrl+j", "home", "end":
			p.filter.Update(msg)
			p.filter.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			p.filterView = p.filter.View()
			p.cursor, p.top = 0, 0
		}
		return nil
	}
	if m.settings.tab == "installed" {
		switch key {
		case "enter", "x":
			m.confirmSettingsRemoval()
		case "up", "ctrl+k":
			p.cursor--
		case "down", "ctrl+j":
			p.cursor++
		case "home":
			p.cursor = 0
		case "end":
			p.cursor = len(p.rows) - 1
		case "pgup":
			p.cursor -= max(1, m.settingsContentRect().Dy()/2)
		case "pgdown":
			p.cursor += max(1, m.settingsContentRect().Dy()/2)
		}
		p.cursor = max(0, min(p.cursor, len(p.rows)-1))
	} else {
		switch key {
		case "enter":
			m.openSettingsSurfaceFilter()
		case "up", "ctrl+k":
			p.cursor--
			p.top = 0
		case "down", "ctrl+j":
			p.cursor++
			p.top = 0
		case "home":
			p.cursor = 0
			p.top = 0
		case "end":
			p.cursor = len(p.filtered) - 1
			p.top = 0
		case "pgup":
			p.cursor -= max(1, m.settingsContentRect().Dy()/2)
			p.top = 0
		case "pgdown":
			p.cursor += max(1, m.settingsContentRect().Dy()/2)
			p.top = 0
		case "backspace":
			if len(p.query) > 0 {
				_, n := utf8.DecodeLastRuneInString(p.query)
				p.query = p.query[:len(p.query)-n]
			}
			p.cursor, p.top = 0, 0
		default:
			if paletteTextFor(msg, key) {
				p.query += key
				p.cursor, p.top = 0, 0
			}
		}
		p.cursor = max(0, min(p.cursor, len(p.filtered)-1))
		if key == "backspace" || paletteTextFor(msg, key) && key != "enter" {
			p.selectedID = ""
		} else if len(p.filtered) > 0 {
			p.selectedID = p.filtered[p.cursor].ID
		}
	}
	return nil
}

func (m *WorkbenchModel) confirmSettingsRemoval() {
	p := m.settings.plugins
	if p.cursor >= len(p.rows) || m.fe == nil {
		return
	}
	m.confirmSettingsRemovalFor(p.rows[p.cursor].ID)
}

// Rebuilds retain the original target, independently of the table cursor.
func (m *WorkbenchModel) confirmSettingsRemovalFor(id string) {
	p := m.settings.plugins
	p.confirmID, p.confirmed = id, false
	p.confirm = huh.NewConfirm().Title("Remove " + plainText(id) + " from this instance?").Description("It is unloaded, not uninstalled: its files, settings and saved state stay, and the next start loads it again.").Value(&p.confirmed).Affirmative(" yes ").Negative(" no ")
	keys := huh.NewDefaultKeyMap()
	keys.Confirm.Toggle.SetKeys("left", "right", "h", "l", "up", "down")
	p.confirm.WithKeyMap(keys)
	p.confirm.WithTheme(settingsHuhTheme(m.palette))
	p.confirm.WithWidth(m.settingsContentRect().Dx())
	p.confirm.Focus()
	p.confirmView = p.confirm.View()
	p.dirty = true
}
func (m *WorkbenchModel) removeSettingsPlugin(id string) {
	s := m.settings
	p := s.plugins
	p.removing[id] = true
	p.dirty = true
	m.fe.RemovePlugin(id, func(_ contracts.PluginRemoval, err error) {
		if m.settings != s {
			return
		}
		delete(p.removing, id)
		if err != nil {
			p.problem = settingsError(err)
		}
		p.dirty = true
	})
}
func (m *WorkbenchModel) openSettingsSurfaceFilter() {
	p := m.settings.plugins
	p.surfaceBefore = p.surface
	options := []huh.Option[string]{huh.NewOption("any surface", "")}
	for _, meta := range allSurfaces {
		options = append(options, huh.NewOption(string(meta.ID), string(meta.ID)))
	}
	for _, id := range []protocol.SurfaceID{protocol.SurfaceModal, protocol.SurfaceFullCanvas, protocol.SurfaceWorkspaceStage} {
		options = append(options, huh.NewOption(string(id), string(id)))
	}
	p.filter = huh.NewSelect[string]().Title("Surface").Options(options...).Value(&p.surface).Height(min(14, m.settingsContentRect().Dy()))
	p.filter.WithKeyMap(huh.NewDefaultKeyMap())
	p.filter.WithTheme(settingsHuhTheme(m.palette))
	p.filter.WithWidth(m.settingsContentRect().Dx())
	p.filter.Focus()
	p.filterView = p.filter.View()
	p.dirty = true
}

// Query matches are field-local, including each tag as an individual field.
func settingsCatalogMatches(e contracts.CatalogEntry, query string) bool {
	query = strings.ToLower(query)
	for _, field := range []string{e.ID, e.Name, e.Description} {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	for _, tag := range e.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	return false
}

func settingsPluginCommand(e contracts.CatalogEntry) []string {
	if filepath.IsAbs(e.Source) || strings.HasPrefix(e.Source, "~/") {
		return []string{"husk plugin add " + e.Source}
	}
	return []string{e.Source, "fetch it, then: husk plugin add <directory>"}
}

func (m *WorkbenchModel) cacheSettingsPlugins() {
	s := m.settings
	p := s.plugins
	base := lipgloss.NewStyle().Foreground(m.palette.Ink).Background(m.palette.SurfaceElevated)
	muted := base.Foreground(m.palette.InkMuted)
	width := m.settingsContentRect().Dx()
	p.lines, p.hits = nil, nil
	add := func(line, id string) { p.lines = append(p.lines, line); p.hits = append(p.hits, id) }
	if s.tab == "installed" {
		p.cursor = max(0, min(p.cursor, len(p.rows)-1))
		cells := [][]string{{"ID", "STATUS", "CLAIMED", "SOURCE", "REMOVE"}}
		for _, row := range p.rows {
			dir := row.Dir
			if dir == "" {
				dir = "(command line)"
			}
			claims := strings.Join(row.Claims, ", ")
			if claims == "" {
				claims = "none"
			}
			status := row.Status
			if p.removing[row.ID] {
				status = "removing…"
			}
			cells = append(cells, []string{row.ID, status, claims, dir, "Remove"})
		}
		widths := settingsKeyWidths(cells, width)
		p.removeX0 = 2 * 4
		for _, w := range widths[:4] {
			p.removeX0 += w
		}
		p.removeX1 = p.removeX0 + widths[4]
		for i, values := range cells {
			st := base
			if i == 0 {
				st = st.Bold(true)
			} else if i-1 == p.cursor {
				st = st.Background(m.palette.SurfaceSelected)
			}
			var parts []string
			for col, value := range values {
				style := st
				if i > 0 && col == 1 {
					switch p.rows[i-1].Status {
					case "exited":
						style = st.Foreground(m.palette.InkMuted)
					}
				}
				if i > 0 && col == 2 && len(p.rows[i-1].Claims) > 0 {
					var claims []string
					for _, surface := range p.rows[i-1].Claims {
						cs := st
						if p.held[surface] != p.rows[i-1].ID {
							cs = st.Foreground(m.palette.InkMuted)
						}
						claims = append(claims, cs.Render(plainText(surface)))
					}
					text := ansi.Truncate(strings.Join(claims, st.Render(", ")), widths[col], "…")
					parts = append(parts, text+st.Render(strings.Repeat(" ", max(0, widths[col]-ansi.StringWidth(text)))))
				} else {
					text := ansi.Truncate(plainText(value), widths[col], "…")
					padding := strings.Repeat(" ", max(0, widths[col]-ansi.StringWidth(text)))
					if i > 0 && col == 1 && !p.removing[p.rows[i-1].ID] && (p.rows[i-1].Status == "live" || p.rows[i-1].Status == "failed") {
						level := "success"
						if p.rows[i-1].Status == "failed" {
							level = "danger"
						}
						parts = append(parts, st.Render(m.statusMark(level, text)+padding))
					} else {
						parts = append(parts, style.Render(text+padding))
					}
				}
			}
			id := ""
			if i > 0 {
				id = p.rows[i-1].ID
			}
			add(strings.Join(parts, st.Render("  ")), id)
			if i > 0 && p.rows[i-1].Status == "failed" && p.rows[i-1].LastError != "" {
				add(m.statusMark("danger", "✕")+" "+base.Render(plainText(p.rows[i-1].LastError)), "")
			}
		}
		if len(p.rows) == 0 {
			add(muted.Render("No plugins loaded"), "")
		}
		if p.problem != "" {
			add(m.statusMark("danger", "✕")+" "+base.Render(plainText(p.problem)), "")
		}
		add("", "")
		box := base.Border(lipgloss.RoundedBorder()).BorderForeground(m.palette.Border).Padding(0, 1).Render("husk plugin add <path>\nhusk plugin add --dir <dir>")
		for _, line := range strings.Split(box, "\n") {
			add(line, "")
		}
		at := 0
		for i, id := range p.hits {
			if p.cursor < len(p.rows) && id == p.rows[p.cursor].ID {
				at = i
				break
			}
		}
		p.top = settingsListTop(p.top, at, len(p.lines), m.settingsContentRect().Dy())
	} else {
		query := strings.ToLower(p.query)
		p.filtered = nil
		for _, e := range p.catalog.Entries {
			if p.surface != "" && !slices.Contains(e.Surfaces, protocol.SurfaceID(p.surface)) {
				continue
			}
			if !settingsCatalogMatches(e, query) {
				continue
			}
			p.filtered = append(p.filtered, e)
		}
		slices.SortFunc(p.filtered, func(a, b contracts.CatalogEntry) int {
			if c := strings.Compare(a.Name, b.Name); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
		if p.selectedID != "" {
			for i, e := range p.filtered {
				if e.ID == p.selectedID {
					p.cursor = i
					break
				}
			}
		}
		p.cursor = max(0, min(p.cursor, len(p.filtered)-1))
		p.selectedID = ""
		if len(p.filtered) > 0 {
			p.selectedID = p.filtered[p.cursor].ID
		}
		surface := p.surface
		if surface == "" {
			surface = "any surface"
		}
		add(base.Render("Surface: "+surface+" ▾  (enter)"), "")
		add(base.Render("❯ "+plainText(p.query)), "")
		if len(p.catalog.Entries) == 0 {
			for _, line := range strings.Split(ansi.Wrap("No catalog. This Husk ships none; the distribution or you supply one.", width, " "), "\n") {
				add(base.Render(line), "")
			}
			add(base.Render(plainText(filepath.Join(p.configDir, "catalog.json"))), "")
			for _, line := range strings.Split(settingsCatalogExample, "\n") {
				add(muted.Render(line), "")
			}
		} else if len(p.filtered) == 0 {
			add(muted.Render("no entry matches"), "")
		} else {
			height := max(1, min(6, m.settingsContentRect().Dy()/3))
			top := settingsListTop(0, p.cursor, len(p.filtered), height)
			for i := top; i < min(len(p.filtered), top+height); i++ {
				e := p.filtered[i]
				st := base
				if i == p.cursor {
					st = st.Background(m.palette.SurfaceSelected)
				}
				var surfaces []string
				for _, surface := range e.Surfaces {
					surfaces = append(surfaces, string(surface))
				}
				text := st.Render(plainText(e.Name)) + " " + st.Foreground(m.palette.InkMuted).Render(plainText(e.ID+"  "+strings.Join(surfaces, ", ")+"  "+strings.Join(e.Tags, " ")))
				if slices.ContainsFunc(p.rows, func(r settingsPluginRow) bool { return r.ID == e.ID }) {
					text += " " + m.statusMark("success", "installed")
				}
				add(text, e.ID)
			}
			e := p.filtered[p.cursor]
			add("", "")
			for _, line := range strings.Split(ansi.Wrap(plainText(e.Description), width, " "), "\n") {
				add(base.Render(line), "")
			}
			var claims []string
			for _, surface := range e.Surfaces {
				text := muted.Render(string(surface))
				if p.held[string(surface)] != "" && p.held[string(surface)] != e.ID {
					text += " " + m.statusMark("warning", "taken")
				}
				claims = append(claims, text)
			}
			add(strings.Join(claims, ", "), "")
			add(muted.Render(plainText("tags: "+strings.Join(e.Tags, ", "))), "")
			add(muted.Render(plainText("source ("+e.SourceLayer+"): "+e.Source)), "")
			if slices.ContainsFunc(p.rows, func(r settingsPluginRow) bool { return r.ID == e.ID }) {
				add(base.Render("already loaded"), "")
			} else {
				for _, line := range settingsPluginCommand(e) {
					add(base.Render(plainText(line)), "")
				}
			}
		}
		for _, problem := range p.catalog.Problems[:min(10, len(p.catalog.Problems))] {
			add(m.statusMark("warning", "⚠")+" "+base.Render(plainText("warning: "+problem.Code+": "+problem.Message)), "")
		}
		if n := len(p.catalog.Problems) - 10; n > 0 {
			add(muted.Render(fmt.Sprintf("and %d more", n)), "")
		}
		p.top = max(0, min(p.top, len(p.lines)-m.settingsContentRect().Dy()))
	}
}

func (m *WorkbenchModel) settingsPluginsMouse(point image.Point) {
	p := m.settings.plugins
	r := m.settingsContentRect()
	at := p.top + point.Y - r.Min.Y
	if m.settings.tab == "store" && at == 0 {
		m.openSettingsSurfaceFilter()
		return
	}
	if at < 0 || at >= len(p.hits) || p.hits[at] == "" {
		return
	}
	id := p.hits[at]
	if m.settings.tab == "installed" {
		for i, row := range p.rows {
			if row.ID == id {
				p.cursor = i
				break
			}
		}
		if x := point.X - r.Min.X; x >= p.removeX0 && x < p.removeX1 {
			m.confirmSettingsRemoval()
		}
	} else {
		for i, e := range p.filtered {
			if e.ID == id {
				p.cursor = i
				p.selectedID = id
				p.top = 0
				break
			}
		}
	}
	p.dirty = true
}

// settingsPluginConfirmLayout is shared by drawing and button hit-testing.
// It is pure: View must not publish mouse geometry or mutate the dialog.
func (m *WorkbenchModel) settingsPluginConfirmLayout() ([]string, image.Rectangle) {
	p := m.settings.plugins
	content := m.settingsContentRect()
	lines := strings.Split(p.confirmView, "\n")
	keep := min(len(p.lines)-min(p.top, len(p.lines)), max(1, content.Dy()-len(lines)-1))
	y := content.Min.Y + keep + 1
	return lines, image.Rect(content.Min.X, y, content.Max.X, min(content.Max.Y, y+len(lines)))
}

// settingsPluginDialogMouse reports whether the dialog consumed the click.
// An outside dismissal lets the same click reach its visible target.
func (m *WorkbenchModel) settingsPluginDialogMouse(point image.Point) bool {
	p := m.settings.plugins
	if p.confirm == nil {
		r := m.settingsContentRect()
		r.Max.Y = min(r.Max.Y, r.Min.Y+len(strings.Split(p.filterView, "\n")))
		m.settings.dismissPluginDialogs()
		return point.In(r)
	}
	lines, r := m.settingsPluginConfirmLayout()
	if !point.In(r) {
		m.settings.dismissPluginDialogs()
		return false
	}
	// huh renders the buttons on its final line. Description words such as
	// "not" must never count as a click on the negative button.
	if point.Y-r.Min.Y != len(lines)-1 {
		return true
	}
	line := ansi.Strip(lines[len(lines)-1])
	for _, button := range []struct {
		text string
		yes  bool
	}{{"yes", true}, {"no", false}} {
		if at := strings.Index(line, button.text); at >= 0 {
			x := r.Min.X + ansi.StringWidth(line[:at])
			if point.X >= x && point.X < x+len(button.text) {
				p.confirmed = button.yes
				m.settingsPluginKey(tea.KeyPressMsg{Code: tea.KeyEnter})
				return true
			}
		}
	}
	return true
}

const settingsCatalogExample = `{"version": 1, "entries": {
  "editor": {"source": "~/plugins/editor",
    "surfaces": ["main-stage"]},
  "clock": {"source": "https://example.invalid/clock",
    "surfaces": ["sidebar-footer"]},
  "tree": {"source": "~/plugins/tree",
    "surfaces": ["sidebar-list"]}
}}`
