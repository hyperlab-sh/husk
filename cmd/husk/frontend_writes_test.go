package main

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestFakeFrontendWritesAreDeferredAndRecorded(t *testing.T) {
	f := &fakeFE{}
	var settings, profiles, removals int
	failure := contracts.NewError(protocol.CodeUnavailable, "fixture refusal", nil)
	f.SetSetting("theme.name", "selected", func(result contracts.SettingSetResult, err error) {
		settings++
		if result.Key != "theme.name" || result.Value != "selected" || err != nil {
			t.Errorf("setting reply: %+v %v", result, err)
		}
	})
	f.UseProfile("work", func(result contracts.ProfileUseResult, err error) {
		profiles++
		if result.Active != "work" || err != nil {
			t.Errorf("profile reply: %+v %v", result, err)
		}
	})
	f.RemovePlugin("alpha", func(result contracts.PluginRemoval, err error) {
		removals++
		if result.Plugin != "alpha" || err != failure {
			t.Errorf("removal reply: %+v %v", result, err)
		}
	})
	if settings != 0 || profiles != 0 || removals != 0 {
		t.Fatal("a writer replied synchronously")
	}
	if len(f.SettingWrites) != 1 || f.SettingWrites[0].Key != "theme.name" || f.SettingWrites[0].Value != "selected" || f.SettingWrites[0].Reply == nil {
		t.Fatal("setting call not recorded")
	}
	if len(f.ProfileUses) != 1 || f.ProfileUses[0].Name != "work" || f.ProfileUses[0].Reply == nil {
		t.Fatal("profile call not recorded")
	}
	if len(f.PluginRemovals) != 1 || f.PluginRemovals[0].ID != "alpha" || f.PluginRemovals[0].Reply == nil {
		t.Fatal("removal call not recorded")
	}
	// The test acts as the loop: each queued reply is delivered after the call
	// returned, with the chosen result/error, and cannot be delivered twice.
	for range 2 {
		f.replySetting(0, contracts.SettingSetResult{Key: "theme.name", Value: "selected"}, nil)
		f.replyProfile(0, contracts.ProfileUseResult{Active: "work"}, nil)
		f.replyRemoval(0, contracts.PluginRemoval{Plugin: "alpha"}, failure)
	}
	if settings != 1 || profiles != 1 || removals != 1 {
		t.Fatalf("reply counts: %d %d %d", settings, profiles, removals)
	}
}
