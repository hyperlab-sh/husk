package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func anchor(id, key string) string { return "\x1b]husk;slot=" + id + ";key=" + key + "\x1b\\" }

func TestPublishedAnchorReplacementAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, content, suffix string
		width                         int
		want                          string
		hitX, hitWidth                int
	}{
		{"plain", "agent ", "[Run]", "", 20, "agent [Run]", 6, 5},
		{"truncated", "agent ", "[Inspect]", "", 10, "agent [Ins", 6, 4},
		{"ansi", "A ", "\x1b[32m[Run]\x1b[0m", "!", 6, "A [Run", 2, 4},
		{"unicode", "界 ", "好🙂Z", "", 7, "界 好🙂", 3, 4},
		{"no-room", "already-full", "[Run]", "", 5, "alrea", 0, 0},
		{"suffix", "A", "B", "C", 5, "ABC", 1, 1},
		{"inline-first-row", "", "one\ntwo", "", 10, "one", 0, 3},
		{"zero", "", "[Run]", "", 0, "", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPublicationRegistry()
			mustDeclare(t, r, "a", "a.action")
			mustContribute(t, r, "b", "a.action", "b.action", tc.content)
			out, hits := r.replaceAnchors("a", tc.prefix+anchor("a.action", "row-1")+tc.suffix, tc.width, 2)
			if got := ansi.Strip(out); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if strings.Contains(out, "husk;slot") {
				t.Fatal("OSC anchor leaked")
			}
			if tc.hitWidth == 0 {
				if len(hits) != 0 {
					t.Fatalf("invisible content has hits: %+v", hits)
				}
				return
			}
			if len(hits) != 1 || hits[0].X != tc.hitX || hits[0].Y != 2 || hits[0].Width != tc.hitWidth || hits[0].RowKey != "row-1" || hits[0].CommandID != "b.click" {
				t.Fatalf("wrong hit geometry: %+v", hits)
			}
		})
	}
}

func TestPublishedAnchorsOwnershipAndMultipleRows(t *testing.T) {
	r := newPublicationRegistry()
	mustDeclare(t, r, "a", "a.action")
	mustDeclare(t, r, "c", "c.action")
	mustContribute(t, r, "b", "a.action", "b.first", "1")
	mustContribute(t, r, "d", "a.action", "d.second", "2")
	mustContribute(t, r, "b", "c.action", "b.foreign", "foreign")
	source := []string{"X" + anchor("a.action", "one") + "/" + anchor("a.action", "two"), anchor("c.action", "foreign") + anchor("missing.slot", "") + "Z"}
	copySource := append([]string(nil), source...)
	rows, hits := r.renderPublished(huskipc.SlotMainStage, "a", source, 10, 3)
	if rows[0] != "X12/12    " || rows[1] != "Z         " || rows[2] != "          " {
		t.Fatalf("unexpected rows: %q", rows)
	}
	if len(hits) != 4 || hits[2].RowKey != "two" || hits[2].X != 4 {
		t.Fatalf("bad multiple-anchor hits: %+v", hits)
	}
	if !reflect.DeepEqual(source, copySource) {
		t.Fatal("source frame was mutated")
	}
}

func TestPublishedTruncationPreservesContributionOrder(t *testing.T) {
	r := newPublicationRegistry()
	mustDeclare(t, r, "a", "a.action")
	mustContribute(t, r, "b", "a.action", "b.wide", "界")
	mustContribute(t, r, "c", "a.action", "c.ascii", "X")
	line, hits := r.replaceAnchors("a", "A"+anchor("a.action", "row")+"suffix", 2, 0)
	if ansi.Strip(line) != "A" || len(hits) != 0 {
		t.Fatalf("later content jumped ahead of clipped wide glyph: %q %+v", line, hits)
	}
}

func TestPublishedRegionsStackClipAndOcclude(t *testing.T) {
	r := newPublicationRegistry()
	_ = r.claimSurface("a", huskipc.SlotMainStage)
	mustDeclare(t, r, "a", "a.inline")
	mustContribute(t, r, "b", "a.inline", "b.inline", "abcdefghij")
	if err := r.declareSlot("a", huskipc.SlotDeclarePayload{ID: "a.region", Kind: "region", Rect: &huskipc.PublishedRect{Surface: huskipc.SlotMainStage, X: 2, Y: 0, Width: 5, Height: 3}}); err != nil {
		t.Fatal(err)
	}
	mustContribute(t, r, "b", "a.region", "b.first", "first-too-long\n界界界")
	mustContribute(t, r, "c", "a.region", "c.second", "third\nfourth")
	rows, hits := r.renderPublished(huskipc.SlotMainStage, "a", []string{anchor("a.inline", "row"), "0123456789", "0123456789", "unchanged"}, 10, 3)
	want := []string{"abfirsthij", "01界界 789", "01third789"}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %q; want %q", rows, want)
	}
	if len(hits) != 5 {
		t.Fatalf("expected split inline hit and three region rows, got %+v", hits)
	}
	if hits[0].X != 0 || hits[0].Width != 2 || hits[1].X != 7 || hits[1].Width != 3 {
		t.Fatalf("occluded hits were not split: %+v", hits)
	}
	for _, row := range rows {
		if ansi.StringWidth(row) != 10 {
			t.Fatalf("bad cell width: %q", row)
		}
	}
	// Clip both axes to the resized publisher surface, never spill into neighbors.
	rows, hits = r.renderPublished(huskipc.SlotMainStage, "a", nil, 4, 1)
	if rows[0] != "  fi" || len(hits) != 1 || hits[0].Width != 2 {
		t.Fatalf("resize clipping failed: %q %+v", rows, hits)
	}
}

func TestPublishedEmptyRegionReservesSpace(t *testing.T) {
	r := newPublicationRegistry()
	_ = r.claimSurface("a", huskipc.SlotMainStage)
	if err := r.declareSlot("a", huskipc.SlotDeclarePayload{ID: "a.region", Kind: "region", Rect: &huskipc.PublishedRect{Surface: huskipc.SlotMainStage, X: 1, Y: 0, Width: 3, Height: 2}}); err != nil {
		t.Fatal(err)
	}
	rows, hits := r.renderPublished(huskipc.SlotMainStage, "a", []string{"abcdef", "ghijkl"}, 6, 2)
	if !reflect.DeepEqual(rows, []string{"a   ef", "g   kl"}) || len(hits) != 0 {
		t.Fatalf("empty rect not reserved: %q %+v", rows, hits)
	}
}

func TestPublishedUnanchoredLinesKeepLegacyTruncation(t *testing.T) {
	r := newPublicationRegistry()
	rows, hits := r.renderPublished(huskipc.SlotMainStage, "a", []string{"abcdefghi"}, 5, 1)
	if rows[0] != fitLineWidth("abcdefghi", 5) || len(hits) != 0 {
		t.Fatalf("plain surface truncation changed: %q %+v", rows, hits)
	}
}

func TestPublishedDemoFrame(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	m.recomputeLayout()
	m.seedPubslotsDemo()
	frame := ansi.Strip(m.View())
	if strings.Count(frame, "[Inspect]") != 2 || !strings.Contains(frame, "Published region from B") || strings.Contains(frame, "husk;slot=") {
		t.Fatalf("demo missing composed contributions:\n%s", frame)
	}
}
