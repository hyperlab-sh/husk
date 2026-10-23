//go:build !linux && !darwin

package ptyhost

import "testing"

func TestPTYRequiresSupportedUnixBackend(t *testing.T) {
	t.Skip("real PTY process tests require Linux or macOS; Windows ConPTY is not implemented")
}
