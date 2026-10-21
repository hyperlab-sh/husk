//go:build !linux && !darwin

package main

import "errors"

func parentPID(int) (int, error) {
	return 0, errors.New("parent pid lookup unsupported on this platform")
}
