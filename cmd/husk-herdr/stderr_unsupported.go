//go:build !linux && !darwin

package main

// Platforms without descriptor path discovery leave the optional log path empty.
func stderrPath() string { return "" }
