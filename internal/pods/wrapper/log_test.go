package wrapper

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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

// The read window of a long file starts mid-line; the cut fragment must be
// dropped whenever whole lines follow it.
func TestTailLinesLargeFileNeverReturnsTheCutLine(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "line %04d padding padding\n", i)
	}
	whole := regexp.MustCompile(`^line \d{4} padding padding$`)
	got := tailLines(writeLog(t, b.String()), 1000) // more than the window holds
	if len(got) == 0 || len(got) >= 1000 {
		t.Fatalf("got %d lines", len(got))
	}
	for i, line := range got {
		if !whole.MatchString(line) {
			t.Fatalf("line %d is not whole: %q", i, line)
		}
	}
	if last := got[len(got)-1]; last != "line 1999 padding padding" {
		t.Fatalf("last = %q", last)
	}
}

// With no newline to resync on, the window is the end of one long last line.
// That end is kept: it is the stderr tail, and dropping it would report
// nothing for a plugin that died mid-line.
func TestTailLinesSingleLongLastLineKeepsItsEnd(t *testing.T) {
	for name, content := range map[string]string{
		"newline terminated": strings.Repeat("x", 20000) + "\n",
		"unterminated":       strings.Repeat("x", 20000),
	} {
		t.Run(name, func(t *testing.T) {
			got := tailLines(writeLog(t, content), 3)
			if len(got) != 1 || got[0] == "" || strings.Trim(got[0], "x") != "" {
				t.Fatalf("got %d lines %.20q", len(got), got)
			}
			if len(got[0]) < 8000 {
				t.Fatalf("kept %d bytes of the long line", len(got[0]))
			}
		})
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
