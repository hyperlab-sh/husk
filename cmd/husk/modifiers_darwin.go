//go:build darwin

package main

/*
#cgo LDFLAGS: -framework ApplicationServices
#include <ApplicationServices/ApplicationServices.h>

unsigned int huskGetModifierFlags() {
    CGEventFlags flags = CGEventSourceFlagsState(kCGEventSourceStateHIDSystemState);
    unsigned int out = 0;
    if (flags & kCGEventFlagMaskAlternate) out |= 1; // Alt / Option
    if (flags & kCGEventFlagMaskControl)   out |= 2; // Ctrl
    if (flags & kCGEventFlagMaskShift)     out |= 4; // Shift
    if (flags & kCGEventFlagMaskCommand)   out |= 8; // Super / Command
    return out;
}
*/
import "C"
import "github.com/hyperlab-sh/husk/pkg/protocol"

func pollOSModifiers() (protocol.ModifierState, bool) {
	bits := uint(C.huskGetModifierFlags())
	return protocol.ModifierState{
		Alt:   (bits & 1) != 0,
		Ctrl:  (bits & 2) != 0,
		Shift: (bits & 4) != 0,
		Super: (bits & 8) != 0,
	}, true
}
