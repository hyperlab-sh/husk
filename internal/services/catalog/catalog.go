// Package catalog reads the optional plugin catalog. It never acts on a source
// and never participates in a Services reload.
package catalog

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Entry is a validated winning catalog declaration.
type Entry struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Surfaces    []protocol.SurfaceID `json:"surfaces"`
	Source      string               `json:"source"`
	Tags        []string             `json:"tags"`
	SourceLayer string               `json:"source_layer"`
}

// Result is a detached catalog view; catalog problems are always warnings.
type Result struct {
	Entries  []Entry            `json:"entries"`
	Problems []protocol.Problem `json:"problems"`
}

// Reader is the Store's off-loop layered-document seam.
type Reader interface {
	Resolve(store.DocName, string) (store.Resolved, error)
	Layers(store.DocName, string) ([]store.LayerDoc, error)
}

// Read gathers load diagnostics before validating unmerged layers. A broken
// layer cannot hide valid declarations in another layer or fail a reload.
func Read(s Reader, profile string) Result {
	out := Result{Entries: []Entry{}, Problems: []protocol.Problem{}}
	resolved, err := s.Resolve("catalog", profile)
	if err != nil {
		out.Problems = append(out.Problems, settings.StoreProblem(store.Problem{Layer: "", File: "catalog.json", Message: err.Error()}, nil))
	}
	for _, p := range resolved.Problems {
		if p.Code != "type_clash" {
			out.Problems = append(out.Problems, settings.StoreProblem(p, nil))
		}
	}
	layers, err := s.Layers("catalog", profile)
	if err != nil {
		out.Problems = append(out.Problems, settings.StoreProblem(store.Problem{File: "catalog.json", Message: err.Error()}, nil))
		return out
	}
	winners := map[string]Entry{}
	for _, l := range layers {
		problem := func(key, reason string) {
			out.Problems = append(out.Problems, protocol.Problem{Severity: "warning", Code: "invalid_value", Layer: string(l.Layer), Key: key, Message: key + ": " + reason, Source: &protocol.Source{Layer: string(l.Layer), File: l.File}})
		}
		if v, ok := l.Doc["version"].(float64); !ok || v != 1 {
			problem("catalog.version", "want 1")
			continue
		}
		entries, ok := l.Doc["entries"].(map[string]any)
		if !ok {
			problem("catalog.entries", "want object")
			continue
		}
		for i, id := range l.OrderedMembers("/entries", entries) {
			if i == 256 {
				problem("catalog.entries", "more than 256 entries; the rest ignored")
				out.Problems[len(out.Problems)-1].Message = "more than 256 entries; the rest ignored"
				break
			}
			entry, field, reason := validate(id, entries[id])
			if reason != "" {
				key := "catalog." + id
				if field != "" {
					key += "." + field
				}
				problem(key, reason)
				continue
			}
			entry.SourceLayer = string(l.Layer)
			winners[id] = entry
		}
	}
	for _, e := range winners {
		out.Entries = append(out.Entries, e)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return out
}

func validate(id string, raw any) (Entry, string, string) {
	e := Entry{ID: id, Name: id, Surfaces: []protocol.SurfaceID{}, Tags: []string{}}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(id) {
		return e, "", "invalid plugin id"
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return e, "", "want object"
	}
	for _, f := range []string{"name", "description", "source"} {
		raw, present := obj[f]
		if !present && f != "source" {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			return e, f, "want string"
		}
		switch f {
		case "name":
			if utf8.RuneCountInString(s) > 128 {
				return e, f, "more than 128 runes"
			}
			e.Name = s
		case "description":
			if len(s) > 1024 {
				return e, f, "more than 1024 bytes"
			}
			e.Description = s
		case "source":
			if len(s) < 1 || len(s) > 1024 || !validSource(s) {
				return e, f, "want absolute path, home path or supported URL (1 to 1024 bytes)"
			}
			e.Source = s
		}
	}
	for _, f := range []string{"surfaces", "tags"} {
		raw, present := obj[f]
		if !present {
			continue
		}
		a, ok := raw.([]any)
		if !ok {
			return e, f, "want array of strings"
		}
		limit := 12
		if f == "tags" {
			limit = 16
		}
		if len(a) > limit {
			return e, f, fmt.Sprintf("more than %d values", limit)
		}
		seen := map[string]bool{}
		for _, raw := range a {
			s, ok := raw.(string)
			if !ok {
				return e, f, "want array of strings"
			}
			if f == "surfaces" {
				surface := protocol.SurfaceID(s)
				if !surface.Valid() {
					return e, f, "unknown surface"
				}
				if seen[s] {
					return e, f, "duplicate surface"
				}
				seen[s] = true
				e.Surfaces = append(e.Surfaces, surface)
			} else {
				if !regexp.MustCompile(`^[a-z0-9_-]{1,32}$`).MatchString(s) {
					return e, f, "invalid tag"
				}
				e.Tags = append(e.Tags, s)
			}
		}
	}
	return e, "", ""
}

func validSource(s string) bool {
	if filepath.IsAbs(s) || strings.HasPrefix(s, "~/") {
		return true
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "http", "https", "git", "git+ssh", "ssh":
		return true
	}
	return false
}
