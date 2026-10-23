//go:build !unix

package instance

import "os"

// ownUID reports -1: this platform does not expose the file owner through
// os.FileInfo, so the ownership half of the runtime-directory check is
// skipped there. Unix sockets and their peer-credential checks are out of
// scope for v1 on such platforms anyway.
func ownUID(st os.FileInfo) int { return -1 }
