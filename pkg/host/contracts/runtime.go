package contracts

import "github.com/hyperlab-sh/husk/pkg/protocol"

// RuntimeSurface is an open runtime surface. State records the plugin,
// placement, title and requested outer dimensions; the ordered frontend seam
// additionally supplies its id and detached frame declaration.
type RuntimeSurface struct {
	// FocusRevision changes on an explicit focus request, including for the top.
	// It is a loop-only frontend signal, not persisted runtime metadata.
	FocusRevision uint64                    `json:"-"`
	Surface       protocol.SurfaceID        `json:"-"`
	Plugin        string                    `json:"plugin"`
	Placement     protocol.SurfacePlacement `json:"placement"`
	Title         string                    `json:"title"`
	Width         protocol.SurfaceDimension `json:"width"`
	Height        protocol.SurfaceDimension `json:"height"`
	Frame         *protocol.FrameSpec       `json:"-"`
}
