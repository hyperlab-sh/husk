package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// docFiles returns the repository documents that describe the host. They are
// read relative to the cmd/husk package directory the tests run in.
func docFiles(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..")
	paths := []string{
		filepath.Join(root, "README.md"),
		filepath.Join(root, "SUMMARY.md"),
		filepath.Join(root, "AGENTS.md"),
	}
	more, err := filepath.Glob(filepath.Join(root, "docs", "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, more...)
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		out[filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))] = string(data)
	}
	return out
}

// TestDocsHaveNoApplicationResidue fails when the docs mention the
// application-specific commands, plugins and binaries that used to live in
// the host, or the stale surface count.
func TestDocsHaveNoApplicationResidue(t *testing.T) {
	banned := []string{
		"herdr", "tv.open", "fleet.open", "gitstage.open", "tv-search",
		"fleet-dashboard", "project-git", "nerd font", "60fps", "nine-slot",
	}
	for name, text := range docFiles(t) {
		lower := strings.ToLower(text)
		for _, b := range banned {
			if i := strings.Index(lower, b); i >= 0 {
				line := strings.Count(lower[:i], "\n") + 1
				t.Errorf("%s:%d mentions %q; the host docs must stay application-agnostic", name, line, b)
			}
		}
	}
}

// TestDocsSurfaceListMatchesRegistry checks that every surface the registry
// declares is named in the architecture slot description and in SUMMARY.md,
// and that the documented total equals the registry's.
func TestDocsSurfaceListMatchesRegistry(t *testing.T) {
	docs := docFiles(t)
	surfaces := []protocol.SurfaceID{
		protocol.SurfaceActivityRail, protocol.SurfaceSidebarNavigation, protocol.SurfaceSidebarList,
		protocol.SurfaceThreadRowStatus, protocol.SurfaceSidebarFooter, protocol.SurfaceMainHeader,
		protocol.SurfaceMainStage, protocol.SurfaceSidePanel, protocol.SurfaceAppOverlay,
		protocol.SurfaceModal, protocol.SurfaceFullCanvas, protocol.SurfaceWorkspaceStage,
	}
	var ids []string
	for _, id := range surfaces {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	if len(ids) != 12 {
		t.Fatalf("registry declares %d surfaces (%v); update the docs that say twelve, then this test", len(ids), ids)
	}
	for _, doc := range []string{"docs/docs/architecture.md", "SUMMARY.md", "README.md"} {
		text := docs[doc]
		for _, id := range ids {
			if !strings.Contains(text, "`"+id+"`") {
				t.Errorf("%s does not name surface `%s`", doc, id)
			}
		}
	}
	for _, doc := range []string{"docs/docs/architecture.md", "SUMMARY.md", "README.md"} {
		if !strings.Contains(strings.ToLower(docs[doc]), "twelve") {
			t.Errorf("%s does not state the surface count (twelve)", doc)
		}
	}
}

// TestDocsRepoPathsExist checks that every `cmd/...`, `pkg/...` or
// `plugins/...` path written in backticks resolves to a real file or
// directory.
func TestDocsRepoPathsExist(t *testing.T) {
	re := regexp.MustCompile("`((?:cmd|pkg|plugins)/[A-Za-z0-9_./-]+)`")
	// Build output that is git-ignored, so absent from a clean checkout.
	generated := map[string]bool{"plugins/reference/reference": true}
	root := filepath.Join("..", "..")
	for name, text := range docFiles(t) {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			p := strings.TrimRight(m[1], "/.")
			if generated[p] {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
				t.Errorf("%s references %q which does not exist", name, m[1])
			}
		}
	}
}
