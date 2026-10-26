package store

import "testing"

// LayerProblems reports the layer files Layers left out, with line and column.
func TestLayerProblemsReportsMalformedLayerFile(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "t1", nil)
	writeConfig(t, s, []string{"keymap.json"}, "{\n  \"version\": 1,\n  \"bind\": [,\n}")
	layers, err := s.Layers("keymap", "")
	if err != nil || len(layers) != 0 {
		t.Fatalf("layers = %+v, %v", layers, err)
	}
	ps, err := s.LayerProblems("keymap", "")
	if err != nil || len(ps) != 1 {
		t.Fatalf("problems = %+v, %v", ps, err)
	}
	if p := ps[0]; p.Code != "invalid_document" || p.Layer != LayerUser || p.Line != 3 || p.Message == "" {
		t.Errorf("problem = %+v", p)
	}
	writeConfig(t, s, []string{"keymap.json"}, `{"version":1,"bind":[]}`)
	s.Invalidate()
	if ps, _ = s.LayerProblems("keymap", ""); len(ps) != 0 {
		t.Errorf("problems after fix = %+v", ps)
	}
}
