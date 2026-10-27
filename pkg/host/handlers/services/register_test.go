package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func wired(t *testing.T) (*handlers.Mux, *fakes.Recorder) {
	t.Helper()
	rec := fakes.NewRecorder()
	m := handlers.NewMux()
	Register(m, Ports{
		Settings: fakes.NewSettings(rec),
		Secrets:  fakes.NewSecrets(rec),
		Profile:  fakes.NewProfile(rec),
		Theme:    fakes.NewTheme(rec),
		Keys:     fakes.NewKeybinding(rec),
		Config:   fakes.NewConfig(rec),
		Limiter:  NewWindowLimiter(fakes.NewClock(time.Unix(0, 0)), contracts.RateLimitPerSecond),
	})
	return m, rec
}

// servicesParams is one minimal valid params object per inbound method.
var servicesParams = map[string]string{
	"settings.get":      `{"key":"theme.name"}`,
	"settings.list":     `{}`,
	"settings.schema":   `{"owner":"husk"}`,
	"settings.validate": `{}`,
	"settings.set":      `{"key":"theme.name","value":"evening"}`,
	"secrets.get":       `{"name":"token"}`,
	"secrets.names":     `{}`,
	"secrets.set":       `{"name":"token","value":"v"}`,
	"secrets.grant":     `{"plugin":"files","name":"token"}`,
	"secrets.revoke":    `{"plugin":"files","name":"token"}`,
	"secrets.reveal":    `{"name":"token"}`,
	"profile.list":      `{}`,
	"profile.use":       `{"name":"work"}`,
	"theme.get":         `{}`,
	"theme.list":        `{}`,
	"theme.check":       `{}`,
	"keys.list":         `{}`,
	"config.reload":     `{}`,
}

// servicesPorts names the port families this package serves, as
// contracts.MethodHomes spells them.
var servicesPorts = map[string]bool{
	"SettingsPort":   true,
	"SecretsPort":    true,
	"ProfilePort":    true,
	"ThemePort":      true,
	"KeybindingPort": true,
	"ConfigPort":     true,
}

func TestRegisterServesEveryServicesMethod(t *testing.T) {
	m, rec := wired(t)
	homed := 0
	for method, home := range contracts.MethodHomes() {
		if !servicesPorts[home.Port] {
			continue
		}
		homed++
		params, ok := servicesParams[method]
		if !ok {
			t.Fatalf("%s: no test params registered", method)
		}
		caller := cliCaller
		if !kindAllowed(home.Kinds, contracts.CallerCLI) {
			t.Fatalf("%s: expected a CLI-callable services method", method)
		}
		reply := m.Serve(context.Background(), caller, protocol.Message{Method: method, Params: json.RawMessage(params)}, nil)
		if reply.Error != nil {
			t.Fatalf("%s: %+v", method, reply.Error)
		}
		if len(rec.Methods()) != homed {
			t.Fatalf("%s: port not reached (calls so far %d)", method, len(rec.Methods()))
		}
	}
	if homed != len(servicesParams) {
		t.Fatalf("MethodHomes homed %d services methods, params cover %d", homed, len(servicesParams))
	}
}

func TestRegisterDeniesPluginsTheCLIMethods(t *testing.T) {
	m, rec := wired(t)
	for method, params := range servicesParams {
		home := contracts.MethodHomes()[method]
		if kindAllowed(home.Kinds, contracts.CallerPlugin) {
			continue
		}
		for _, pluginID := range []string{"files", ""} {
			t.Run(method+"/plugin="+pluginID, func(t *testing.T) {
				rec.Reset()
				// Even an empty plugin id hits MethodHomes before the
				// Services-specific unattributed-caller check.
				reply := m.Serve(context.Background(), pluginCaller(pluginID), protocol.Message{Method: method, Params: json.RawMessage(params)}, nil)
				if reply.Error == nil || reply.Error.Code != protocol.CodeDenied {
					t.Fatalf("%s: %+v, want denied", method, reply.Error)
				}
				var data struct {
					Reason string `json:"reason"`
				}
				if err := json.Unmarshal(reply.Error.Data, &data); err != nil {
					t.Fatalf("%s: decoding denial data: %v", method, err)
				}
				if data.Reason != "user_only" {
					t.Fatalf("%s: reason = %q, want user_only", method, data.Reason)
				}
				if calls := rec.Methods(); len(calls) != 0 {
					t.Fatalf("%s: port touched before denial: %v", method, calls)
				}
			})
		}
	}
}

// TestRegisterDeniesUnattributedCallers pins the D36/D51 rows: a zero-kind
// caller and a plugin-kind caller with no plugin id are denied on every
// Services method, even the plugin-open ones, before any port is touched. (A
// non-plugin caller carrying a plugin id is denied downstream by Services
// itself: a nonempty PluginID never claims user authority.)
func TestRegisterDeniesUnattributedCallers(t *testing.T) {
	m, rec := wired(t)
	for method, params := range servicesParams {
		for name, c := range map[string]contracts.Caller{
			"zero kind":         {},
			"plugin without id": {Kind: contracts.CallerPlugin},
			"unknown kind":      {Kind: "bogus"},
			// Stem owns none of the Services methods: no method home below
			// includes it. A future services method that wants Stem must add
			// its home — and this row is what fails first if someone widens
			// an existing home instead of ruling it.
			"stem": {Kind: contracts.CallerHost},
		} {
			rec.Reset()
			reply := m.Serve(context.Background(), c, protocol.Message{Method: method, Params: json.RawMessage(params)}, nil)
			if reply.Error == nil || reply.Error.Code != protocol.CodeDenied {
				t.Fatalf("%s (%s): %+v, want denied", method, name, reply.Error)
			}
			if calls := rec.Methods(); len(calls) != 0 {
				t.Fatalf("%s (%s): port touched before denial: %v", method, name, calls)
			}
		}
	}
}

func TestRegisterUnknownPrefixAndMethod(t *testing.T) {
	m, _ := wired(t)
	reply := m.Serve(context.Background(), cliCaller, protocol.Message{Method: "atlas.get"}, nil)
	if reply.Error == nil || reply.Error.Code != protocol.CodeUnknownMethod {
		t.Fatalf("unknown prefix = %+v", reply.Error)
	}
	reply = m.Serve(context.Background(), cliCaller, protocol.Message{Method: "settings.poke"}, nil)
	if reply.Error == nil || reply.Error.Code != protocol.CodeUnknownMethod {
		t.Fatalf("unknown method in prefix = %+v", reply.Error)
	}
}

func TestRegisterPanicsWithoutEveryPort(t *testing.T) {
	full := Ports{
		Settings: fakes.NewSettings(nil), Secrets: fakes.NewSecrets(nil),
		Profile: fakes.NewProfile(nil), Theme: fakes.NewTheme(nil),
		Keys: fakes.NewKeybinding(nil), Config: fakes.NewConfig(nil),
		Limiter: NewWindowLimiter(fakes.NewClock(time.Unix(0, 0)), contracts.RateLimitPerSecond),
	}
	for _, drop := range []func(*Ports){
		func(p *Ports) { p.Settings = nil },
		func(p *Ports) { p.Secrets = nil },
		func(p *Ports) { p.Profile = nil },
		func(p *Ports) { p.Theme = nil },
		func(p *Ports) { p.Keys = nil },
		func(p *Ports) { p.Config = nil },
		func(p *Ports) { p.Limiter = nil },
	} {
		p := full
		drop(&p)
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("Register accepted a missing port")
				}
			}()
			Register(handlers.NewMux(), p)
		}()
	}
	Register(handlers.NewMux(), full) // every port present: no panic
}

func kindAllowed(kinds []contracts.CallerKind, k contracts.CallerKind) bool {
	for _, want := range kinds {
		if want == k {
			return true
		}
	}
	return false
}
