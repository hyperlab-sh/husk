package main

import "os"

func stderrPath() string {
	if path, err := os.Readlink("/proc/self/fd/2"); err == nil {
		return path
	}
	return "/dev/stderr"
}
