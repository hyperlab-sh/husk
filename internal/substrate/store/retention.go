package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

const quietWindow = 500 * time.Millisecond
const maxDelay = 5 * time.Second
const gcInterval = time.Hour

func storageID(k Key, namespace string) string {
	return string(k.Scope) + "/" + k.Instance + "/" + namespace
}
func (s *Store) sliceKey(spec state.Spec, file string) Key {
	k := Key{Scope: spec.Scope}
	if spec.Scope != state.ScopeUser {
		k.Instance = s.opts.Instance.ID
	}
	if spec.Scope != state.ScopeWorkspace {
		k.Path = []string{"slices"}
	}
	k.Path = append(k.Path, spec.Namespace)
	if file != "" {
		k.Path = append(k.Path, file)
	}
	return k
}
func recordNamespace(k Key) (string, bool) {
	p := k.Path
	if k.Scope != state.ScopeWorkspace {
		if len(p) != 3 || p[0] != "slices" {
			return "", false
		}
		p = p[1:]
	} else if len(p) != 2 {
		return "", false
	}
	return p[0], state.ValidNamespace(p[0]) && (p[1] == "current.json" || strings.HasPrefix(p[1], "gen-") && strings.HasSuffix(p[1], ".json"))
}
func (s *Store) prefixes(all bool) ([]Key, error) {
	out := []Key{{Scope: state.ScopeUser, Path: []string{"slices"}}}
	names := []string{}
	if all {
		p := filepath.Join(s.paths.State, "instances")
		if err := privateExistingDir(p); err == nil {
			entries, err := os.ReadDir(p)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if e.IsDir() && validName(e.Name()) {
					names = append(names, e.Name())
				}
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	} else if s.opts.Instance.Named {
		names = append(names, s.opts.Instance.ID)
	}
	for _, name := range names {
		out = append(out, Key{Scope: state.ScopeInstance, Instance: name, Path: []string{"slices"}}, Key{Scope: state.ScopeWorkspace, Instance: name})
	}
	return out, nil
}
func (s *Store) entries(all bool) ([]Entry, error) {
	prefixes, err := s.prefixes(all)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, p := range prefixes {
		entries, err := s.source.List(context.Background(), p)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
	}
	return out, nil
}
func parseEnvelope(data []byte, k Key) (envelope, error) {
	var e envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return e, err
	}
	ns, ok := recordNamespace(k)
	if !ok || e.Version < 1 || e.Namespace != ns || e.Scope != k.Scope || e.Instance != k.Instance {
		return e, fmt.Errorf("store: invalid slice envelope")
	}
	if e.Version > 1 {
		return e, nil
	}
	v := bytes.TrimSpace(e.Value)
	if len(v) == 0 || v[0] != '{' || !json.Valid(v) {
		return e, fmt.Errorf("store: slice is not a JSON object")
	}
	return e, nil
}
func (s *Store) preload() error {
	entries, err := s.entries(false)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		ns, ok := recordNamespace(entry.Key)
		if !ok {
			continue
		}
		id := storageID(entry.Key, ns)
		data, _, readErr := s.source.Get(context.Background(), entry.Key)
		env, parseErr := parseEnvelope(data, entry.Key)
		if readErr != nil {
			parseErr = readErr
		}
		if parseErr != nil {
			if err = s.quarantine(entry, ns, parseErr); err != nil {
				s.blocked[id] = true
			}
			continue
		}
		if env.Version > 1 {
			s.blocked[id] = true
			s.diagnostic(Problem{Code: "newer_store", Path: ns, Message: "written by a newer Husk; persistence disabled this run"}, "")
			continue
		}
		s.cacheRecord(id, savedRecord{env: env, key: entry.Key, modified: entry.Modified})
		if parts := strings.Split(entry.Key.Path[len(entry.Key.Path)-1], "-"); len(parts) > 2 && parts[0] == "gen" {
			nanos, _ := strconv.ParseInt(parts[1], 10, 64)
			s.lastGeneration = max(s.lastGeneration, nanos)
		}
	}
	return nil
}

// cacheRecord retains only the latest current and generation values, not N full
// history documents per slice. Caller holds mu or runs before the worker starts.
func (s *Store) cacheRecord(id string, record savedRecord) {
	isGeneration := strings.HasPrefix(record.key.Path[len(record.key.Path)-1], "gen-")
	for i, old := range s.loaded[id] {
		if strings.HasPrefix(old.key.Path[len(old.key.Path)-1], "gen-") != isGeneration {
			continue
		}
		if strings.Join(record.key.Path, "/") == strings.Join(old.key.Path, "/") || record.modified.After(old.modified) || record.modified.Equal(old.modified) && strings.Join(record.key.Path, "/") > strings.Join(old.key.Path, "/") {
			s.loaded[id][i] = record
		}
		return
	}
	s.loaded[id] = append(s.loaded[id], record)
}
func (s *Store) quarantine(entry Entry, namespace string, cause error) error {
	p, err := s.source.path(entry.Key, false)
	if err != nil {
		return err
	}
	target := p + ".corrupt-" + s.clock.Now().UTC().Format("20060102T150405.000000000Z")
	// Never replace evidence from an earlier failure at the same injected time.
	for n := 0; ; n++ {
		if _, err = os.Lstat(target); errors.Is(err, fs.ErrNotExist) {
			break
		}
		target = p + fmt.Sprintf(".corrupt-%s-%d", s.clock.Now().UTC().Format("20060102T150405.000000000Z"), n)
	}
	if err = os.Rename(p, target); err != nil {
		s.diagnostic(Problem{Code: "quarantine_failed", Path: namespace, Message: err.Error()}, "")
		return err
	}
	s.diagnostic(Problem{Code: "slice_reset", Path: namespace, Message: "Slice reset: " + namespace + ": " + cause.Error()}, target)
	return nil
}

// Hydrate is a loop-only, non-I/O lookup of values preloaded by Open. The
// registration's current policy, including a newly changed TTL, governs reuse.
func (s *Store) Hydrate(spec state.Spec) (json.RawMessage, bool) {
	if !spec.Persistent || !state.ValidNamespace(spec.Namespace) || spec.Scope != state.ScopeUser && !s.opts.Instance.Named {
		return nil, false
	}
	k := s.sliceKey(spec, "")
	id := storageID(k, spec.Namespace)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies[id] = spec
	if s.blocked[id] {
		return nil, false
	}
	for _, record := range s.loaded[id] {
		file := record.key.Path[len(record.key.Path)-1]
		if spec.Retention.Policy == state.KeepLastN && !strings.HasPrefix(file, "gen-") || spec.Retention.Policy != state.KeepLastN && file != "current.json" {
			continue
		}
		if spec.Retention.Policy == state.TTL && !s.clock.Now().Before(record.modified.Add(spec.Retention.TTL)) {
			return nil, false
		}
		return bytes.Clone(record.env.Value), true
	}
	return nil, false
}
func (s *Store) onFlush(changes []state.Change, snap *state.Snapshot) {
	now := s.clock.Now()
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	for _, change := range changes {
		if change.Removed {
			delete(s.dirty, change.Namespace)
			delete(s.registered, change.Namespace)
			continue
		}
		entry, ok := snap.Slices[change.Namespace]
		if !ok || !entry.Persistent || entry.Scope != state.ScopeUser && !s.opts.Instance.Named {
			continue
		}
		k := s.sliceKey(entry.Spec, "")
		id := storageID(k, entry.Namespace)
		s.policies[id] = entry.Spec
		if _, exists := s.registered[entry.Namespace]; !exists {
			s.registered[entry.Namespace] = now
		}
		if s.blocked[id] {
			continue
		}
		pending, exists := s.dirty[entry.Namespace]
		if !exists {
			pending.first = now
		}
		pending.last = now
		pending.entry = entry
		pending.epoch = s.epochs[entry.Namespace]
		pending.failed = false
		s.dirty[entry.Namespace] = pending
	}
	s.mu.Unlock()
	s.signal()
}
func deadline(p pendingWrite) time.Time {
	quiet := p.last.Add(quietWindow)
	maximum := p.first.Add(maxDelay)
	if quiet.Before(maximum) {
		return quiet
	}
	return maximum
}
func (s *Store) nextDelay() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := gcInterval
	now := s.clock.Now()
	for _, p := range s.dirty {
		if p.failed {
			continue
		}
		delay := deadline(p).Sub(now)
		if delay < d {
			d = delay
		}
	}
	return max(d, 0)
}
func (s *Store) run() {
	defer close(s.done)
	gcTimer := s.clock.NewTimer(gcInterval)
	defer func() { gcTimer.Stop() }()
	_, _ = s.GC(context.Background())
	for {
		timer := s.clock.NewTimer(s.nextDelay())
		select {
		case <-s.stop:
			timer.Stop()
			s.mu.Lock()
			ctx := s.closeCtx
			s.mu.Unlock()
			err := s.flushDue(ctx, true)
			if s.opts.Instance.Named && ctx.Err() == nil {
				err = errors.Join(err, s.writeMeta(true))
			}
			s.mu.Lock()
			s.closeErr = err
			s.mu.Unlock()
			return
		case req := <-s.barriers:
			timer.Stop()
			req.done <- s.flushDue(req.ctx, req.force)
		case <-s.wake:
			timer.Stop()
			_ = s.flushDue(context.Background(), false)
		case <-timer.C():
			_ = s.flushDue(context.Background(), false)
		case <-gcTimer.C():
			timer.Stop()
			_, _ = s.GC(context.Background())
			gcTimer = s.clock.NewTimer(gcInterval)
		}
	}
}
func (s *Store) flushDue(ctx context.Context, force bool) error {
	s.mu.Lock()
	work := map[string]pendingWrite{}
	now := s.clock.Now()
	workspaceDue := false
	for ns, p := range s.dirty {
		if force || !p.failed && !now.Before(deadline(p)) {
			work[ns] = p
			if p.entry.Scope == state.ScopeWorkspace {
				workspaceDue = true
			}
		}
	}
	// A workspace flush uses one snapshot for all registered persistent workspace
	// entries. Each file is atomic; the batch is intentionally not a transaction.
	if workspaceDue && s.st != nil {
		snap := s.st.Snapshot()
		for ns, entry := range snap.Slices {
			if entry.Persistent && entry.Scope == state.ScopeWorkspace {
				work[ns] = pendingWrite{entry: entry, epoch: s.epochs[ns], first: now, last: now}
			}
		}
	}
	s.mu.Unlock()
	keys := make([]string, 0, len(work))
	for ns := range work {
		keys = append(keys, ns)
	}
	sort.Strings(keys)
	var errs []error
	for _, ns := range keys {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("store: unflushed %s: %w", ns, err))
			continue
		}
		p := work[ns]
		s.ioMu.Lock()
		s.mu.Lock()
		st := s.st
		stale := s.epochs[ns] != p.epoch
		id := storageID(s.sliceKey(p.entry.Spec, ""), ns)
		blocked := s.blocked[id]
		registeredAt := s.registered[ns]
		s.mu.Unlock()
		if st != nil {
			entry, exists := st.Snapshot().Slices[ns]
			if !exists || !entry.Persistent || entry.Scope != p.entry.Scope {
				stale = true
			}
		}
		var err error
		if !stale && !blocked {
			err = s.persist(ctx, p.entry, registeredAt)
		}
		s.ioMu.Unlock()
		s.mu.Lock()
		current, exists := s.dirty[ns]
		if err == nil && exists && current.entry.Revision == p.entry.Revision && current.epoch == p.epoch {
			delete(s.dirty, ns)
		} else if err != nil && exists {
			current.failed = true
			s.dirty[ns] = current
		}
		s.mu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("store: unflushed %s: %w", ns, err))
			s.diagnostic(Problem{Code: "persist_failed", Path: ns, Message: err.Error()}, "")
		}
	}
	return errors.Join(errs...)
}
func (s *Store) persist(ctx context.Context, entry state.Entry, registered time.Time) error {
	if !entry.Persistent {
		return nil
	}
	if len(entry.Value) > entry.MaxBytes || len(entry.Value) > state.HardMaxBytes {
		return state.ErrTooLarge
	}
	now := s.clock.Now()
	env := envelope{Version: 1, Namespace: entry.Namespace, Scope: entry.Scope, WrittenAt: now, RegisteredAt: registered, Retention: retentionMetadata(entry.Retention), Value: entry.Value}
	if entry.Scope != state.ScopeUser {
		env.Instance = s.opts.Instance.ID
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	filename := "current.json"
	modified := now
	if entry.Retention.Policy == state.KeepLastN {
		// A frozen or backwards clock must not overwrite a generation or make
		// its newest value sort before older files. Preload seeds this counter.
		s.lastGeneration = max(now.UnixNano(), s.lastGeneration+1)
		modified = time.Unix(0, s.lastGeneration)
		filename = fmt.Sprintf("gen-%020d-%s.json", s.lastGeneration, s.opts.Instance.ID)
	}
	k := s.sliceKey(entry.Spec, filename)
	if err = s.source.Put(ctx, k, b); err != nil {
		return err
	}
	path, err := s.source.path(k, false)
	if err != nil {
		return err
	}
	if err = os.Chtimes(path, modified, modified); err != nil {
		return err
	}
	s.mu.Lock()
	id := storageID(k, entry.Namespace)
	saved := savedRecord{env: env, key: k, modified: modified}
	s.cacheRecord(id, saved)
	s.mu.Unlock()
	// Bound generations immediately as well as during periodic GC.
	if entry.Retention.Policy == state.KeepLastN {
		_, err = s.collectSlice(ctx, entry.Spec)
	}
	return err
}

// ClearSlice deletes every persisted scope's copies and pending pre-clear writes.
// Call it off the loop, then post State.Clear. Epochs prevent a queued old write
// from resurrecting the deleted value after the delete finishes.
func (s *Store) ClearSlice(namespace string) error {
	if !state.ValidNamespace(namespace) {
		return ErrInvalidPath
	}
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	s.mu.Lock()
	s.epochs[namespace]++
	delete(s.dirty, namespace)
	for id, records := range s.loaded {
		if len(records) > 0 && records[0].env.Namespace == namespace {
			delete(s.loaded, id)
		}
	}
	for id := range s.blocked {
		if strings.HasSuffix(id, "/"+namespace) {
			delete(s.blocked, id)
		}
	}
	s.mu.Unlock()
	prefixes, err := s.prefixes(true)
	if err != nil {
		return err
	}
	for _, prefix := range prefixes {
		prefix.Path = append(prefix.Path, namespace)
		p, err := s.source.path(prefix, false)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info, err := os.Lstat(p); err == nil {
			if !info.IsDir() {
				return ErrInvalidPath
			}
			if err = os.RemoveAll(p); err != nil {
				return err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// RemovePlugin removes only the plugin's State namespace in all instance scopes.
func (s *Store) RemovePlugin(pluginID string) error {
	if !validID(pluginID) {
		return ErrInvalidPath
	}
	return s.ClearSlice(pluginID + ".state")
}
