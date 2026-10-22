package wrapper

import (
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
	if st, err := f.Stat(); err == nil && st.Size() > 8<<10 {
		_, _ = f.Seek(st.Size()-(8<<10), io.SeekStart)
	}
	data, _ := io.ReadAll(io.LimitReader(f, 8<<10))
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
