package store

import "testing"

// LayersWithProblems reports the layer files Layers left out, with line and column.
func TestLayersWithProblemsReportsMalformedLayerFile(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "t1", nil)
	writeConfig(t, s, []string{"keymap.json"}, "{\n  \"version\": 1,\n  \"bind\": [,\n}")
	layers, ps, err := s.LayersWithProblems("keymap", "")
	if len(layers) != 0 {
		t.Fatalf("layers = %+v", layers)
	}
	if err != nil || len(ps) != 1 {
		t.Fatalf("problems = %+v, %v", ps, err)
	}
	if p := ps[0]; p.Code != "invalid_document" || p.Layer != LayerUser || p.Line != 3 || p.Message == "" {
		t.Errorf("problem = %+v", p)
	}
	writeConfig(t, s, []string{"keymap.json"}, `{"version":1,"bind":[]}`)
	s.Invalidate()
	if layers, ps, _ = s.LayersWithProblems("keymap", ""); len(ps) != 0 || len(layers) != 1 {
		t.Errorf("after fix: layers %+v problems %+v", layers, ps)
	}
}
