package keyspelling

import (
	"strings"
	"testing"
)

func TestReviewModifiedLettersAndShiftedSymbols(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"ctrl+X", "ctrl+x"}, {"alt+X", "alt+X"}, {"ctrl+alt+X", "alt+ctrl+x"}, {"shift+x", "X"}, {"shift+X", "X"}, {"alt+shift+X", "alt+X"}, {"ctrl+shift+X", "ctrl+shift+x"}, {"X", "X"}, {"ctrl+É", "ctrl+é"}} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := Normalize(tc.in)
			if err != nil || got != tc.want {
				t.Fatalf("%s => %q %v want %q", tc.in, got, err, tc.want)
			}
		})
	}
	for _, tc := range []struct{ in, produced string }{{"shift+1", "!"}, {"alt+shift+1", "!"}, {"shift+/", "?"}, {"shift+-", "_"}, {"shift+=", "+"}, {"shift+[", "{"}, {"shift+]", "}"}, {"shift+\\", "|"}, {"shift+;", ":"}, {"shift+'", "\""}, {"shift+,", "<"}, {"shift+.", ">"}, {"shift+`", "~"}, {"shift+0", ")"}} {
		t.Run(tc.in, func(t *testing.T) {
			_, err := Normalize(tc.in)
			if err == nil || !strings.Contains(err.Error(), "invalid_key") || !strings.Contains(err.Error(), tc.produced) {
				t.Fatalf("no produced-character hint %q: %v", tc.produced, err)
			}
		})
	}
}
