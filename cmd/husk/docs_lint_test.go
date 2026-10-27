package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
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
	generated := map[string]bool{"plugins/reference/reference": true, "plugins/guide/guide": true}
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

// The README's closed set of CLI error codes is the set output defines.
func TestReadmeErrorCodesMatchOutput(t *testing.T) {
	readme := docFiles(t)["README.md"]
	m := regexp.MustCompile("closed set: (.*?), plus the instance's own code").FindStringSubmatch(readme)
	if m == nil {
		t.Fatal("README has no closed-set sentence")
	}
	var got []string
	for _, c := range strings.Split(m[1], ",") {
		got = append(got, strings.Trim(strings.TrimSpace(c), "`"))
	}
	want := output.Codes()
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("README codes = %v, want %v", got, want)
	}
}

// Every command the CLI's usage text lists has a line in the README's
// command block.
func TestReadmeCommandBlockCoversCLIUsage(t *testing.T) {
	readme := docFiles(t)["README.md"]
	sec := readme[strings.Index(readme, "## Command line"):]
	start := strings.Index(sec, "```bash\n")
	if start < 0 {
		t.Fatal("README has no command block")
	}
	block := sec[start:]
	block = block[:strings.Index(block[8:], "```")+8]
	have := map[string]bool{}
	for _, l := range strings.Split(block, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[0] == "husk" {
			have[f[1]] = true
			if len(f) >= 3 {
				have[f[1]+" "+f[2]] = true
			}
		}
	}
	cmd := exec.Command(os.Args[0], "help")
	cmd.Env = append(os.Environ(), "HUSK_TEST_RUN_MAIN=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("husk help: %v\n%s", err, out)
	}
	checked := 0
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "usage:"))
		if !strings.HasPrefix(l, "husk ") || strings.HasPrefix(l, "husk [flags]") || l == "husk help" {
			continue
		}
		noun := ""
		for _, form := range strings.Split(l, "|") {
			f := strings.Fields(form)
			if len(f) == 0 {
				continue
			}
			if f[0] != "husk" { // "store paths | check": a bare verb of the same noun
				f = []string{"husk", noun, f[0]}
			}
			if len(f) < 2 || strings.HasPrefix(f[1], "__") {
				continue
			}
			noun = f[1]
			key := noun
			if len(f) >= 3 && !strings.ContainsAny(f[2], "<[-") {
				key += " " + f[2]
			}
			checked++
			if !have[key] {
				t.Errorf("README command block lacks %q (usage line %q)", key, l)
			}
		}
	}
	if checked < 30 {
		t.Fatalf("only %d usage forms checked; parsing broke\n%s", checked, out)
	}
}
