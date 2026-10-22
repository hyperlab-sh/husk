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
	const window = 8 << 10
	seeked := false
	// A file of window+1 bytes or fewer is read whole from offset 0, which is
	// a line boundary. Beyond that, start one byte before the window: that
	// byte says whether the window starts on a boundary.
	if st, err := f.Stat(); err == nil && st.Size() > window+1 {
		_, serr := f.Seek(st.Size()-window-1, io.SeekStart)
		seeked = serr == nil
	}
	data, _ := io.ReadAll(io.LimitReader(f, window+1))
	if seeked && len(data) > 0 {
		// The first byte is outside the window. Unless it is a newline the
		// window starts mid-line: drop the cut fragment. When no whole line
		// follows it, the window is the end of one long last line and that
		// end is the best evidence there is, so it is kept.
		if data[0] != '\n' {
			if i := bytes.IndexByte(data, '\n'); i >= 0 && len(cleanLines(data[i+1:], 1)) > 0 {
				data = data[i+1:]
			}
		}
		if data[0] == '\n' {
			data = data[1:]
		}
	}
	return cleanLines(data, n)
}

// cleanLines strips escapes and control bytes from each line of data, drops
// the lines left empty and returns the last n.
func cleanLines(data []byte, n int) []string {
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
