package guide

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestDefaultFrame(t *testing.T) {
	l := fixture()
	h := start(t, l)
	lines := h.on(fixtureKeys())
	grid := decode(t, lines)
	if len(grid) != 40 {
		t.Fatalf("rows %d", len(grid))
	}
	order := []protocol.SurfaceID{protocol.SurfaceActivityRail, protocol.SurfaceSidebarNavigation, protocol.SurfaceMainHeader, protocol.SurfaceSidePanel, protocol.SurfaceSidebarList, protocol.SurfaceMainStage, protocol.SurfaceSidebarFooter}
	legend := legendBounds(l)
	callouts := []string{"①", "② Sidebar navigation", "③ Main header · main-header · empty · f1", "④ Side panel · side-panel · empty · ]", "⑤ Sidebar list · sidebar-list", "⑥ Main stage · main-stage · example.editor · f1", "⑦ Sidebar footer"}
	for i, id := range order {
		r := l.Geometry.Rects[id]
		var caption strings.Builder
		for x := r.X + 1; x <= r.X+len([]rune(callouts[i])); x++ {
			caption.WriteString(grid[r.Y][x].text)
		}
		if caption.String() != callouts[i] {
			t.Fatalf("%s callout %q, want %q", id, caption.String(), callouts[i])
		}
		chars, fg := []rune("┌┐└┘─│"), "38;2;17;34;51"
		if id == protocol.SurfaceMainStage {
			chars, fg = []rune("╔╗╚╝═║"), "38;2;34;51;68"
			assertTextAt(t, grid, r.X+1, r.Y, callouts[i], "38;2;17;34;51")
		}
		for y := r.Y; y < r.Y+r.Height; y++ {
			for x := r.X; x < r.X+r.Width; x++ {
				top, bottom := y == r.Y, y == r.Y+r.Height-1
				left, right := x == r.X, x == r.X+r.Width-1
				if !(top || bottom || left || right) || inside(legend, x, y) || top && x > r.X && x <= r.X+len([]rune(callouts[i])) {
					continue
				}
				glyph := chars[5]
				switch {
				case top && left:
					glyph = chars[0]
				case top && right:
					glyph = chars[1]
				case bottom && left:
					glyph = chars[2]
				case bottom && right:
					glyph = chars[3]
				case top || bottom:
					glyph = chars[4]
				}
				assertTextAt(t, grid, x, y, string(glyph), fg)
			}
		}
	}
	for y, row := range grid {
		if len(row) != 144 {
			t.Fatalf("row %d width %d", y, len(row))
		}
		for x, c := range row {
			allowed := inside(legend, x, y)
			for _, id := range order {
				r := l.Geometry.Rects[id]
				allowed = allowed || x >= r.X && x < r.X+r.Width && (y == r.Y || y == r.Y+r.Height-1) || y >= r.Y && y < r.Y+r.Height && (x == r.X || x == r.X+r.Width-1)
			}
			if !allowed && (c.text != " " || c.fg != "" || c.bg != "") {
				t.Fatalf("unexpected opaque cell (%d,%d): %+v", x, y, c)
			}
		}
	}
	if grid[5][39].fg != "38;2;34;51;68" || grid[1][7].fg != "38;2;17;34;51" {
		t.Fatal("focus and ordinary box colours")
	}
	if !strings.Contains(plain(lines), "tab next focus · f2 hide guide") {
		t.Fatal("missing help")
	}
}
func TestModalLast(t *testing.T) {
	l := fixture()
	l.Geometry.Rects[protocol.SurfaceModal] = rect{0, 0, 130, 15}
	l.Focus.Surface = protocol.SurfaceModal
	l.Surfaces.Overlays.Modal.RailItem = "example.inspect"
	l.Surfaces.Overlays.Modal.Plugin = "example"
	k := fixtureKeys()
	k.Entries = append(k.Entries, binding("example.inspect", "alt+2"), binding("husk.modal.close", "wrong"))
	k.Entries[len(k.Entries)-1].Focus = protocol.SurfaceSidebarList
	close := binding("husk.modal.close", "esc")
	close.Focus = protocol.SurfaceModal
	k.Entries = append(k.Entries, close)
	h := start(t, l)
	lines := h.on(k)
	text := plain(lines)
	if !strings.HasPrefix(text, "╔⑧ Modal surface · modal-surface · empty · alt+2, esc") {
		t.Fatalf("modal callout: %s", strings.Split(text, "\n")[0])
	}
}
func TestHiddenAndNotPlaced(t *testing.T) {
	l := fixture()
	for _, id := range []protocol.SurfaceID{protocol.SurfaceSidebarNavigation, protocol.SurfaceSidebarList, protocol.SurfaceSidebarFooter} {
		delete(l.Geometry.Rects, id)
	}
	h := start(t, l)
	frame := h.on(fixtureKeys())
	grid := decode(t, frame)
	for _, id := range []protocol.SurfaceID{protocol.SurfaceSidebarNavigation, protocol.SurfaceSidebarList, protocol.SurfaceSidebarFooter} {
		assertLegendRow(t, l, grid, id, "hidden", "[")
	}
	assertLegendRow(t, l, grid, protocol.SurfaceThreadRowStatus, "not placed", "")
	lines := strings.Split(plain(frame), "\n")
	for _, name := range []string{"Sidebar navigation", "Sidebar list", "Sidebar footer"} {
		found := false
		for _, line := range lines {
			if strings.Contains(line, name) && strings.Contains(line, "hidden") && strings.Contains(line, "[") {
				found = true
			}
		}
		if !found {
			t.Errorf("hidden legend row for %s", name)
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "Thread row status") {
			if !strings.Contains(line, "not placed") || strings.Contains(line, "[") {
				t.Fatalf("unplaced row %q", line)
			}
			return
		}
	}
	t.Fatal("missing humanised unplaced name")
}
func TestEmptyAndFailedClaimant(t *testing.T) {
	l := fixture()
	delete(l.Surfaces.Occupants, protocol.SurfaceMainStage)
	h := start(t, l)
	before := h.on(fixtureKeys())
	if !strings.Contains(plain(before), "empty") {
		t.Fatal("missing empty label")
	}
	grid := decode(t, before)
	stage := l.Geometry.Rects[protocol.SurfaceMainStage]
	prefix := "⑥ Main stage · "
	assertTextAt(t, grid, stage.X+1+len([]rune(prefix)), stage.Y, "main-stage", "38;2;17;34;51")
	prefix += "main-stage · "
	assertTextAt(t, grid, stage.X+1+len([]rune(prefix)), stage.Y, "empty", "38;2;68;85;102")
	assertTextAt(t, grid, stage.X+1+len([]rune(prefix+"empty · ")), stage.Y, "f1", "38;2;17;34;51")
	assertLegendRow(t, l, grid, protocol.SurfaceMainStage, "empty", "f1")
	h.socket(changed("husk.pods", map[string]any{"plugins": map[string]any{"example.editor": map[string]string{"status": "failed"}}}))
	keys := h.expect(protocol.MethodKeysList)
	h.respond(keys, fixtureKeys(), nil)
	after := h.frame()
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatal("failed claimant drawn as an occupant")
	}
	// A replacement resolution slice must remove old occupancy, not merge it.
	l.Surfaces.Occupants[protocol.SurfaceMainStage] = struct {
		Plugin string `json:"plugin"`
	}{"example.editor"}
	h.socket(changed("husk.surfaces", l.Surfaces))
	h.frame()
	h.socket(changed("husk.surfaces", map[string]any{"occupants": map[string]any{"app-overlay": map[string]string{"plugin": "guide"}}}))
	last := h.frame()
	if strings.Join(last, "\n") != strings.Join(before, "\n") {
		t.Fatal("removed occupant survived State replacement")
	}
}
func TestUnknownAndNarrowCallouts(t *testing.T) {
	l := fixture()
	l.Geometry.Rects["future-surface"] = rect{0, 10, 75, 4}
	l.Geometry.Rects[protocol.SurfaceSidebarList] = rect{7, 5, 20, 4}
	h := start(t, l)
	lines := strings.Split(plain(h.on(fixtureKeys())), "\n")
	if !strings.HasPrefix(lines[10], "┌⑦ Future surface · future-surface · empty") {
		t.Fatalf("unknown callout %q", lines[10])
	}
	if strings.Contains(lines[10][:60], "unbound") {
		t.Fatal("unknown id got a key")
	}
	if got := []rune(lines[5])[8:26]; strings.TrimRight(string(got), "─") != "⑤ Sidebar list" {
		t.Fatalf("narrow callout %q", string(got))
	}
	if []rune(lines[5])[7] != '┌' || []rune(lines[5])[26] != '┐' {
		t.Fatal("callout overwrote corners")
	}
	for _, width := range []int{4, 5} {
		l.Geometry.Rects[protocol.SurfaceSidebarList] = rect{7, 5, width, 4}
		h.socket(changed("husk.geometry", l.Geometry))
		grid := decode(t, h.frame())
		assertTextAt(t, grid, 7, 5, "┌⑤"+strings.Repeat("─", width-3)+"┐", "38;2;17;34;51")
	}
}
func TestToggleUsesLatestBaseline(t *testing.T) {
	l := fixture()
	h := start(t, l)
	h.on(fixtureKeys())
	h.toggle(2)
	if lines := h.frame(); lines == nil || len(lines) != 0 {
		t.Fatalf("off lines must be []: %#v", lines)
	}
	h.state(false)
	if m := receive(t, h.out); string(m.Result) != `{"shown":false}` {
		t.Fatalf("off reply %+v", m)
	}
	l.Geometry.Terminal.Width = 200
	l.Geometry.Terminal.Height = 60
	h.socket(changed("husk.geometry", l.Geometry), changed("husk.pods", map[string]any{}))
	keys := h.expect(protocol.MethodKeysList)
	h.respond(keys, fixtureKeys(), nil)
	h.toggle(3)
	keys = h.expect(protocol.MethodKeysList)
	h.respond(keys, fixtureKeys(), nil)
	lines := h.frame()
	if len(lines) != 60 || len(decode(t, lines)[0]) != 200 {
		t.Fatal("toggle did not use newest hidden baseline")
	}
	h.state(true)
	receive(t, h.out)
}
func TestRefusedClaimStaysLive(t *testing.T) {
	for _, tc := range []struct{ name, owner, message string }{
		{"occupied", "example.other", "app-overlay is occupied by example.other"},
		{"unclaimed", "", "app-overlay is not claimed by guide"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := fixture()
			delete(l.Surfaces.Occupants, protocol.SurfaceAppOverlay)
			if tc.owner != "" {
				l.Surfaces.Occupants[protocol.SurfaceAppOverlay] = struct {
					Plugin string `json:"plugin"`
				}{tc.owner}
			}
			h := start(t, l)
			h.toggle(1)
			keys := h.expect(protocol.MethodKeysList)
			h.respond(keys, fixtureKeys(), nil)
			h.frame()
			h.state(false)
			reply := receive(t, h.out)
			if reply.Error == nil || reply.Error.Code != protocol.CodeFailed || reply.Error.Message != tc.message {
				t.Fatalf("refusal %+v", reply)
			}
			if strings.Count(h.logs.String(), tc.message) != 1 {
				t.Fatal("refusal not logged once")
			}
			l.Surfaces.Occupants[protocol.SurfaceAppOverlay] = struct {
				Plugin string `json:"plugin"`
			}{"guide"}
			h.socket(changed("husk.surfaces", l.Surfaces), changed("husk.pods", map[string]any{}))
			keys = h.expect(protocol.MethodKeysList)
			h.respond(keys, fixtureKeys(), nil)
			// Still live and able to toggle after resolution hands over the claim.
			h.on(fixtureKeys())
		})
	}
}
func TestReconnectUsesFreshWatch(t *testing.T) {
	l := fixture()
	h := start(t, l)
	h.on(fixtureKeys())
	_ = h.peer.Close()
	l.Geometry.Terminal.Width = 200
	l.Geometry.Terminal.Height = 60
	delete(l.Geometry.Rects, protocol.SurfaceSidebarList)
	h.connect(l)
	keys := fixtureKeys()
	keys.Entries[3].Keys = []string{"ctrl+n"}
	keys.Entries[4].Keys = []string{"f3"}
	h.respond(h.expect(protocol.MethodKeysList), keys, nil)
	lines := h.frame()
	if !strings.Contains(plain(lines), "ctrl+n next focus · f3 hide guide") {
		t.Fatal("reconnect did not refresh ready keys")
	}
	if len(lines) != 60 || len(decode(t, lines)[0]) != 200 {
		t.Fatal("reconnect used stale geometry")
	}
	if strings.HasPrefix(strings.Split(plain(lines), "\n")[5][7:], "┌⑤") {
		t.Fatal("reconnect retained missing rect")
	}
}
func TestReconnectDuringKeysRequest(t *testing.T) {
	l := fixture()
	h := start(t, l)
	h.on(fixtureKeys())
	h.socket(changed("husk.pods", map[string]any{}))
	h.expect(protocol.MethodKeysList) // Intentionally leave this request unanswered.
	_ = h.peer.Close()
	l.Geometry.Terminal.Width, l.Geometry.Terminal.Height = 200, 60
	h.connect(l)
	keys := fixtureKeys()
	keys.Entries[4].Keys = []string{"f4"}
	h.respond(h.expect(protocol.MethodKeysList), keys, nil)
	lines := h.frame()
	if len(lines) != 60 || len(decode(t, lines)[0]) != 200 || !strings.Contains(plain(lines), "f4 hide guide") {
		t.Fatal("reconnect did not resume rendering with new baseline and keys")
	}
}

func TestKeysFailure(t *testing.T) {
	h := start(t, fixture())
	h.toggle(1)
	keys := h.expect(protocol.MethodKeysList)
	h.respond(keys, nil, &protocol.Error{Code: protocol.CodeDenied, Message: "refused"})
	lines := h.frame()
	h.state(true)
	receive(t, h.out)
	if !strings.Contains(plain(lines), "? next focus · ? hide guide") || !strings.Contains(h.logs.String(), "keys.list failed") {
		t.Fatal("key failure did not render and log ?")
	}
}
func TestReadBatchAndTooSmall(t *testing.T) {
	l := fixture()
	h := start(t, l)
	h.on(fixtureKeys())
	l.Geometry.TooSmall = true
	h.socket(changed("husk.geometry", l.Geometry), changed("husk.pods", map[string]any{}))
	keys := h.expect(protocol.MethodKeysList)
	h.respond(keys, fixtureKeys(), nil)
	l.Geometry.TooSmall = false
	l.Focus.Surface = protocol.SurfaceSidebarList
	h.socket(changed("husk.geometry", l.Geometry), changed("husk.focus", l.Focus))
	lines := h.frame()
	if decode(t, lines)[5][7].text != "╔" {
		t.Fatal("batch rendered before final focus change")
	}
	// An unrelated request is a deterministic barrier: an extra render from the
	// same read would appear before this next keys.list.
	h.socket(changed("husk.pods", map[string]any{}))
	keys = h.expect(protocol.MethodKeysList)
	h.respond(keys, fixtureKeys(), nil)
	h.frame()
}
func TestNumberingPastTwenty(t *testing.T) {
	l := fixture()
	l.Geometry.Terminal.Width = 200
	l.Geometry.Terminal.Height = 60
	l.Geometry.Rects = map[protocol.SurfaceID]rect{protocol.SurfaceAppOverlay: {0, 0, 200, 60}}
	for i := 0; i < 21; i++ {
		l.Geometry.Rects[protocol.SurfaceID(fmt.Sprintf("future-%02d", i))] = rect{0, i * 2, 20, 2}
	}
	h := start(t, l)
	lines := strings.Split(plain(h.on(fixtureKeys())), "\n")
	if !strings.HasPrefix(lines[38], "┌⑳") || !strings.HasPrefix(lines[40], "┌21") {
		t.Fatalf("number 20/21: %q / %q", lines[38], lines[40])
	}
	l.Geometry.Rects["future-20"] = rect{0, 40, 1, 1}
	h.socket(changed("husk.geometry", l.Geometry))
	grid := decode(t, h.frame())
	assertTextAt(t, grid, 0, 40, "2", "38;2;17;34;51")
	if grid[40][1].text != " " || grid[40][1].fg != "" {
		t.Fatal("unboxed number escaped its one-cell rect")
	}
}
func TestLegendGeometry(t *testing.T) {
	for _, size := range [][2]int{{84, 24}, {200, 60}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			l := fixture()
			l.Geometry.Terminal.Width, l.Geometry.Terminal.Height = size[0], size[1]
			h := start(t, l)
			grid := decode(t, h.on(fixtureKeys()))
			w, ht := size[0], size[1]
			left, right, top, bottom := w-66, w-3, ht-15, ht-2
			if grid[top][left].text != "┌" || grid[bottom][right].text != "┘" {
				t.Fatal("legend bounds")
			}
			for y := top; y <= bottom; y++ {
				for x := left; x <= right; x++ {
					if grid[y][x].bg != "48;2;85;102;119" {
						t.Fatalf("legend transparent at %d,%d", x, y)
					}
				}
			}
			if grid[ht-1][right].bg != "" {
				t.Fatal("legend covers footer")
			}
		})
	}
}
func TestThemeAndSmallRects(t *testing.T) {
	l := fixture()
	l.Geometry.Rects[protocol.SurfaceMainHeader] = rect{50, 0, 1, 1}
	h := start(t, l)
	grid := decode(t, h.on(fixtureKeys()))
	if grid[0][50].text != "①" || grid[0][51].text != " " {
		t.Fatal("one-cell rect must have only a clipped callout")
	}
	p := testPalette()
	p.Accent = "#667788"
	p.SurfaceElevated = "#778899"
	h.host(protocol.Message{Method: protocol.MethodThemeChanged, Params: raw(protocol.ThemeChangedParams{Palette: p})})
	grid = decode(t, h.frame())
	if grid[0][50].fg != "38;2;102;119;136" || grid[25][78].bg != "48;2;119;136;153" {
		t.Fatal("theme change did not repaint with the supplied roles")
	}
}
func TestDialFailureRetry(t *testing.T) {
	h := start(t, fixture(), 3)
	h.on(fixtureKeys())
	if strings.Count(h.logs.String(), "injected dial failure") != 1 || strings.Count(h.logs.String(), "connection recovered") != 1 {
		t.Fatalf("dial outage must log once plus recovery: %s", h.logs.String())
	}
}
func TestSourceEmbeddingAndManifest(t *testing.T) {
	var paths []string
	if err := fs.WalkDir(Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		if path == "guide" || strings.HasSuffix(path, "_test.go") {
			t.Errorf("unexpected embedded file %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"husk-plugin.json", "run.go", "embed.go", "cmd/guide/main.go"} {
		if _, err := fs.ReadFile(Files, name); err != nil {
			t.Fatal(err)
		}
	}
	b, err := fs.ReadFile(Files, "husk-plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		t.Fatal("bad manifest")
	}
	if string(m["id"]) != `"guide"` || string(m["command"]) != `["./guide"]` || string(m["surfaces"]) != `[{"id": "app-overlay"}]` || m["state"] != nil || m["keybindings"] != nil {
		t.Fatalf("manifest %s", b)
	}
	if len(paths) != 7 {
		t.Fatalf("source inventory %v", paths)
	}
}
