package manifest

import (
	"strings"
	"testing"
)

func TestEntryIdentifiers(t *testing.T) {
	for _, tc := range []struct {
		id, want string
		valid    bool
	}{
		{"_internal", "_internal", true}, {"-x", "-x", true}, {"husk", "husk", true},
		{strings.Repeat("a", 100), strings.Repeat("a", 100), true}, {"a:b", "a-b", true},
		{"with.dot", "", false}, {"", "", false}, {"bad!", "", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			source := minimal + "[[actions]]\nid='" + tc.id + "'\ntitle='Show'\ncommand=['true']\n"
			doc, problems, _ := Convert([]byte(source), Options{Dir: "/plugins/sample", Executable: "/opt/husk-herdr", Platform: "linux"})
			if !tc.valid {
				if doc != nil || len(problems) != 1 || problems[0].Code != "id_unmappable" {
					t.Fatalf("invalid entry accepted: doc=%v problems=%v", doc, problems)
				}
				return
			}
			if doc == nil || len(problems) != 0 || len(doc.Actions) != 1 || doc.Actions[0].ID != "sample-plugin."+tc.want {
				t.Fatalf("legal entry refused: doc=%+v problems=%v", doc, problems)
			}
		})
	}
}
