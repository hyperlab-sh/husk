package stem

import "context"

// await waits for a loop closure's result on done or for ctx to end. A result
// that is already there wins over an ended ctx: the work ran, so the reply
// must say so (select alone picks at random when both are ready).
func await[T any](ctx context.Context, done <-chan T) (T, bool) {
	select {
	case v := <-done:
		return v, true
	case <-ctx.Done():
		select {
		case v := <-done:
			return v, true
		default:
			var zero T
			return zero, false
		}
	}
}
