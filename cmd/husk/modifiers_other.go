//go:build !darwin

package main

import "github.com/hyperlab-sh/husk/pkg/huskipc"

func pollOSModifiers() (huskipc.ModifierState, bool) {
	return huskipc.ModifierState{}, false
}
