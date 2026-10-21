//go:build !darwin

package main

import "husk/pkg/huskipc"

func pollOSModifiers() (huskipc.ModifierState, bool) {
	return huskipc.ModifierState{}, false
}
