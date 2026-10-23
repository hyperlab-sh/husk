package stem

import "context"

// Loop is this package's single seam onto the host's Bubble Tea loop: the
// one goroutine that may mutate loop-confined modules (State, the Event bus,
// the Dispatcher, the Hook runner). The two methods cover the two ways a
// port crosses the seam; production wires one adapter over tea.Program's
// Post, tests wire inline or queueing fakes.
type Loop interface {
	// Do runs fn on the host loop goroutine with claimed execution: either
	// fn runs to completion and Do returns nil, or fn is guaranteed never
	// to run and Do returns an error. When ctx ends while fn is already
	// running, Do waits for it and returns nil. Do also ends when the host's
	// own context ends, with the same completion-or-abandonment guarantee.
	// The Event bus publishes and Clear applies its reset through Do.
	Do(ctx context.Context, fn func()) error

	// Post hands fn to the host loop and returns at once; it must not
	// block the caller on the loop's work, and returns false when the
	// loop no longer runs, in which case fn is not run. Ports that must
	// observe the outcome wait off the loop: Calls and Hooks on their
	// reply channels, State's Set and Patch on the request context (Clear
	// uses Do once the persisted copies have been deleted).
	Post(fn func()) bool
}
