package huskipc

import (
	"encoding/json"
	"strings"
	"testing"
)

// Surface metadata is generic: Husk knows nothing about any application, so
// slot titles and descriptions must not name one, and SlotMeta carries no
// third-party hotspot mapping.
func TestAllSlotsMetadataIsApplicationAgnostic(t *testing.T) {
	if len(AllSlots) != 9 {
		t.Fatalf("AllSlots has %d entries, want the 9 fixed surfaces", len(AllSlots))
	}
	banned := []string{
		"agent", "space", "worktree", "tab", "pane", "herdr", "bb", "tv",
		"telescope", "lazygit", "gh dash", "daemon", "token", "telemetry",
	}
	for i, s := range AllSlots {
		if s.Number != i {
			t.Fatalf("%s number = %d, want %d", s.ID, s.Number, i)
		}
		if s.Title == "" || s.Description == "" {
			t.Fatalf("%s lacks title/description", s.ID)
		}
		for _, field := range []string{s.Title, s.Description} {
			words := strings.FieldsFunc(strings.ToLower(field), func(r rune) bool {
				return !(r >= 'a' && r <= 'z')
			})
			text := " " + strings.Join(words, " ") + " "
			for _, b := range banned {
				if strings.Contains(text, " "+b+" ") || strings.Contains(text, " "+b+"s ") {
					t.Fatalf("%s metadata %q names application term %q", s.ID, field, b)
				}
			}
		}
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var keys map[string]any
		_ = json.Unmarshal(data, &keys)
		if _, ok := keys["bb_hotspot"]; ok || len(keys) != 4 {
			t.Fatalf("SlotMeta JSON = %s, want id/number/title/description only", data)
		}
	}
}
