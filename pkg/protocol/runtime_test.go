package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRuntimeProtocolValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch func(*SurfaceOpenParams)
	}{
		{"reserved_stage", func(p *SurfaceOpenParams) { p.Placement = "stage" }},
		{"reserved_panel", func(p *SurfaceOpenParams) { p.Placement = "panel" }},
		{"name_bytes", func(p *SurfaceOpenParams) { p.Name = strings.Repeat("a", 33) }},
		{"name_dot", func(p *SurfaceOpenParams) { p.Name = "a.b" }},
		{"name_empty", func(p *SurfaceOpenParams) { p.Name = "" }},
		{"title_runes", func(p *SurfaceOpenParams) { p.Title = strings.Repeat("界", 81) }},
		{"width_shape", func(p *SurfaceOpenParams) { p.Width = SurfaceDimension(`"20"`) }},
		{"height_shape", func(p *SurfaceOpenParams) { p.Height = SurfaceDimension(`1.5`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := SurfaceOpenParams{Placement: SurfacePlacementModal, Name: "picker"}
			tc.patch(&p)
			if p.Validate() == nil {
				t.Fatal("invalid runtime open accepted")
			}
		})
	}
	p := SurfaceOpenParams{Placement: SurfacePlacementCanvas, Name: strings.Repeat("a", 32), Title: strings.Repeat("界", 80), Width: SurfaceDimension(`"60%"`), Height: SurfaceDimension(`20`)}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var round SurfaceOpenParams
	if err = json.Unmarshal(b, &round); err != nil || string(round.Width) != `"60%"` || string(round.Height) != `20` {
		t.Fatal("dimension wire round trip failed")
	}
	for _, id := range []SurfaceID{"example.files.picker", "A.b_1.pick-2", "main-stage"} {
		if !id.Valid() {
			t.Fatalf("valid surface %s refused", id)
		}
	}
	for _, id := range []SurfaceID{".picker", "husk.picker", "a.Picker", "a.a.b?", "a.", "a." + SurfaceID(strings.Repeat("a", 33))} {
		if id.Valid() {
			t.Fatalf("bad surface %s accepted", id)
		}
	}
}

func TestRuntimeModalDimensionBounds(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		raw                      SurfaceDimension
		total, def, lo, hi, want int
	}{
		{"default_width", nil, 144, 92, 20, 134, 92},
		{"default_height", nil, 40, 24, 6, 34, 24},
		{"below_width", SurfaceDimension(`1`), 144, 92, 20, 134, 20},
		{"below_height", SurfaceDimension(`1`), 40, 24, 6, 34, 6},
		{"at_width_min", SurfaceDimension(`20`), 144, 92, 20, 134, 20},
		{"at_height_min", SurfaceDimension(`6`), 40, 24, 6, 34, 6},
		{"at_width_max", SurfaceDimension(`134`), 144, 92, 20, 134, 134},
		{"at_height_max", SurfaceDimension(`34`), 40, 24, 6, 34, 34},
		{"above_width", SurfaceDimension(`999`), 144, 92, 20, 134, 134},
		{"above_height", SurfaceDimension(`999`), 40, 24, 6, 34, 34},
		{"percent_full_terminal", SurfaceDimension(`"60%"`), 144, 92, 20, 134, 86},
		{"percent_clamps", SurfaceDimension(`"100%"`), 144, 92, 20, 134, 134},
		{"tiny", SurfaceDimension(`20`), 25, 15, 20, 15, 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.raw.Resolve(tc.total, tc.def, tc.lo, tc.hi); got != tc.want {
				t.Fatalf("dimension=%d want %d", got, tc.want)
			}
		})
	}
}
