package huskipc

import "fmt"

// Valid reports whether s is one of the slot identifiers the protocol defines.
func (s SlotID) Valid() bool {
	switch s {
	case SlotActivityRail, SlotSidebarNavigation, SlotSidebarList, SlotThreadRowStatus,
		SlotSidebarFooter, SlotMainHeader, SlotMainStage, SlotSidePanel, SlotAppOverlay,
		SlotModalSurface, SlotFullCanvas, SlotWorkspaceStage:
		return true
	}
	return false
}

// Valid reports whether p is a placement the host knows how to open.
func (p RailPlacement) Valid() bool {
	switch p {
	case PlacementModal, PlacementFullCanvas, PlacementStage:
		return true
	}
	return false
}

// Valid reports whether p is a defined key_state phase.
func (p KeyPhase) Valid() bool {
	switch p {
	case KeyPhaseDown, KeyPhaseUp, KeyPhaseRepeat, KeyPhaseSync:
		return true
	}
	return false
}

// Validate checks the enumerated fields of a key_state payload.
func (p KeyStatePayload) Validate() error {
	if !p.Phase.Valid() {
		return fmt.Errorf("key_state: unknown phase %q (want down|up|repeat|sync)", p.Phase)
	}
	if p.Key == "" {
		return fmt.Errorf("key_state: key is empty")
	}
	if p.Slot != "" && !p.Slot.Valid() {
		return fmt.Errorf("key_state: unknown slot %q", p.Slot)
	}
	return nil
}

// Validate checks the enumerated fields of a slot_key payload.
func (p SlotKeyPayload) Validate() error {
	if !p.Slot.Valid() {
		return fmt.Errorf("slot_key: unknown slot %q", p.Slot)
	}
	if p.Key == "" {
		return fmt.Errorf("slot_key: key is empty")
	}
	return nil
}

// Validate checks that a rail item has an id and a placement the host can open.
func (r RailItemSpec) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("rail item: id is empty")
	}
	if !r.Placement.Valid() {
		return fmt.Errorf("rail item %q: unknown placement %q (want modal|full-canvas|stage)", r.ID, r.Placement)
	}
	return nil
}
