package manifest

import (
	"bytes"
	"slices"
)

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func cloneSpec(s Spec) Spec {
	s.Command = slices.Clone(s.Command)
	s.Actions = slices.Clone(s.Actions)
	for i := range s.Actions {
		s.Actions[i].ArgsSchema = bytes.Clone(s.Actions[i].ArgsSchema)
	}
	s.Surfaces = slices.Clone(s.Surfaces)
	for i := range s.Surfaces {
		s.Surfaces[i].Frame = clonePtr(s.Surfaces[i].Frame)
	}
	s.RailItems = slices.Clone(s.RailItems)
	for i := range s.RailItems {
		s.RailItems[i].Frame = clonePtr(s.RailItems[i].Frame)
	}
	s.Keybindings = slices.Clone(s.Keybindings)
	for i := range s.Keybindings {
		s.Keybindings[i].Keys = slices.Clone(s.Keybindings[i].Keys)
		s.Keybindings[i].Args = bytes.Clone(s.Keybindings[i].Args)
	}
	s.Subscriptions = slices.Clone(s.Subscriptions)
	s.EventsDeclared = slices.Clone(s.EventsDeclared)
	for i := range s.EventsDeclared {
		s.EventsDeclared[i].Coalesce = clonePtr(s.EventsDeclared[i].Coalesce)
	}
	s.HooksDeclared = slices.Clone(s.HooksDeclared)
	s.HookRegistrations = slices.Clone(s.HookRegistrations)
	s.PublishedSlots = slices.Clone(s.PublishedSlots)
	for i := range s.PublishedSlots {
		s.PublishedSlots[i].Rect = clonePtr(s.PublishedSlots[i].Rect)
	}
	s.Slice = clonePtr(s.Slice)
	if s.Slice != nil {
		s.Slice.Retention = clonePtr(s.Slice.Retention)
	}
	s.SecretRequests = slices.Clone(s.SecretRequests)
	s.SettingsDeclared = slices.Clone(s.SettingsDeclared)
	for i := range s.SettingsDeclared {
		v := &s.SettingsDeclared[i]
		v.Default = bytes.Clone(v.Default)
		v.Enum = slices.Clone(v.Enum)
		v.Min = clonePtr(v.Min)
		v.Max = clonePtr(v.Max)
	}
	s.BindingProblems = slices.Clone(s.BindingProblems)
	s.Suggestions = slices.Clone(s.Suggestions)
	return s
}
