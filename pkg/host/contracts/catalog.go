package contracts

import "github.com/hyperlab-sh/husk/pkg/protocol"

// CatalogEntry is a plugin declaration in the optional layered catalog.
type CatalogEntry struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Surfaces    []protocol.SurfaceID `json:"surfaces"`
	Source      string               `json:"source"`
	Tags        []string             `json:"tags"`
	SourceLayer string               `json:"source_layer"`
}

// CatalogResult is a detached cache read. Problems never enter reload reports.
type CatalogResult struct {
	Entries  []CatalogEntry     `json:"entries"`
	Problems []protocol.Problem `json:"problems"`
}
