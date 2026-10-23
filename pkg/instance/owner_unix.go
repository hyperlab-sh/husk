//go:build unix

package instance

import (
	"os"
	"syscall"
)

// ownUID reports the file's owner uid, or -1 when the platform's
// os.FileInfo does not expose it.
func ownUID(st os.FileInfo) int {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(s.Uid)
	}
	return -1
}
