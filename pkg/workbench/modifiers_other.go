//go:build !darwin || !cgo

package workbench

import "github.com/hyperlab-sh/husk/pkg/protocol"

func pollOSModifiers() (protocol.ModifierState, bool) {
	return protocol.ModifierState{}, false
}
