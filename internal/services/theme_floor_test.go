package services

import (
	"context"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/services/settings"
	stheme "github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// themeInput applies raw as the merged config with the given origins and
// returns the Theme input over the resulting Settings snapshot.
func themeInput(t *testing.T, raw string, origins map[string]store.Layer, layers ...store.LayerDoc) ReloadInput {
	t.Helper()
	s := newSet(settings.Options{})
	in := input(raw)
	in.Doc.Origins = origins
	apply(t, s, in)
	in.Settings = s.Snapshot()
	in.ConfigLayers = layers
	return in
}

// Only a distribution value of true is an opt-out, and a stronger layer
// cannot hide it.
func TestDistributionOptOutFiresForTrueOnly(t *testing.T) {
	dist := func(v string) store.LayerDoc {
		return store.LayerDoc{Layer: store.LayerDistribution, Doc: doc(`{"theme":{"allow_low_contrast":` + v + `}}`)}
	}
	for name, tc := range map[string]struct {
		raw     string
		origins map[string]store.Layer
		layers  []store.LayerDoc
		wantErr bool
	}{
		"true in distribution, passing palette": {`{"theme":{"allow_low_contrast":true}}`, map[string]store.Layer{"theme.allow_low_contrast": store.LayerDistribution}, []store.LayerDoc{dist("true")}, true},
		"false in distribution":                 {`{"theme":{"allow_low_contrast":false}}`, map[string]store.Layer{"theme.allow_low_contrast": store.LayerDistribution}, []store.LayerDoc{dist("false")}, false},
		"true overridden to false by user":      {`{"theme":{"allow_low_contrast":false}}`, map[string]store.Layer{"theme.allow_low_contrast": store.LayerUser}, []store.LayerDoc{dist("true"), {Layer: store.LayerUser, Doc: doc(`{"theme":{"allow_low_contrast":false}}`)}}, true},
		"absent from distribution":              {`{}`, nil, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			in := themeInput(t, tc.raw, tc.origins, tc.layers...)
			cand, rep, err := stheme.New().Rebuild(context.Background(), in)
			var opt []int
			for i, p := range rep.Problems {
				if p.Code == "distribution_opt_out" {
					opt = append(opt, i)
				}
			}
			if tc.wantErr {
				p := rep.Problems[opt[0]]
				if err == nil || cand != nil || len(opt) != 1 || p.Severity != "error" || p.Layer != "distribution" || p.Key != "theme.allow_low_contrast" || p.Message != "distribution cannot opt out of contrast" {
					t.Fatalf("err=%v problems=%+v", err, rep.Problems)
				}
				return
			}
			if err != nil || cand == nil || len(opt) != 0 {
				t.Fatalf("err=%v problems=%+v", err, rep.Problems)
			}
		})
	}
}

// A failing pair is charged to the foreground's layer, else the background's;
// the problem's Key is that role's settings key.
func TestLowContrastAttribution(t *testing.T) {
	user := store.LayerDoc{Layer: store.LayerUser, File: "/home/u/.config/husk/config.json", Doc: doc(`{}`)}
	find := func(rep Report, prefix string) (p Problem, ok bool) {
		for _, p := range rep.Problems {
			if p.Code == "low_contrast" && strings.HasPrefix(p.Message, prefix) {
				return p, true
			}
		}
		return Problem{}, false
	}
	t.Run("user overrides canvas only", func(t *testing.T) {
		in := themeInput(t, `{"theme":{"palette":{"canvas":"#ffffff"}}}`, map[string]store.Layer{"theme.palette.canvas": store.LayerUser}, user)
		_, rep, err := stheme.New().Rebuild(context.Background(), in)
		if err == nil {
			t.Fatal("white canvas passed contrast")
		}
		n := 0
		for _, p := range rep.Problems {
			if p.Code != "low_contrast" {
				continue
			}
			n++
			if p.Layer != "user" || p.Key != "theme.palette.canvas" || p.Source == nil || p.Source.Layer != "user" || p.Source.File != "config.json" {
				t.Errorf("not attributed to the user's canvas: %+v (source %+v)", p, p.Source)
			}
		}
		if n == 0 {
			t.Fatal("no low_contrast problem")
		}
		if p, ok := find(rep, "ink on canvas"); !ok || p.Key != "theme.palette.canvas" {
			t.Errorf("ink on canvas: %+v ok=%v", p, ok)
		}
	})
	t.Run("both overridden by different layers: the foreground's", func(t *testing.T) {
		in := themeInput(t, `{"theme":{"palette":{"ink":"#111111","canvas":"#101010"}}}`,
			map[string]store.Layer{"theme.palette.ink": store.LayerProfile, "theme.palette.canvas": store.LayerUser}, user)
		_, rep, err := stheme.New().Rebuild(context.Background(), in)
		if err == nil {
			t.Fatal("dark on dark passed contrast")
		}
		p, ok := find(rep, "ink on canvas")
		if !ok || p.Layer != "profile" || p.Key != "theme.palette.ink" || p.Source == nil || p.Source.Layer != "profile" {
			t.Fatalf("ink on canvas: %+v ok=%v", p, ok)
		}
	})
	t.Run("Source.File comes from the layer document", func(t *testing.T) {
		for layer, tc := range map[store.Layer]struct{ file, want string }{
			store.LayerInstance:     {"/run/state/instances/t1/config.json", "instances/t1/config.json"},
			store.LayerProfile:      {"/home/u/.config/husk/profiles/work/config.json", "profiles/work/config.json"},
			store.LayerUser:         {"/home/u/.config/husk/config.json", "config.json"},
			store.LayerDistribution: {"", ""},
		} {
			docs := store.LayerDoc{Layer: layer, File: tc.file, Doc: doc(`{}`)}
			in := themeInput(t, `{"theme":{"palette":{"canvas":"#ffffff"}}}`, map[string]store.Layer{"theme.palette.canvas": layer}, docs)
			_, rep, _ := stheme.New().Rebuild(context.Background(), in)
			p, ok := find(rep, "ink on canvas")
			if !ok || p.Source == nil || p.Source.Layer != string(layer) || p.Source.File != tc.want {
				t.Errorf("%s: %+v source %+v, want file %q", layer, p, p.Source, tc.want)
			}
		}
	})
	t.Run("a default foreground is never the attributed layer", func(t *testing.T) {
		in := themeInput(t, `{"theme":{"palette":{"surface":"#ffffff"}}}`, map[string]store.Layer{"theme.palette.surface": store.LayerInstance})
		_, rep, _ := stheme.New().Rebuild(context.Background(), in)
		for _, p := range rep.Problems {
			if p.Code == "low_contrast" && (p.Layer == "default" || p.Layer == "") {
				t.Fatalf("charged to %q: %+v", p.Layer, p)
			}
		}
	})
}
