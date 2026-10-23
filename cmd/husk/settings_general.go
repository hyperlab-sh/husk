package main

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The center owns traversal and submission, not huh. Keeping the individual
// fields live avoids huh's completion path and lets collapsed groups, status
// lines and the profile corner participate in the same tab order.
type settingsGeneral struct {
	schema   []contracts.SettingEntry
	fields   map[string]*settingsField
	groups   []settingsGroup
	expanded map[string]bool
	active   string
	top      int
	lines    []string
	hits     []string
	saving   bool
	queue    []settingsSave
	styled   protocol.Palette
	indices  map[string]int
}

type settingsGroup struct {
	name   string
	plugin bool
	keys   []string
}
type settingsField struct {
	entry    contracts.SettingEntry
	widget   huh.Field
	text     string
	boolean  bool
	baseline any
	snapshot any
	source   string
	problem  string
	revision uint64
}
type settingsSave struct {
	key      string
	value    any
	revision uint64
}

func settingsGroupFor(e contracts.SettingEntry) (string, int) {
	if id, ok := strings.CutPrefix(e.Owner, "plugin:"); ok {
		return "plugins." + id, 2
	}
	if e.Key == "dist" || strings.HasPrefix(e.Key, "dist.") {
		return e.Key[:max(0, strings.LastIndex(e.Key, "."))], 1
	}
	at := strings.LastIndex(e.Key, ".")
	if at < 0 {
		return e.Key, 0
	}
	return e.Key[:at], 0
}

func (f *settingsField) read(value any) {
	switch f.entry.Type {
	case "bool":
		f.boolean, _ = value.(bool)
	case "strings":
		var lines []string
		switch v := value.(type) {
		case []string:
			lines = v
		case []any:
			for _, item := range v {
				if text, ok := item.(string); ok {
					lines = append(lines, text)
				}
			}
		}
		f.text = strings.Join(lines, "\n")
	case "duration":
		switch v := value.(type) {
		case time.Duration:
			f.text = strconv.FormatInt(v.Milliseconds(), 10)
		default:
			f.text = fmt.Sprint(value)
		}
	default:
		f.text, _ = value.(string)
		if value != nil && f.entry.Type != "string" && f.entry.Type != "enum" {
			f.text = fmt.Sprint(value)
		}
	}
}

func (f *settingsField) value() (any, error) {
	switch f.entry.Type {
	case "bool":
		return f.boolean, nil
	case "int", "duration":
		n, err := strconv.ParseInt(f.text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("enter an integer")
		}
		if f.entry.Min != nil && n < *f.entry.Min {
			return nil, fmt.Errorf("minimum is %d", *f.entry.Min)
		}
		if f.entry.Max != nil && n > *f.entry.Max {
			return nil, fmt.Errorf("maximum is %d", *f.entry.Max)
		}
		return n, nil
	case "strings":
		lines := []string{}
		for _, line := range strings.Split(f.text, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				lines = append(lines, line)
			}
		}
		return lines, nil
	default:
		return f.text, nil
	}
}
func (f *settingsField) dirty() bool {
	value, err := f.value()
	return err != nil || !reflect.DeepEqual(value, f.baseline)
}
func (f *settingsField) accept(value any) { f.read(value); f.baseline, _ = f.value(); f.syncWidget() }
func (f *settingsField) syncWidget() {
	switch widget := f.widget.(type) {
	case *huh.Input:
		widget.Value(&f.text)
	case *huh.Text:
		widget.Value(&f.text)
	case *huh.Confirm:
		widget.Value(&f.boolean)
	case *huh.Select[string]:
		widget.Value(&f.text)
	}
}

func (m *WorkbenchModel) buildSettingsField(f *settingsField) {
	label := f.entry.Key[strings.LastIndex(f.entry.Key, ".")+1:]
	if f.entry.Type == "duration" {
		label += " (ms)"
	}
	title := label
	switch f.entry.Type {
	case "bool":
		f.widget = huh.NewConfirm().Key(f.entry.Key).Title(title).Description(plainText(f.entry.Doc)).Affirmative("yes").Negative("no").Value(&f.boolean)
	case "enum":
		options := make([]huh.Option[string], 0, len(f.entry.Enum))
		for _, option := range f.entry.Enum {
			options = append(options, huh.NewOption(plainText(option), option))
		}
		f.widget = huh.NewSelect[string]().Key(f.entry.Key).Title(title).Description(plainText(f.entry.Doc)).Options(options...).Value(&f.text).Height(min(5, len(options)+2))
	case "strings":
		f.widget = huh.NewText().Key(f.entry.Key).Title(title).Description(plainText(f.entry.Doc)).Value(&f.text).Lines(3).CharLimit(0)
	default:
		input := huh.NewInput().Key(f.entry.Key).Title(title).Description(plainText(f.entry.Doc)).Value(&f.text).CharLimit(0)
		if f.entry.Type == "int" || f.entry.Type == "duration" {
			input.Validate(func(string) error { _, err := f.value(); return err })
		}
		f.widget = input
	}
	// The center owns traversal and drops huh's submission commands, so no
	// asynchronous completion can escape after the last field. Disable the
	// external-editor binding too: a field must not suspend the workbench.
	keys := huh.NewDefaultKeyMap()
	keys.Input.Next.SetKeys()
	keys.Input.Prev.SetKeys()
	keys.Input.Submit.SetKeys()
	keys.Text.Next.SetKeys()
	keys.Text.Prev.SetKeys()
	keys.Text.Submit.SetKeys()
	keys.Text.Editor.SetKeys()
	keys.Confirm.Next.SetKeys()
	keys.Confirm.Prev.SetKeys()
	keys.Confirm.Submit.SetKeys()
	keys.Confirm.Accept.SetKeys()
	keys.Confirm.Reject.SetKeys()
	keys.Select.Next.SetKeys()
	keys.Select.Prev.SetKeys()
	// Select's Home/End paths move its cursor before its accessor. An
	// internal Enter commits that cursor below; its next-field command is
	// discarded, so it can never submit or complete the host-owned form.
	keys.Select.Submit.SetKeys("enter")
	keys.Select.Filter.SetKeys()
	f.widget.WithKeyMap(keys).WithTheme(settingsHuhTheme(m.palette)).WithWidth(m.settingsContentRect().Dx() - 2)
	f.widget.Blur()
	f.widget.Update(nil)
}

func (m *WorkbenchModel) refreshSettingsGeneral() {
	s := m.settings
	if m.fe == nil {
		return
	}
	g := s.general
	if g == nil {
		g = &settingsGeneral{fields: map[string]*settingsField{}, expanded: map[string]bool{}}
		s.general = g
	}
	schema := slices.Clone(m.fe.SettingsSchema())
	slices.SortFunc(schema, func(a, b contracts.SettingEntry) int {
		ag, ar := settingsGroupFor(a)
		bg, br := settingsGroupFor(b)
		if ar != br {
			return ar - br
		}
		if ag != bg {
			return strings.Compare(ag, bg)
		}
		return strings.Compare(a.Key, b.Key)
	})
	changed := !reflect.DeepEqual(schema, g.schema)
	if changed {
		fields := map[string]*settingsField{}
		g.groups = nil
		for _, entry := range schema {
			f := g.fields[entry.Key]
			if f == nil {
				f = &settingsField{entry: entry}
				value, source, err := m.fe.Setting(entry.Key)
				if err != nil {
					value = entry.Default
					f.problem = settingsError(err)
				}
				f.source = source
				f.accept(value)
				f.snapshot = value
			}
			f.entry = entry
			fields[entry.Key] = f
			name, rank := settingsGroupFor(entry)
			if len(g.groups) == 0 || g.groups[len(g.groups)-1].name != name {
				g.groups = append(g.groups, settingsGroup{name: name, plugin: rank == 2})
			}
			group := &g.groups[len(g.groups)-1]
			group.keys = append(group.keys, entry.Key)
		}
		g.fields, g.schema = fields, schema
	}
	retheme := g.styled != protocol.Palette(m.served) || !maps.Equal(g.indices, m.ansi)
	for _, entry := range schema {
		f := g.fields[entry.Key]
		value, source, err := m.fe.Setting(entry.Key)
		if err == nil {
			f.source = source
			if !f.dirty() && !reflect.DeepEqual(value, f.snapshot) {
				f.accept(value)
			}
			f.snapshot = value
		}
		if changed || retheme || f.widget == nil {
			m.buildSettingsField(f)
		}
	}
	g.styled, g.indices = protocol.Palette(m.served), maps.Clone(m.ansi)
	targets := g.targets()
	if !slices.Contains(targets, g.active) {
		g.active = ""
		if len(targets) > 0 {
			g.active = targets[0]
		}
	}
	m.cacheSettingsGeneral()
}

// Plugin headings are traversal targets too: enter/space expands or collapses
// them, and mouse clicks use the same targets as keyboard traversal.
func (g *settingsGeneral) targets() []string {
	var keys []string
	for _, group := range g.groups {
		if group.plugin {
			keys = append(keys, "@"+group.name)
			if !g.expanded[group.name] {
				continue
			}
		}
		keys = append(keys, group.keys...)
	}
	return keys
}
func (m *WorkbenchModel) settingsGeneralTab(back bool) bool {
	g := m.settings.general
	if g == nil {
		return false
	}
	targets := g.targets()
	at := slices.Index(targets, g.active)
	next := at + 1
	if back {
		next = at - 1
	}
	if next < 0 || next >= len(targets) {
		return false
	}
	g.active = targets[next]
	m.cacheSettingsGeneral()
	return true
}

func (m *WorkbenchModel) settingsGeneralKey(msg tea.Msg) tea.Cmd {
	s := m.settings
	g := s.general
	if g == nil {
		return nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		spelling := legacyKeyString(key)
		if spelling == "ctrl+s" {
			m.saveSettingsGeneral()
			return nil
		}
		if strings.HasPrefix(g.active, "@") {
			name := strings.TrimPrefix(g.active, "@")
			if spelling == "enter" || spelling == " " {
				g.expanded[name] = !g.expanded[name]
			}
			m.cacheSettingsGeneral()
			return nil
		}
		// Enter on a single-line field advances without ever submitting a form.
		if spelling == "enter" {
			if f := g.fields[g.active]; f != nil && f.entry.Type != "strings" {
				if !m.settingsGeneralTab(false) {
					s.focus = settingsProfileFocus
				}
				return nil
			}
		}
	}
	f := g.fields[g.active]
	if f == nil {
		return nil
	}
	before, _ := f.value()
	beforeText := f.text
	_, cmd := f.widget.Update(msg)
	if f.entry.Type == "enum" {
		f.widget.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	after, _ := f.value()
	if beforeText != f.text || !reflect.DeepEqual(before, after) {
		f.revision++
		f.problem = ""
	}
	if _, err := f.value(); err != nil {
		f.problem = "invalid_value: " + err.Error()
	}
	m.cacheSettingsGeneral()
	return cmd
}

func (m *WorkbenchModel) saveSettingsGeneral() {
	g := m.settings.general
	if g == nil || g.saving || m.fe == nil {
		return
	}
	var keys []string
	for key, f := range g.fields {
		if f.dirty() {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		f := g.fields[key]
		value, err := f.value()
		if err != nil {
			f.problem = "invalid_value: " + err.Error()
			continue
		}
		g.queue = append(g.queue, settingsSave{key, value, f.revision})
	}
	g.saving = len(g.queue) > 0
	m.nextSettingsSave()
}
func (m *WorkbenchModel) nextSettingsSave() {
	s := m.settings
	g := s.general
	if len(g.queue) == 0 {
		g.saving = false
		for _, f := range g.fields {
			m.buildSettingsField(f)
		}
		m.cacheSettingsGeneral()
		return
	}
	write := g.queue[0]
	g.queue = g.queue[1:]
	m.fe.SetSetting(write.key, write.value, func(result contracts.SettingSetResult, err error) {
		if m.settings != s {
			return
		}
		f := g.fields[write.key]
		if f != nil {
			problem := ""
			if err != nil {
				problem = settingsError(err)
			} else {
				for _, report := range result.Reports {
					if report.Service != "settings" {
						continue
					}
					for _, p := range report.Problems {
						if p.Key != write.key {
							continue
						}
						if p.Code == "restart_required" {
							if m.settingsRestarts == nil {
								m.settingsRestarts = map[string]bool{}
							}
							m.settingsRestarts[write.key] = true
						}
						if p.Severity == "error" && problem == "" {
							problem = p.Code + ": " + p.Message
						}
					}
				}
			}
			f.problem = problem
			if problem == "" {
				if f.revision == write.revision {
					f.accept(result.Value)
				} else {
					accepted := &settingsField{entry: f.entry}
					accepted.accept(result.Value)
					f.baseline = accepted.baseline
				}
			}
		}
		m.nextSettingsSave()
	})
}

// Cache huh rendering only on Update. View consumes strings and hit targets,
// keeping cursor models, dynamic titles and viewport bookkeeping out of View.
func (m *WorkbenchModel) cacheSettingsGeneral() {
	s := m.settings
	g := s.general
	if g == nil {
		return
	}
	g.lines, g.hits = nil, nil
	base := lipgloss.NewStyle().Foreground(m.palette.Ink).Background(m.palette.SurfaceElevated)
	add := func(text, target string) {
		for _, line := range strings.Split(text, "\n") {
			g.lines = append(g.lines, line)
			g.hits = append(g.hits, target)
		}
	}
	activeStart, activeEnd := 0, 0
	for _, group := range g.groups {
		target := ""
		title := plainText(group.name)
		if group.plugin {
			target = "@" + group.name
			mark := "▸ "
			if g.expanded[group.name] {
				mark = "▾ "
			}
			title = mark + title
		}
		style := base.Bold(true)
		if s.tab == "general" && s.focus == settingsContentFocus && g.active == target {
			style = style.Foreground(m.palette.Accent)
		}
		if g.active == target {
			activeStart = len(g.lines)
		}
		add(style.Render(title), target)
		if g.active == target {
			activeEnd = len(g.lines)
		}
		if group.plugin && !g.expanded[group.name] {
			add("", "")
			continue
		}
		for _, key := range group.keys {
			f := g.fields[key]
			if f.widget == nil {
				continue
			}
			focused := s.tab == "general" && s.focus == settingsContentFocus && g.active == key && s.profileForm == nil
			if focused {
				f.widget.Focus()
			} else {
				f.widget.Blur()
			}
			f.widget.WithWidth(m.settingsContentRect().Dx() - 2)
			title := f.entry.Key[strings.LastIndex(f.entry.Key, ".")+1:]
			if f.entry.Type == "duration" {
				title += " (ms)"
			}
			if f.dirty() {
				title += " *"
			}
			if f.source != "" && f.source != "default" && f.source != "user" {
				title += " (from " + plainText(f.source) + ")"
			}
			switch widget := f.widget.(type) {
			case *huh.Input:
				widget.Title(title)
			case *huh.Text:
				widget.Title(title)
			case *huh.Confirm:
				widget.Title(title)
			case *huh.Select[string]:
				widget.Title(title)
			}
			if g.active == key {
				activeStart = len(g.lines)
			}
			view := f.widget.View()
			if !focused {
				// Pinned huh updates the focused Bubbles Input styles, but not
				// its blurred styles. Repaint only the inactive editor body;
				// preserve huh's title/description roles and the active cursor.
				body := 0
				switch f.widget.(type) {
				case *huh.Input:
					body = 1
				case *huh.Text:
					body = 3
				}
				lines := strings.Split(view, "\n")
				for i := max(0, len(lines)-body); i < len(lines); i++ {
					lines[i] = base.Render(ansi.Strip(lines[i]))
				}
				view = strings.Join(lines, "\n")
			}
			add(view, key)
			if m.settingsRestarts[key] {
				add(m.statusMark("warning", "restart required"), key)
			}
			if f.problem != "" {
				add(m.statusMark("danger", "✕")+" "+base.Render(plainText(f.problem)), key)
			}
			if g.active == key {
				activeEnd = len(g.lines)
			}
			add("", key)
		}
	}
	height := m.settingsContentRect().Dy()
	if activeStart < g.top {
		g.top = activeStart
	}
	if activeEnd > g.top+height {
		g.top = max(activeStart, activeEnd-height)
	}
	g.top = max(0, min(g.top, len(g.lines)-height))
}
