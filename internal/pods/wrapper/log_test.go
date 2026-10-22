package wrapper

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeLog(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTailLines(t *testing.T) {
	var big strings.Builder
	for i := 0; i < 2000; i++ { // ~40 KiB, well past the 8 KiB read window
		fmt.Fprintf(&big, "line %04d padding padding\n", i)
	}
	for _, tc := range []struct {
		name, content string
		n             int
		want          []string
	}{
		{"empty file", "", 3, nil},
		{"fewer lines than asked", "a\nb\n", 3, []string{"a", "b"}},
		{"keeps the last n", "a\nb\nc\nd\ne\n", 3, []string{"c", "d", "e"}},
		{"no trailing newline", "a\nb\nc", 2, []string{"b", "c"}},
		{"blank lines skipped", "a\n\n   \nb\n\n", 3, []string{"a", "b"}},
		{"large file reads only the tail", big.String(), 3, []string{"line 1997 padding padding", "line 1998 padding padding", "line 1999 padding padding"}},
		{"sgr colours stripped", "\x1b[31mfatal:\x1b[0m boom\n", 3, []string{"fatal: boom"}},
		{"osc title and hyperlink stripped", "\x1b]0;title\x07before \x1b]8;;https://x.test\x1b\\link\x1b]8;;\x1b\\ after\n", 3, []string{"before link after"}},
		{"cursor and erase sequences stripped", "\x1b[2K\x1b[1Gpanic: oops\x1b[?25h\n", 3, []string{"panic: oops"}},
		{"bare control bytes dropped", "a\rb\x00c\x7fd\te\n", 3, []string{"abcd\te"}},
		{"line made only of escapes is empty", "ok\n\x1b[0m\x1b[K\n", 3, []string{"ok"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tailLines(writeLog(t, tc.content), tc.n)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tailLines = %q, want %q", got, tc.want)
			}
		})
	}
}

// The tail of a file longer than the read window starts mid-line; the cut
// line must never be reported as if it were a whole one while whole lines
// exist.
func TestTailLinesLargeFileNeverReturnsTheCutLine(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "line %04d padding padding\n", i)
	}
	got := tailLines(writeLog(t, b.String()), 1000) // more than the window holds
	if len(got) == 0 || len(got) >= 1000 {
		t.Fatalf("got %d lines", len(got))
	}
	for i := 1; i < len(got); i++ {
		if !strings.HasPrefix(got[i], "line ") || !strings.HasSuffix(got[i], " padding padding") {
			t.Fatalf("line %d is malformed: %q", i, got[i])
		}
	}
	if last := got[len(got)-1]; last != "line 1999 padding padding" {
		t.Fatalf("last = %q", last)
	}
}

func TestTailLinesMissingOrUnreadablePath(t *testing.T) {
	dir := t.TempDir()
	if got := tailLines(filepath.Join(dir, "absent.log"), 3); got != nil {
		t.Fatalf("missing file: %q", got)
	}
	// A directory opens but cannot be read; that must not panic or invent text.
	if got := tailLines(dir, 3); len(got) != 0 {
		t.Fatalf("directory: %q", got)
	}
	if got := tailLines("", 3); got != nil {
		t.Fatalf("empty path: %q", got)
	}
}
