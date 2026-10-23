package host

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	ports "github.com/hyperlab-sh/husk/internal/host/ports/services"
	svccontracts "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Themes implements contracts.Frontend. Any goroutine; snapshot only.
func (r *Root) Themes() []contracts.ThemeEntry {
	themes, _ := (ports.Theme{Svc: r.rt.Theme}).List(context.Background(), contracts.Caller{Kind: protocol.CallerHost})
	return themes
}

// SettingsSchema implements contracts.Frontend. Any goroutine; snapshot only.
func (r *Root) SettingsSchema() []contracts.SettingEntry {
	entries, _ := (ports.Settings{Svc: r.rt.Settings}).Schema(context.Background(), contracts.Caller{Kind: protocol.CallerHost}, "")
	return entries
}

// Setting implements contracts.Frontend. Any goroutine; snapshot only.
func (r *Root) Setting(key string) (any, string, error) {
	return (ports.Settings{Svc: r.rt.Settings}).Get(context.Background(), contracts.Caller{Kind: protocol.CallerHost}, key)
}

// KeyListing implements contracts.Frontend. Any goroutine; the same port and
// CLI view used by the socket adapter, not a separate listing join.
func (r *Root) KeyListing() contracts.KeyListing {
	listing, _ := r.keys().List(context.Background(), contracts.Caller{Kind: protocol.CallerCLI})
	return listing
}

// Profiles implements contracts.Frontend. Any goroutine; snapshot only.
func (r *Root) Profiles() []contracts.ProfileInfo {
	profiles, _ := (ports.Profile{Svc: r.rt.Profile}).List(context.Background(), contracts.Caller{Kind: protocol.CallerHost})
	return profiles
}

// ConfigDir implements contracts.Frontend. Any goroutine; resolved at boot.
func (r *Root) ConfigDir() string { return r.store.Paths().Config }

// frontendWrite runs a port on a tracked worker and delivers its reply only
// through the loop. A rejected spawn or closed post gate drops the reply.
func frontendWrite[T any](r *Root, call func(context.Context) (T, error), reply func(T, error)) {
	r.spawn(func() tea.Msg {
		result, err := call(r.closingCtx)
		if r.closingCtx.Err() != nil {
			err = contracts.NewError(protocol.CodeCancelled, "husk is shutting down", map[string]any{"reason": "shutdown"})
		}
		if reply != nil {
			r.post(func() { reply(result, err) })
		}
		return nil
	})
}

// SetSetting implements contracts.Frontend. Call and reply on the loop; the
// port's Store write and reload wait run on the tracked worker.
func (r *Root) SetSetting(key string, value any, reply func(contracts.SettingSetResult, error)) {
	// A caller may reuse mutable form data as soon as this method returns.
	value = svccontracts.Clone(value)
	frontendWrite(r, func(ctx context.Context) (contracts.SettingSetResult, error) {
		return (ports.Settings{Svc: r.rt.Settings, Inputs: r.rt.Coordinator}).Set(ctx, contracts.Caller{Kind: protocol.CallerHost}, key, value)
	}, reply)
}

// UseProfile implements contracts.Frontend. Call and reply on the loop;
// the port runs on a tracked worker.
func (r *Root) UseProfile(name string, reply func(contracts.ProfileUseResult, error)) {
	frontendWrite(r, func(ctx context.Context) (contracts.ProfileUseResult, error) {
		return (ports.Profile{Svc: r.rt.Profile}).Use(ctx, contracts.Caller{Kind: protocol.CallerHost}, name)
	}, reply)
}

// RemovePlugin implements contracts.Frontend. Call and reply on the loop;
// the port runs on a tracked worker.
func (r *Root) RemovePlugin(id string, reply func(contracts.PluginRemoval, error)) {
	frontendWrite(r, func(ctx context.Context) (contracts.PluginRemoval, error) {
		return r.pods.Remove(ctx, contracts.Caller{Kind: protocol.CallerHost}, id)
	}, reply)
}
