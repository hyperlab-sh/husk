package ptyhost

import "os/exec"

// macOS has no PR_SET_PDEATHSIG equivalent; shutdown uses stdin EOF and signals.
func parentDeath(*exec.Cmd) {}
