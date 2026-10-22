package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

func (s *Store) collectSlice(ctx context.Context, spec state.Spec) ([]string, error) {
	prefix := s.sliceKey(spec, "")
	entries, err := s.source.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	return s.collectRecords(ctx, entries, spec.Retention)
}
func (s *Store) collectRecords(ctx context.Context, entries []Entry, r state.Retention) ([]string, error) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Modified.Equal(entries[j].Modified) {
			return strings.Join(entries[i].Key.Path, "/") > strings.Join(entries[j].Key.Path, "/")
		}
		return entries[i].Modified.After(entries[j].Modified)
	})
	var removed []string
	generations := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		filename := entry.Key.Path[len(entry.Key.Path)-1]
		remove := false
		if r.Policy == state.KeepLastN && r.N >= 2 && r.N <= 20 && strings.HasPrefix(filename, "gen-") && strings.HasSuffix(filename, ".json") {
			generations++
			remove = generations > r.N
		}
		if r.Policy == state.TTL && r.TTL >= time.Minute && r.TTL <= 8760*time.Hour && filename == "current.json" {
			remove = !s.clock.Now().Before(entry.Modified.Add(r.TTL))
		}
		if remove {
			if err := s.source.Delete(ctx, entry.Key); err != nil {
				return removed, err
			}
			p, _ := s.source.path(entry.Key, false)
			removed = append(removed, p)
		}
	}
	return removed, nil
}

// GC applies known retention policies, removes stale crash temps and bounded
// quarantine copies, and trims old logs. Unknown policy metadata is never guessed.
func (s *Store) GC(ctx context.Context) (GCReport, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	var report GCReport
	entries, err := s.entries(true)
	if err != nil {
		return report, err
	}
	groups := map[string][]Entry{}
	policies := map[string]state.Retention{}
	policyFiles := map[string]Entry{}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		ns, ok := recordNamespace(entry.Key)
		if !ok {
			continue
		}
		data, _, err := s.source.Get(ctx, entry.Key)
		if err != nil {
			report.Problems = append(report.Problems, Problem{Code: "gc_read", Path: ns, Message: err.Error()})
			continue
		}
		env, err := parseEnvelope(data, entry.Key)
		if err != nil || env.Version > 1 {
			continue
		}
		id := storageID(entry.Key, ns)
		groups[id] = append(groups[id], entry)
		previous, exists := policyFiles[id]
		if !exists || entry.Modified.After(previous.Modified) || entry.Modified.Equal(previous.Modified) && strings.Join(entry.Key.Path, "/") > strings.Join(previous.Key.Path, "/") {
			policies[id] = env.Retention.policy()
			policyFiles[id] = entry
		}
	}
	s.mu.Lock()
	for id, spec := range s.policies {
		policies[id] = spec.Retention
	}
	for id := range s.blocked {
		delete(groups, id)
	}
	s.mu.Unlock()
	for id, entries := range groups {
		removed, err := s.collectRecords(ctx, entries, policies[id])
		report.Removed = append(report.Removed, removed...)
		if err != nil {
			report.Problems = append(report.Problems, Problem{Code: "gc_delete", Path: id, Message: err.Error()})
		}
	}
	for _, root := range []string{s.paths.Config, s.paths.State, s.paths.Data} {
		if err = s.collectAux(ctx, root, &report); err != nil {
			return report, err
		}
	}
	s.collectLogs(ctx, &report)
	for _, p := range report.Problems {
		s.diagnostic(p, "")
	}
	return report, ctx.Err()
}
func quarantineOriginal(name string) (string, bool) {
	i := strings.Index(name, ".json.corrupt-")
	if i < 0 {
		return "", false
	}
	original := name[:i+5]
	if original != "current.json" && !strings.HasPrefix(original, "gen-") {
		return "", false
	}
	stamp := name[i+len(".json.corrupt-"):]
	end := strings.IndexByte(stamp, 'Z')
	if end < 0 {
		return "", false
	}
	if _, err := time.Parse("20060102T150405.000000000Z", stamp[:end+1]); err != nil {
		return "", false
	}
	if suffix := stamp[end+1:]; suffix != "" {
		if len(suffix) < 2 || suffix[0] != '-' {
			return "", false
		}
		for _, c := range suffix[1:] {
			if c < '0' || c > '9' {
				return "", false
			}
		}
	}
	return original, true
}

func (s *Store) collectAux(ctx context.Context, root string, report *GCReport) error {
	quarantines := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			// WalkDir never follows a symlink. Skip this entry, not the
			// containing directory's remaining siblings.
			return nil
		}
		if d.IsDir() {
			if err := privateExistingDir(path); err != nil {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && strings.Contains(d.Name(), ".tmp-") && s.clock.Now().Sub(info.ModTime()) > 10*time.Minute {
			if err = os.Remove(path); err == nil {
				report.Removed = append(report.Removed, path)
			} else {
				report.Problems = append(report.Problems, Problem{Code: "gc_temp", File: path, Message: err.Error()})
			}
		}
		if original, ok := quarantineOriginal(d.Name()); ok && root == s.paths.State {
			base := filepath.Join(filepath.Dir(path), original)
			quarantines[base] = append(quarantines[base], path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, paths := range quarantines {
		sort.Sort(sort.Reverse(sort.StringSlice(paths)))
		for i, path := range paths {
			info, err := os.Lstat(path)
			if err != nil {
				continue
			}
			if i >= 3 || s.clock.Now().Sub(info.ModTime()) > 30*24*time.Hour {
				if err = os.Remove(path); err == nil {
					report.Removed = append(report.Removed, path)
				} else {
					report.Problems = append(report.Problems, Problem{Code: "gc_quarantine", File: path, Message: err.Error()})
				}
			}
		}
	}
	return nil
}
func (s *Store) collectLogs(ctx context.Context, report *GCReport) {
	root := filepath.Join(s.paths.State, "logs")
	if privateExistingDir(root) != nil {
		return
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type logDir struct {
		path     string
		modified time.Time
	}
	var candidates []logDir
	for _, dir := range dirs {
		if ctx.Err() != nil {
			return
		}
		if !dir.IsDir() {
			continue
		}
		p := filepath.Join(root, dir.Name())
		if privateExistingDir(p) != nil {
			continue
		}
		info, err := dir.Info()
		if err != nil {
			continue
		}
		files, _ := os.ReadDir(p)
		for _, file := range files {
			if file.Type()&os.ModeSymlink != 0 || file.IsDir() {
				continue
			}
			fi, err := file.Info()
			if err != nil {
				continue
			}
			if fi.ModTime().After(info.ModTime()) {
				info = fi
			}
			if (fi.Size() == 0 || s.clock.Now().Sub(fi.ModTime()) > 14*24*time.Hour) && dir.Name() != s.opts.Instance.ID {
				path := filepath.Join(p, file.Name())
				if os.Remove(path) == nil {
					report.Removed = append(report.Removed, path)
				}
			}
		}
		if dir.Name() != s.opts.Instance.ID {
			candidates = append(candidates, logDir{p, info.ModTime()})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modified.After(candidates[j].modified) })
	limit := 20
	if _, err := os.Stat(filepath.Join(root, s.opts.Instance.ID)); err == nil {
		limit--
	}
	for i, c := range candidates {
		if i >= limit || s.clock.Now().Sub(c.modified) > 14*24*time.Hour {
			if os.RemoveAll(c.path) == nil {
				report.Removed = append(report.Removed, c.path)
			}
		}
	}
}

// Check scans config/slice files without modifying them. It lists retained
// quarantine files and namespaces whose latest registration is over 90 days old.
func (s *Store) Check(ctx context.Context) (CheckReport, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	var report CheckReport
	entries, err := s.entries(true)
	if err != nil {
		return report, err
	}
	latest := map[string]time.Time{}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		ns, ok := recordNamespace(entry.Key)
		if !ok {
			continue
		}
		data, _, err := s.source.Get(ctx, entry.Key)
		env, parseErr := parseEnvelope(data, entry.Key)
		if err != nil {
			parseErr = err
		}
		if parseErr != nil {
			report.Problems = append(report.Problems, Problem{Code: "invalid_slice", Path: ns, Message: parseErr.Error()})
			continue
		}
		at := env.RegisteredAt
		if at.IsZero() {
			at = env.WrittenAt
		}
		if at.IsZero() {
			at = entry.Modified
		}
		if at.After(latest[ns]) {
			latest[ns] = at
		}
	}
	s.mu.Lock()
	for ns, at := range s.registered {
		if at.After(latest[ns]) {
			latest[ns] = at
		}
	}
	s.mu.Unlock()
	for ns, at := range latest {
		if s.clock.Now().Sub(at) > 90*24*time.Hour {
			report.Orphans = append(report.Orphans, ns)
		}
	}
	sort.Strings(report.Orphans)
	for _, root := range []string{s.paths.Config, s.paths.State} {
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&os.ModeSymlink != 0 {
				report.Problems = append(report.Problems, Problem{Code: "unsafe_path", File: path, Message: "symlink not followed"})
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if _, quarantined := quarantineOriginal(d.Name()); quarantined {
				report.Quarantined = append(report.Quarantined, path)
				return nil
			}
			if root == s.paths.Config && strings.HasSuffix(d.Name(), ".json") {
				data, _, err := readFile(path, MaxDocBytes, false)
				if err == nil {
					_, err = ParseDoc(data)
				}
				if err != nil {
					report.Problems = append(report.Problems, Problem{Code: "invalid_document", File: path, Message: err.Error()})
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return report, err
		}
	}
	return report, nil
}
