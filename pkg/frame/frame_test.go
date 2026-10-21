package frame

import "testing"

func TestInnerRectNoFrame(t *testing.T) {
	x, y, w, h := InnerRect(3, 4, 30, 12, Spec{})
	if x != 3 || y != 4 || w != 30 || h != 12 {
		t.Fatalf("zero spec must leave the rect untouched, got (%d,%d,%d,%d)", x, y, w, h)
	}
}

func TestInnerRectBareLabelOrClose(t *testing.T) {
	for _, s := range []Spec{
		{Label: "title"},
		{Close: true},
		{Label: "title", Close: true},
	} {
		x, y, w, h := InnerRect(0, 0, 20, 8, s)
		if x != 0 || y != 0 || w != 20 || h != 8 {
			t.Fatalf("spec %+v must not consume cells, got (%d,%d,%d,%d)", s, x, y, w, h)
		}
	}
}

func TestInnerRectBorder(t *testing.T) {
	x, y, w, h := InnerRect(10, 2, 40, 16, Spec{Border: true})
	if x != 11 || y != 3 {
		t.Fatalf("border insets origin by one, got (%d,%d)", x, y)
	}
	if w != 38 || h != 14 {
		t.Fatalf("border consumes two cells per axis, got %dx%d", w, h)
	}
}

func TestInnerRectBorderClampsToMinimum(t *testing.T) {
	_, _, w, h := InnerRect(0, 0, 1, 1, Spec{Border: true})
	if w < 1 || h < 1 {
		t.Fatalf("inner rect must stay >= 1x1, got %dx%d", w, h)
	}
}

func TestEnabled(t *testing.T) {
	if (Spec{}).Enabled() {
		t.Fatal("zero spec must not be enabled")
	}
	if !(Spec{Close: true}).Enabled() || !(Spec{Label: "x"}).Enabled() || !(Spec{Border: true}).Enabled() {
		t.Fatal("any single element must enable the frame")
	}
}
