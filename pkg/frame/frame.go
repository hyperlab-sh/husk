// Package frame defines the chrome a plugin can ask Husk to draw around its
// surface. A surface itself is a blank canvas: Husk adds no decoration unless
// the occupying plugin declares a frame.
package frame

// Spec is the frame declaration a plugin sends in its hello payload (per-slot
// "frames" map) or on a rail item ("frame"). Each element is independent;
// an absent spec means no chrome at all.
type Spec struct {
	Border bool   `json:"border"`
	Label  string `json:"label,omitempty"`
	Close  bool   `json:"close,omitempty"`
}

// Enabled reports whether any chrome is requested.
func (s Spec) Enabled() bool {
	return s.Border || s.Label != "" || s.Close
}

// InnerRect returns the content area (x, y, w, h) inside the outer rect for a
// surface framed with spec s. Only a border consumes cells: a bare label or
// close button is drawn over the content and never changes geometry.
func InnerRect(x, y, w, h int, s Spec) (int, int, int, int) {
	if !s.Border {
		return x, y, max(1, w), max(1, h)
	}
	return x + 1, y + 1, max(1, w-2), max(1, h-2)
}
