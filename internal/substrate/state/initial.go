package state

// Initial core namespaces and their capability owners. The module responsible
// for each slice keeps its handle private; these constants do not register any
// slices or establish a runtime registry. Pods construct dynamic plugin names
// as <plugin_id>.state and <workspace-owner>.workspace.
const (
	// OwnerHusk is reserved for in-process core modules, never a plugin id.
	OwnerHusk = "husk"

	// SurfacesNamespace holds occupancy, visibility and overlays; owned by Surfaces.
	SurfacesNamespace = "husk.surfaces"
	// SurfacesOwner is the capability owner of SurfacesNamespace.
	SurfacesOwner = OwnerHusk
	// FocusNamespace holds surface and terminal focus; owned by Surfaces.
	FocusNamespace = "husk.focus"
	// FocusOwner is the capability owner of FocusNamespace.
	FocusOwner = OwnerHusk
	// LayoutNamespace holds persistent dock preferences; owned by Surfaces.
	LayoutNamespace = "husk.layout"
	// LayoutOwner is the capability owner of LayoutNamespace.
	LayoutOwner = OwnerHusk
	// GeometryNamespace holds terminal size and computed rectangles; owned by Surfaces.
	GeometryNamespace = "husk.geometry"
	// GeometryOwner is the capability owner of GeometryNamespace.
	GeometryOwner = OwnerHusk
	// PodsNamespace holds per-plugin liveness; owned by Pods.
	PodsNamespace = "husk.pods"
	// PodsOwner is the capability owner of PodsNamespace.
	PodsOwner = OwnerHusk
	// ProfileNamespace holds the active profile; owned by the Profile service.
	ProfileNamespace = "husk.profile"
	// ProfileOwner is the capability owner of ProfileNamespace.
	ProfileOwner = OwnerHusk
	// GrantsNamespace holds secret grants, never secret values; owned by Secrets.
	GrantsNamespace = "husk.grants"
	// GrantsOwner is the capability owner of GrantsNamespace.
	GrantsOwner = OwnerHusk
	// StoreNamespace holds persistence diagnostics; owned by Store.
	StoreNamespace = "husk.store"
	// StoreOwner is the capability owner of StoreNamespace.
	StoreOwner = OwnerHusk
)
