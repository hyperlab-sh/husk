package wrapper

import (
	"bytes"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func tailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	seeked := false
	if st, err := f.Stat(); err == nil && st.Size() > 8<<10 {
		_, serr := f.Seek(st.Size()-(8<<10), io.SeekStart)
		seeked = serr == nil
	}
	data, _ := io.ReadAll(io.LimitReader(f, 8<<10))
	if seeked {
		// The window starts mid-line: drop the cut fragment. When no whole
		// line follows it, the window is the end of one long last line and
		// that end is the best evidence there is, so it is kept.
		if i := bytes.IndexByte(data, '\n'); i >= 0 && len(bytes.TrimSpace(data[i+1:])) > 0 {
			data = data[i+1:]
		}
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 0x20 && r != '\t' || r == 0x7f {
				return -1
			}
			return r
		}, ansi.Strip(line)))
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
