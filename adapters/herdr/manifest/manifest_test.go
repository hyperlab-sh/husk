package manifest

import (
	"fmt"
	"strings"
	"testing"
)

const minimal = "id='sample:plugin'\nname='Sample'\nversion='1.0'\nmin_herdr_version='0.9.0'\n"

func TestConversionProblems(t *testing.T) {
	for _, tc := range []struct {
		name, source, code string
		ids                []string
		fatal              bool
	}{
		{"syntax", "id=[", "manifest_invalid", nil, true},
		{"minimum", "id='sample'\nname='Sample'\nversion='1'", "manifest_invalid", nil, true},
		{"oversize", strings.Replace(minimal, "sample:plugin", strings.Repeat("a", 65), 1), "id_unmappable", nil, true},
		{"invalid_id", strings.Replace(minimal, "sample:plugin", "!bad", 1), "id_unmappable", nil, true},
		{"collision", minimal, "id_unmappable", []string{"sample:plugin", "sample-plugin"}, true},
		{"reverse_collision", strings.Replace(minimal, "sample:plugin", "sample-plugin", 1), "id_unmappable", []string{"sample-plugin", "sample:plugin"}, true},
		{"platform", minimal + "platforms=['windows']\n", "platform_unsupported", nil, true},
		{"unknown_platform", minimal + "platforms=['plan9']\n", "manifest_invalid", nil, true},
		{"empty_platforms", minimal + "platforms=[]\n", "manifest_invalid", nil, true},
		{"empty_build_platforms", minimal + "[[build]]\ncommand=['true']\nplatforms=[]\n", "manifest_invalid", nil, true},
		{"empty_entry_platforms", minimal + "[[actions]]\nid='show'\ntitle='Show'\ncommand=['true']\nplatforms=[]\n", "manifest_invalid", nil, true},
		{"duplicate", minimal + "[[actions]]\nid='pane-view'\ntitle='View'\ncommand=['true']\n[[panes]]\nid='view'\ntitle='View'\ncommand=['true']\n", "duplicate_action", nil, true},
		{"entry_collision", minimal + "[[actions]]\nid='a:b'\ntitle='A'\ncommand=['true']\n[[actions]]\nid='a-b'\ntitle='B'\ncommand=['true']\n", "duplicate_action", nil, true},
		{"pattern", minimal + "[[link_handlers]]\npattern='(?=lookahead)'\naction='missing'\n", "pattern_unsupported", nil, false},
		{"unknown_action", minimal + "[[link_handlers]]\npattern='.*'\naction='missing'\n", "unknown_action", nil, false},
		{"suggestion", minimal + "[[actions]]\nid='show'\ntitle='Show'\ncommand=['true']\ndefault_key='prefix+s'\n", "suggestion_ignored", nil, false},
		{"command", minimal + "[[startup]]\ncommand=[]\n", "manifest_invalid", nil, true},
		{"entry_id", minimal + "[[actions]]\nid='with.dot'\ntitle='Show'\ncommand=['true']\n", "id_unmappable", nil, true},
		{"size", minimal + "[[panes]]\nid='view'\ntitle='View'\nwidth='oops'\ncommand=['true']\n", "manifest_invalid", nil, true},
		{"placement", minimal + "[[panes]]\nid='view'\ntitle='View'\nplacement='unknown'\ncommand=['true']\n", "manifest_invalid", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, problems, _ := Convert([]byte(tc.source), Options{Dir: "/plugins/sample", Executable: "/opt/husk-herdr", Platform: "linux", RegistryIDs: tc.ids})
			if (doc == nil) != tc.fatal {
				t.Fatalf("fatal=%v, doc=%v", tc.fatal, doc)
			}
			if len(problems) != 1 || problems[0].Code != tc.code || problems[0].Field == "" {
				t.Fatalf("problems=%+v, want %s", problems, tc.code)
			}
			if !tc.fatal && (len(doc.Vendor.LinkHandlers) > 0 || tc.code == "suggestion_ignored" && len(doc.Actions) != 1) {
				t.Fatal("nonfatal conversion did not retain only valid declarations")
			}
		})
	}
}

func TestMappingAndFilters(t *testing.T) {
	for _, tc := range []struct {
		platform string
		want     bool
	}{{"linux", true}, {"darwin", true}, {"windows", false}} {
		t.Run(tc.platform, func(t *testing.T) {
			if got := Supports([]string{"linux", "macos"}, tc.platform); got != tc.want {
				t.Fatalf("Supports=%v", got)
			}
		})
	}
	for _, id := range []string{"A.b_c-d", "sample:tool"} {
		t.Run(id, func(t *testing.T) {
			mapped, err := MapID(id)
			if err != nil || mapped != strings.ReplaceAll(id, ":", "-") {
				t.Fatalf("mapped=%s, err=%v", mapped, err)
			}
		})
	}
	source := minimal + "[[actions]]\nid='ignored'\nplatforms=['windows']\ncommand=[]\n[[build]]\ncommand=['false']\n"
	doc, problems, notes := Convert([]byte(source), Options{Dir: "/plugins/sample", Executable: "/opt/husk-herdr", Platform: "linux", RegistryIDs: []string{"sample:plugin", "sample:plugin"}})
	if doc == nil || len(problems) != 0 || len(notes) != 1 || !strings.HasPrefix(notes[0], "build_not_run:") || len(doc.Actions) != 0 {
		t.Fatalf("doc=%+v problems=%v notes=%v", doc, problems, notes)
	}
	if fmt.Sprint(doc.Command) != "[/opt/husk-herdr run /plugins/sample]" || doc.ID != "sample-plugin" || doc.Protocol != 2 || doc.ManifestVersion != 2 {
		t.Fatalf("wrong output: %+v", doc)
	}
}
