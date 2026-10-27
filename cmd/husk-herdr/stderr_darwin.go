package main

import (
	"syscall"
	"unsafe"
)

func stderrPath() string {
	var path [1024]byte
	// Darwin F_GETPATH retrieves the actual Store-owned stderr file without
	// importing any host implementation or deriving a persistence path.
	_, _, err := syscall.Syscall(syscall.SYS_FCNTL, 2, 50, uintptr(unsafe.Pointer(&path[0])))
	if err == 0 {
		for i, b := range path {
			if b == 0 {
				return string(path[:i])
			}
		}
	}
	return "/dev/stderr"
}
