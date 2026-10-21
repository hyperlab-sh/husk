//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
)

// parentPID returns the parent pid of pid, read from /proc/<pid>/stat.
func parentPID(pid int) (int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	return parseStatPPID(b)
}

// parseStatPPID extracts the ppid from a /proc/<pid>/stat line. The comm field
// is parenthesised and may itself contain spaces and parentheses, so the
// fields after it are located from the last ')'.
func parseStatPPID(b []byte) (int, error) {
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, fmt.Errorf("malformed stat line")
	}
	fields := bytes.Fields(b[i+1:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("malformed stat line")
	}
	return strconv.Atoi(string(fields[1]))
}
