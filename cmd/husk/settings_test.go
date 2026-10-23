package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

func settingsFixture(t testing.TB, tab string) (*WorkbenchModel, *fakeFE) {
	t.Helper()
	m := goldenModel()
	fe := withFE(m)
	fe.ThemeRows = []contracts.ThemeEntry{{Name: theme.Default().Name, Source: "built-in", Current: true}, {Name: "evening", Source: "user"}, {Name: "bad", Source: "user"}}
	fe.ProfileRows = []contracts.ProfileInfo{{Name: "work", Source: "user", Active: true}, {Name: "home", Source: "user"}}
	fe.OnInvoke = func(action string, args map[string]any) error {
		if h := m.actions[action]; h != nil {
			_, err := h(args)
			return err
		}
		return nil
	}
	if tab != "" {
		if _, err := m.openSettings(map[string]any{"tab": tab}); err != nil {
			t.Fatal(err)
		}
	}
	return m, fe
}
func settingsPress(m *WorkbenchModel, key string) {
	codes := map[string]rune{"esc": tea.KeyEscape, "tab": tea.KeyTab, "enter": tea.KeyEnter, "up": tea.KeyUp, "down": tea.KeyDown, "right": tea.KeyRight, "home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown}
	msg := tea.KeyPressMsg{Code: codes[key]}
	if strings.HasPrefix(key, "ctrl+") {
		msg.Code = []rune(strings.TrimPrefix(key, "ctrl+"))[0]
		msg.Mod = tea.ModCtrl
	} else if key == "shift+tab" {
		msg.Code = tea.KeyTab
		msg.Mod = tea.ModShift
	} else if msg.Code == 0 {
		msg.Code = []rune(key)[0]
		msg.Text = key
	}
	m.Update(msg)
}

func TestSettingsOpeningMemoryAndArguments(t *testing.T) {
	m, _ := settingsFixture(t, "")
	m.openSettings(nil)
	if m.settingsTab() != "general" {
		t.Fatal("first open is not General")
	}
	settingsPress(m, "4")
	if m.settingsTab() != "store" {
		t.Fatal("digit did not select Store")
	}
	m.closeSettings()
	m.openSettings(nil)
	if m.settingsTab() != "store" {
		t.Fatal("last tab lost")
	}
	m.openSettings(map[string]any{"tab": "keys"})
	if m.settingsTab() != "keys" {
		t.Fatal("argument did not switch existing center")
	}
	m.openSettings(nil)
	if m.settingsTab() != "keys" {
		t.Fatal("argument-less open toggled center")
	}
	m.closeSettings()
	for _, value := range []any{"plugins", "", "unknown", 42, nil} {
		_, err := m.openSettings(map[string]any{"tab": value})
		if contracts.CodeOf(err) != "invalid_params" || m.settings != nil || !strings.Contains(err.Error(), "tab") || !strings.Contains(err.Error(), fmt.Sprint(value)) {
			t.Fatalf("value %v: %v", value, err)
		}
	}
}

func TestSettingsDeclinesWithoutChangingOverlays(t *testing.T) {
	for _, state := range []string{"frozen", "modal", "palette", "secret"} {
		t.Run(state, func(t *testing.T) {
			m, _ := settingsFixture(t, "")
			switch state {
			case "frozen":
				m.width = 83
			case "modal":
				m.openModal("p", "p.dialog")
			case "palette":
				m.showPalette = true
			case "secret":
				m.secretPrompt = &secretPromptState{}
			}
			for _, action := range []string{"husk.settings.open", "husk.theme.select"} {
				_, err := m.actions[action](nil)
				if !errors.Is(err, contracts.ErrDeclined) || m.settings != nil {
					t.Fatalf("%s: %v", action, err)
				}
			}
		})
	}
}

func TestSettingsThemeEntryPointsAndPillPrecedence(t *testing.T) {
	for _, entry := range []string{"alias", "action", "pill", "palette"} {
		t.Run(entry, func(t *testing.T) {
			m, _ := settingsFixture(t, "")
			switch entry {
			case "alias":
				m.actions["husk.theme.select"](nil)
			case "action":
				m.actions["husk.settings.open"](map[string]any{"tab": "theme"})
			case "pill":
				m.openSettings(nil)
				r := themePillRect(m.width, m.height, m.palette.Name)
				m.Update(tea.MouseClickMsg{X: r.Min.X, Y: r.Min.Y, Button: tea.MouseLeft})
			case "palette":
				m.openPalette()
				m.paletteQuery = "Select theme"
				settingsPress(m, "enter")
			}
			if m.settingsTab() != "theme" || m.showPalette {
				t.Fatal("entry point did not open Theme")
			}
		})
	}
}

func TestSettingsOverlayFollowsTabsAndEveryClosePath(t *testing.T) {
	for _, close := range []string{"esc", "ctrl+c", "outside", "frozen", "success"} {
		t.Run(close, func(t *testing.T) {
			m, fe := settingsFixture(t, "general")
			m.attachSlices()
			m.settle()
			settingsPress(m, "3")
			if got := fe.Views[len(fe.Views)-1].Overlays.SettingsCenter; got != "theme" {
				t.Fatalf("overlay %q", got)
			}
			switch close {
			case "esc", "ctrl+c":
				settingsPress(m, close)
			case "outside":
				m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
			case "frozen":
				m.Update(tea.WindowSizeMsg{Width: 83, Height: 24})
			case "success":
				settingsPress(m, "enter")
				settingsPress(m, "enter")
				fe.replySetting(0, contracts.SettingSetResult{Reports: []contracts.ReloadReport{{Service: "theme", Applied: true}}}, nil)
				m.settle()
			}
			if m.settings != nil || fe.Views[len(fe.Views)-1].Overlays.SettingsCenter != "" {
				t.Fatal("close left overlay")
			}
		})
	}
}

type settingsKeyRecorder struct {
	defaultKeys
	calls int
}

func (r *settingsKeyRecorder) Resolve(ctx contracts.KeyContext, keys []string) contracts.KeyResolution {
	r.calls++
	return r.defaultKeys.Resolve(ctx, keys)
}

func TestSettingsFocusCycleAndInputIsolation(t *testing.T) {
	m, fe := settingsFixture(t, "general")
	keys := &settingsKeyRecorder{}
	m.keys = keys
	for _, want := range []int{settingsContentFocus, settingsProfileFocus, settingsTabsFocus} {
		settingsPress(m, "tab")
		if m.settings.focus != want {
			t.Fatalf("focus %d, want %d", m.settings.focus, want)
		}
	}
	settingsPress(m, "shift+tab")
	if m.settings.focus != settingsProfileFocus {
		t.Fatal("reverse focus")
	}
	settingsPress(m, "shift+tab")
	settingsPress(m, "3")
	if m.settingsTab() != "general" {
		t.Fatal("digit stole content input")
	}
	for _, key := range []string{"q", "h", "p", "ctrl+p", "ctrl+s", "x"} {
		settingsPress(m, key)
	}
	m.Update(tea.PasteMsg{Content: "q"})
	if keys.calls != 0 || len(fe.Keys) != 0 || len(fe.Invoked) != 0 || len(fe.SettingWrites) != 0 || len(fe.PluginRemovals) != 0 {
		t.Fatal("modal leaked input")
	}
	m.settings.focus = settingsProfileFocus
	settingsPress(m, "enter")
	settingsPress(m, "tab")
	if m.settings.profileForm != nil || m.settings.focus != settingsTabsFocus {
		t.Fatal("Select trapped the focus cycle")
	}
	m.settings.focus = settingsProfileFocus
	settingsPress(m, "enter")
	settingsPress(m, "ctrl+c")
	if m.settings != nil || m.closed {
		t.Fatal("ctrl+c did not close the whole center without quitting")
	}
}

func TestSettingsThemeListCurrentCursorMovementAndReload(t *testing.T) {
	m, fe := settingsFixture(t, "theme")
	if !reflect.DeepEqual(m.settings.themes, fe.ThemeRows) || m.settings.cursor != 0 {
		t.Fatal("list order/current cursor")
	}
	settingsPress(m, "down")
	if m.settings.cursor != 1 || len(fe.SettingWrites) != 0 {
		t.Fatal("movement wrote or did not move")
	}
	fe.ThemeRows = []contracts.ThemeEntry{{Name: "bad", Source: "user"}, {Name: "evening", Source: "user"}, {Name: theme.Default().Name, Current: true}}
	m.settle()
	if m.settings.cursor != 1 {
		t.Fatal("reload lost cursor name")
	}
	fe.ThemeRows = fe.ThemeRows[2:]
	m.settle()
	if m.settings.cursor != 0 {
		t.Fatal("removed cursor did not fall back to current")
	}
	for _, key := range []string{"end", "home", "pgdown", "pgup", "ctrl+j", "ctrl+k"} {
		settingsPress(m, key)
	}
	if len(fe.SettingWrites) != 0 {
		t.Fatal("navigation wrote")
	}
}

func TestSettingsThemeSelectionReports(t *testing.T) {
	warning := protocol.Problem{Severity: "warning", Code: "unknown_theme", Message: "theme missing"}
	low := protocol.Problem{Severity: "error", Code: "low_contrast", Message: "contrast refused"}
	invalid := protocol.Problem{Severity: "error", Code: "invalid_value", Message: "bad setting"}
	for _, tc := range []struct {
		name                  string
		applied               bool
		problems, settings    []protocol.Problem
		source, current, want string
		err                   error
		closed                bool
	}{
		{name: "applied", applied: true, current: "evening", closed: true},
		{name: "contrast", problems: []protocol.Problem{warning, low}, want: "low_contrast: contrast refused"},
		{name: "unknown", applied: true, problems: []protocol.Problem{warning}, want: "unknown_theme: theme missing"},
		{name: "settings unchanged", problems: []protocol.Problem{{Severity: "warning", Code: "settings_unchanged"}}, settings: []protocol.Problem{invalid}, want: "invalid_value: bad setting"},
		{name: "shadowed", applied: true, current: theme.Default().Name, source: "profile", want: "set in profile layer, which wins"},
		{name: "store error", err: contracts.NewError(protocol.CodeUnavailable, "disk refused", map[string]any{"reason": "store_error"}), want: "store_error: disk refused"},
		{name: "timeout", err: contracts.NewError(protocol.CodeTimeout, "expired", nil), want: "timeout: expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, fe := settingsFixture(t, "theme")
			settingsPress(m, "down")
			settingsPress(m, "enter")
			settingsPress(m, "enter")
			settingsPress(m, "down")
			if len(fe.SettingWrites) != 1 || fe.SettingWrites[0].Key != "theme.name" || fe.SettingWrites[0].Value != "evening" || !m.settings.pending || m.settings.cursor != 1 {
				t.Fatal("pending call not isolated")
			}
			for i := range fe.ThemeRows {
				fe.ThemeRows[i].Current = fe.ThemeRows[i].Name == tc.current
			}
			fe.replySetting(0, contracts.SettingSetResult{Source: tc.source, Reports: []contracts.ReloadReport{{Service: "theme", Applied: tc.applied, Problems: tc.problems}, {Service: "settings", Problems: tc.settings}}}, tc.err)
			if tc.closed {
				if m.settings != nil {
					t.Fatal("applied theme did not close")
				}
				return
			}
			if m.settings == nil || m.settings.pending || m.settings.problem != tc.want || m.settings.cursor != 1 {
				t.Fatalf("selection: %+v", m.settings)
			}
			if tc.err == nil && !tc.applied && !strings.Contains(m.settings.written, "written to the user layer") {
				t.Fatal("missing write notice")
			}
			if !strings.Contains(ansi.Strip(m.View().Content), tc.want) {
				t.Fatal("problem not drawn")
			}
		})
	}
}

func TestSettingsPendingReplyCannotAffectReopenedCenter(t *testing.T) {
	m, fe := settingsFixture(t, "theme")
	settingsPress(m, "enter")
	settingsPress(m, "esc")
	m.openSettings(map[string]any{"tab": "keys"})
	fe.replySetting(0, contracts.SettingSetResult{Reports: []contracts.ReloadReport{{Service: "theme", Applied: true}}}, nil)
	if m.settingsTab() != "keys" || m.settings.problem != "" {
		t.Fatal("late reply changed new center")
	}
}

func TestSettingsProfileCornerSelectionAndFailure(t *testing.T) {
	m, fe := settingsFixture(t, "theme")
	if m.settingsCorner() != "profile: work ▾" {
		t.Fatal(m.settingsCorner())
	}
	m.settings.focus = settingsProfileFocus
	settingsPress(m, "enter")
	if m.settings.profileForm == nil || m.settings.profileChoice != "work" || !strings.Contains(m.settings.profileView, "work") {
		t.Fatal("Select not open")
	}
	settingsPress(m, "esc")
	if m.settings == nil || m.settings.profileForm != nil || len(fe.ProfileUses) != 0 {
		t.Fatal("Select escape wrote or closed center")
	}
	settingsPress(m, " ")
	settingsPress(m, "home")
	settingsPress(m, "enter")
	if len(fe.ProfileUses) != 1 || fe.ProfileUses[0].Name != "" || !m.settings.switching || m.settingsCorner() != "switching…" {
		t.Fatal("none selection")
	}
	fe.replyProfile(0, contracts.ProfileUseResult{}, contracts.NewError(protocol.CodeNotFound, "profile vanished", map[string]any{"reason": "unknown_profile"}))
	if m.settings.active != "work" || m.settings.profileError != "unknown_profile: profile vanished" || !strings.Contains(ansi.Strip(m.View().Content), "unknown_profile: profile vanished") {
		t.Fatal("profile failure")
	}
	settingsPress(m, "enter")
	settingsPress(m, "end")
	settingsPress(m, "enter")
	if len(fe.ProfileUses) != 2 || fe.ProfileUses[1].Name != "work" {
		t.Fatal("profile name choice")
	}
	fe.replyProfile(1, contracts.ProfileUseResult{Active: ""}, nil)
	if m.settingsCorner() != "profile: default ▾" || m.settings.switching {
		t.Fatal("result not reflected")
	}
	settingsPress(m, "enter")
	settingsPress(m, "enter")
	m.closeSettings()
	m.openSettings(map[string]any{"tab": "general"})
	fe.replyProfile(2, contracts.ProfileUseResult{Active: "late"}, nil)
	if m.settings.active == "late" {
		t.Fatal("late profile reply")
	}
}

func TestSettingsMouseAndGeometry(t *testing.T) {
	for _, size := range []image.Point{{144, 40}, {84, 24}} {
		t.Run(size.String(), func(t *testing.T) {
			m, fe := settingsFixture(t, "theme")
			m.width, m.height = size.X, size.Y
			m.recomputeLayout()
			m.settle()
			r := m.settingsRect()
			if r.Dx() != min(100, size.X-4) || r.Dy() != min(32, size.Y-4) || m.layoutLabel() != "Modal" {
				t.Fatal(r)
			}
			content := m.settingsContentRect()
			m.Update(tea.MouseClickMsg{X: content.Min.X, Y: content.Min.Y + 1, Button: tea.MouseLeft})
			if m.settings.cursor != 1 || len(fe.SettingWrites) != 0 {
				t.Fatal("first click")
			}
			m.Update(tea.MouseClickMsg{X: content.Min.X, Y: content.Min.Y + 1, Button: tea.MouseLeft})
			if len(fe.SettingWrites) != 1 {
				t.Fatal("second click")
			}
			m.Update(tea.MouseClickMsg{X: r.Min.X + 3, Y: r.Min.Y + 2, Button: tea.MouseLeft})
			if m.settingsTab() != "keys" {
				t.Fatal("tab click")
			}
			corner := m.settingsCornerRect()
			m.Update(tea.MouseClickMsg{X: corner.Min.X, Y: corner.Min.Y, Button: tea.MouseLeft})
			if m.settings.profileForm == nil {
				t.Fatal("corner click")
			}
			if ansi.StringWidth(m.settingsCorner()) > 24 {
				t.Fatal("unclipped corner")
			}
			if len(fe.Mice) != 0 {
				t.Fatal("mouse leaked")
			}
		})
	}
}

func TestSettingsViewIsPureAndUsesPalette(t *testing.T) {
	m, _ := settingsFixture(t, "theme")
	m.openSettingsProfiles()
	before := *m.settings
	first := m.View().Content
	if second := m.View().Content; first != second || !reflect.DeepEqual(before, *m.settings) {
		t.Fatal("View mutated center")
	}
	m.settings.active = strings.Repeat("界", 30)
	if ansi.StringWidth(m.settingsCorner()) > 24 || !strings.HasSuffix(m.settingsCorner(), "…") {
		t.Fatal("corner clipping")
	}
	p := m.palette
	styles := settingsHuhTheme(p).Theme(false)
	if !sameColor(styles.Focused.Title.GetForeground(), p.Accent) || !sameColor(styles.Focused.ErrorMessage.GetForeground(), p.Ink) || !sameColor(styles.Form.Base.GetBackground(), p.SurfaceElevated) {
		t.Fatal("huh role mapping")
	}
}

func TestSettingsWidgetImportsStayInsideCenter(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".go" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if imp, ok := node.(*ast.ImportSpec); ok {
				path, _ := strconv.Unquote(imp.Path.Value)
				if (path == "charm.land/huh/v2" || strings.HasPrefix(path, "charm.land/bubbles/v2")) && !strings.HasPrefix(file.Name(), "settings") {
					t.Errorf("widget import outside center: %s", file.Name())
				}
			}
			return true
		})
	}
}
