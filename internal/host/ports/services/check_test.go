package services

import (
	"context"
	"testing"

	services "github.com/hyperlab-sh/husk/internal/services"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// theme.check dry-builds Settings first, so a candidate palette in the config
// is judged (not settings_missing), a failing one is a result with problems
// and not a protocol error, and nothing is published.
func TestThemeCheckDryBuildsSettingsFirst(t *testing.T) {
	for name, tc := range map[string]struct {
		raw      string
		origins  map[string]store.Layer
		wantCode string // every problem has it; "" = none
	}{
		"empty config passes":     {`{}`, nil, ""},
		"passing override":        {`{"theme":{"palette":{"canvas":"#101010"}}}`, map[string]store.Layer{"theme.palette.canvas": store.LayerUser}, ""},
		"failing ink is a result": {`{"theme":{"palette":{"ink":"#202020"}}}`, map[string]store.Layer{"theme.palette.ink": store.LayerUser}, "low_contrast"},
	} {
		t.Run(name, func(t *testing.T) {
			settingsSvc := newSet(settings.Options{})
			themeSvc := theme.New()
			genS, genT, before := settingsSvc.Generation(), themeSvc.Generation(), themeSvc.Palette()
			in := services.ReloadInput{Doc: store.Resolved{Doc: doc(t, tc.raw), Origins: tc.origins}}
			port := Theme{Svc: themeSvc, Inputs: staticInputs{in: in}, Settings: settingsSvc}
			res, err := port.Check(context.Background(), userCaller)
			if err != nil {
				t.Fatalf("check err = %v, want a result", err)
			}
			if (tc.wantCode == "") != (len(res.Problems) == 0) {
				t.Fatalf("problems = %+v, want code %q", res.Problems, tc.wantCode)
			}
			for _, p := range res.Problems {
				if p.Code != tc.wantCode || p.Severity != "error" {
					t.Fatalf("problem %+v, want %s error", p, tc.wantCode)
				}
			}
			if res.OK != (tc.wantCode == "") {
				t.Fatalf("ok = %v, want %v", res.OK, tc.wantCode == "")
			}
			if settingsSvc.Generation() != genS || themeSvc.Generation() != genT || themeSvc.Palette() != before {
				t.Fatal("dry check published a snapshot")
			}
		})
	}
}
