package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

const keysFixture = `{"entries":[
 {"keys":["q"],"action":"husk.quit","source":{"layer":"husk","file":"builtin"},"state":"effective","future":true},
 {"keys":["n"],"focus":"sidebar-list","plugin":"files","action":"files.new","source":{"layer":"focus-local","plugin":"files"},"state":"shadowed","shadowed_by":{"layer":"user","file":"keymap.json","order":3}},
 {"keys":["n"],"focus":"sidebar-list","plugin":"files","action":"files.new","source":{"layer":"user","file":"keymap.json","order":3},"state":"future_state"},
 {"keys":["["],"disable":true,"source":{"layer":"user","file":"keymap.json","order":14},"state":"disabled"},
 {"keys":["x"],"action":"files.\n\u001b\u202e","source":{"layer":"husk","order":12},"state":"unknown_action"}
 ],"problems":[{"severity":"warning","code":"duplicate","layer":"user","key":"n","message":"duplicate binding","source":{"layer":"user","file":"keymap.json","order":3}}]}`

func TestKeysFilters(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   []string
		indexes []int
	}{
		{"default", nil, []int{0, 2, 3, 4}}, {"shadowed", []string{"--shadowed"}, []int{0, 1, 2, 3, 4}},
		{"action", []string{"--action", "files.new"}, []int{2}}, {"miss", []string{"--action", "missing"}, []int{}},
		{"focus", []string{"--focus", "sidebar-list"}, []int{2}}, {"both", []string{"--focus", "sidebar-list", "--action", "files.new", "--shadowed"}, []int{1, 2}},
		{"focus excludes global", []string{"--focus", ""}, []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, format := range []string{"json", "table"} {
				t.Run(format, func(t *testing.T) {
					srv, h := serviceServer(t, keysFixture, nil)
					args := append([]string{"keys"}, tc.flags...)
					args = append(args, "--socket", srv.Socket, "--format", format)
					if got := h.run(args...); got != 0 {
						t.Fatalf("exit %d", got)
					}
					if methods := srv.methods(); !reflect.DeepEqual(methods, []string{protocol.MethodSessionPing, protocol.MethodKeysList}) {
						t.Fatalf("methods %v", methods)
					}
					assertJSONEqual(t, lastRequest(t, srv).Params, `{}`)
					var original, got struct {
						Entries  []json.RawMessage
						Problems []protocol.Problem
					}
					if err := json.Unmarshal([]byte(keysFixture), &original); err != nil {
						t.Fatal(err)
					}
					if format == "json" {
						if err := json.Unmarshal(h.out.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						if len(got.Entries) != len(tc.indexes) || !reflect.DeepEqual(got.Problems, original.Problems) {
							t.Fatal("listing shape mismatch")
						}
						for i, index := range tc.indexes {
							assertJSONEqual(t, got.Entries[i], string(original.Entries[index]))
						}
					} else {
						if len(tc.indexes) == 0 {
							if h.out.Len() != 0 {
								t.Fatal("empty listing has output")
							}
							return
						}
						lines := strings.Split(strings.TrimSuffix(h.out.String(), "\n"), "\n")
						if len(lines) != len(tc.indexes)+1 || normalizedTable(lines[0]) != "KEY SCOPE ACTION LAYER SOURCE STATE" {
							t.Fatal("table shape mismatch")
						}
						if strings.ContainsAny(h.out.String(), "\x1b\u202e") {
							t.Fatal("unsafe terminal controls")
						}
						if tc.name == "shadowed" {
							for _, want := range []string{"sidebar-list (files)", "plugin files #0", "shadowed by user keymap.json#3", "(disabled)", "builtin#0", "builtin#12", "future_state", `\u001b`} {
								if !strings.Contains(h.out.String(), want) {
									t.Fatalf("missing %q", want)
								}
							}
						}
					}
				})
			}
		})
	}
}

func TestKeysProblems(t *testing.T) {
	for _, empty := range []bool{false, true} {
		for _, format := range []string{"json", "table"} {
			t.Run(format+map[bool]string{true: " empty", false: " populated"}[empty], func(t *testing.T) {
				reply := keysFixture
				if empty {
					reply = `{}`
				}
				_, h := serviceServer(t, reply, nil)
				if got := h.run("keys", "--problems", "--format", format); got != 0 {
					t.Fatalf("exit %d", got)
				}
				if format == "json" {
					var got map[string]json.RawMessage
					if err := json.Unmarshal(h.out.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if len(got) != 1 || got["problems"] == nil {
						t.Fatal("problems projection")
					}
					if empty {
						assertJSONEqual(t, got["problems"], `[]`)
					}
				} else if empty {
					if h.out.Len() != 0 {
						t.Fatal("empty problems output")
					}
				} else if normalizedTable(h.out.String()) != "SEVERITY CODE LAYER KEY SOURCE MESSAGE\nwarning duplicate user n keymap.json#3 duplicate binding" {
					t.Fatal("problems table mismatch")
				}
			})
		}
	}
}

func TestKeysRemoteError(t *testing.T) {
	_, h := serviceServer(t, "", &protocol.Error{Code: protocol.CodeDenied, Message: "user only"})
	if got := h.run("keys"); got != output.ExitRemote {
		t.Fatalf("exit %d", got)
	}
}
