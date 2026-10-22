package stem

import "context"

// Loop is this package's single seam onto the host's Bubble Tea loop: the
// one goroutine that may mutate loop-confined modules (State, the Event bus,
// the Dispatcher, the Hook runner). The two methods cover the two ways a
// port crosses the seam; production wires one adapter over tea.Program's
// Post, tests wire inline or queueing fakes.
type Loop interface {
	// Do runs fn on the host loop goroutine and returns once it has run.
	// It returns an error, without running fn, when ctx ends first or the
	// loop has shut down. The Event bus publishes through Do.
	Do(ctx context.Context, fn func()) error

	// Post hands fn to the host loop and returns at once; it must not
	// block the caller on the loop's work, and returns false when the
	// loop no longer runs, in which case fn is not run. Ports that must
	// observe the outcome wait off the loop: Calls and Hooks on their
	// reply channels, State on the request context.
	Post(fn func()) bool
}
