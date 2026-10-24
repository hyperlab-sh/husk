package pods

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// addition is loop-owned until done closes, after which its outcome is immutable.
type addition struct {
	done   chan struct{}
	result contracts.PluginAddition
	err    error
}

type addCandidate struct {
	path     string
	spec     *manifest.Spec
	warnings []manifest.Warning
	refusal  *contracts.PluginAddRefusal
}

func emptyAddition() contracts.PluginAddition {
	return contracts.PluginAddition{Added: []contracts.PluginAdded{}, Refused: []contracts.PluginAddRefusal{}, Reports: []contracts.ReloadReport{}}
}

func addError(code protocol.Code, reason, message string) error {
	return contracts.NewError(code, message, map[string]any{"reason": reason})
}

// Add serves plugin.add. Conversion runs on the calling handler goroutine;
// admission and gate completion are loop-confined. Once admitted, a batch
// belongs to the Host, not to the request context.
func (h *Host) Add(ctx context.Context, c contracts.Caller, source contracts.PluginSource) (contracts.PluginAddition, error) {
	if c.Kind != contracts.CallerCLI && c.Kind != contracts.CallerHost {
		return contracts.PluginAddition{}, contracts.NewError(protocol.CodeDenied, "only the user's CLI or the host may add plugins", nil)
	}
	if source.Path != "" && source.Dir != "" {
		return contracts.PluginAddition{}, addError(protocol.CodeInvalidParams, "path_and_dir", "give path or dir, not both")
	}
	path := source.Path
	if path == "" {
		path = source.Dir
	}
	if path == "" {
		return contracts.PluginAddition{}, addError(protocol.CodeInvalidParams, "missing_path", "path or dir is required")
	}
	if !filepath.IsAbs(path) {
		return contracts.PluginAddition{}, addError(protocol.CodeInvalidParams, "relative_path", "plugin path must be absolute")
	}
	if h.stopped.Load() {
		return contracts.PluginAddition{}, addError(protocol.CodeUnavailable, "shutdown", "husk is shutting down")
	}
	if h.opts.Converter == nil {
		return contracts.PluginAddition{}, contracts.NewError(protocol.CodeInternal, "no plugin converter configured", nil)
	}
	items, err := h.convertAddition(source)
	if err != nil {
		return contracts.PluginAddition{}, err
	}
	result := emptyAddition()
	survivors := false
	for _, item := range items {
		if item.refusal == nil {
			survivors = true
		}
	}
	if !survivors {
		for _, item := range items {
			result.Refused = append(result.Refused, *item.refusal)
		}
		return result, nil
	}
	var entry *addition
	var refusal error
	err = h.loop.Do(ctx, func() {
		if h.closing {
			refusal = addError(protocol.CodeUnavailable, "shutdown", "husk is shutting down")
			return
		}
		var ids []string
		warnings := map[string][]contracts.PluginWarning{}
		for i := range items {
			item := &items[i]
			if item.refusal != nil {
				continue
			}
			id := item.spec.ID
			reason, message := "", ""
			if h.Removing(id) {
				reason, message = "removing", "plugin "+id+" is being removed"
			} else if h.pods[id] != nil {
				reason, message = "duplicate_plugin_id", "plugin "+id+" is already loaded"
			}
			if reason == "" {
				if _, err := h.AddPod(*item.spec); err != nil {
					if contracts.CodeOf(err) != string(protocol.CodeConflict) {
						admitted := append([]string{}, ids...)
						slices.Sort(admitted)
						refusal = contracts.NewError(protocol.CodeInternal, "register plugin "+id+": "+err.Error(), map[string]any{"added": admitted})
						break
					}
					reason, message = "duplicate_plugin_id", err.Error()
				}
			}
			if reason != "" {
				if source.Path != "" {
					refusal = contracts.NewError(protocol.CodeConflict, message, map[string]any{"reason": reason, "plugin": id})
					return
				}
				item.refusal = &contracts.PluginAddRefusal{Path: item.path, Plugin: id, Code: reason, Message: message}
				continue
			}
			ids = append(ids, id)
			warnings[id] = []contracts.PluginWarning{}
			for _, w := range item.warnings {
				h.loop.Log(LogMsg{PluginID: id, Level: "warn", Message: fmt.Sprintf("manifest %s: %s", w.Field, w.Message)})
				warnings[id] = append(warnings[id], contracts.PluginWarning{Code: w.Code, Message: w.Message})
			}
			if h.opts.Workers != nil {
				h.opts.Workers.Added(id)
			}
		}
		for _, item := range items {
			if item.refusal != nil {
				result.Refused = append(result.Refused, *item.refusal)
			}
		}
		if len(ids) == 0 {
			return
		}
		gate, cmd := h.prepare(ids...)
		if cmd == nil {
			return
		}
		// A later registry failure must not strand earlier admitted Pods:
		// settle their gate before answering the batch with that error.
		entry = &addition{done: make(chan struct{}), result: result, err: refusal}
		slices.Sort(ids)
		for _, id := range ids {
			entry.result.Added = append(entry.result.Added, contracts.PluginAdded{Plugin: id, Warnings: warnings[id]})
		}
		h.adding[gate] = entry
		h.spawn(cmd)
	})
	if err != nil {
		if ctx.Err() != nil {
			return contracts.PluginAddition{}, ctx.Err()
		}
		return contracts.PluginAddition{}, contracts.NewError(protocol.CodeUnavailable, "the host loop no longer runs", nil)
	}
	if refusal != nil && entry == nil {
		return contracts.PluginAddition{}, refusal
	}
	if entry == nil {
		return result, nil
	}
	select {
	case <-entry.done:
	case <-ctx.Done():
		return contracts.PluginAddition{}, ctx.Err()
	}
	if ctx.Err() != nil {
		return contracts.PluginAddition{}, ctx.Err()
	}
	return entry.result, entry.err
}

func (h *Host) convertAddition(source contracts.PluginSource) ([]addCandidate, error) {
	var results []manifest.Result
	if source.Path != "" {
		if _, err := os.Stat(source.Path); err != nil {
			return nil, conversionError(err)
		}
		spec, warnings, err := h.opts.Converter.LoadPath(source.Path)
		if err != nil {
			return nil, addError(protocol.CodeFailed, "conversion", err.Error())
		}
		results = []manifest.Result{{Dir: source.Path, Spec: spec, Warnings: warnings}}
	} else {
		st, err := os.Stat(source.Dir)
		if err != nil {
			return nil, conversionError(err)
		}
		if !st.IsDir() {
			return nil, addError(protocol.CodeInvalidParams, "not_a_directory", "dir must name a directory")
		}
		results, err = h.opts.Converter.LoadDir(source.Dir)
		if err != nil {
			return nil, conversionError(err)
		}
	}
	seen := map[string]bool{}
	items := make([]addCandidate, 0, len(results))
	for _, res := range results {
		item := addCandidate{path: res.Dir, spec: res.Spec, warnings: res.Warnings}
		id := ""
		if res.Spec != nil {
			id = res.Spec.ID
		}
		err := res.Err
		if err == nil {
			if res.Spec == nil {
				err = errors.New("converter returned no plugin spec")
			} else {
				err = res.Spec.Validate()
			}
		}
		if err != nil {
			if source.Path != "" {
				return nil, addError(protocol.CodeFailed, "conversion", err.Error())
			}
			item.refusal = &contracts.PluginAddRefusal{Path: res.Dir, Plugin: id, Code: "conversion", Message: err.Error()}
		} else if seen[id] {
			item.refusal = &contracts.PluginAddRefusal{Path: res.Dir, Plugin: id, Code: "duplicate_plugin_id", Message: "plugin " + id + " appears twice in the directory"}
		} else {
			seen[id] = true
		}
		items = append(items, item)
	}
	return items, nil
}

func conversionError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return contracts.NewError(protocol.CodeNotFound, err.Error(), nil)
	}
	return addError(protocol.CodeFailed, "conversion", err.Error())
}

// finishAddition runs after every Wrapper in the gate started or failed.
func (h *Host) finishAddition(gate uint64, reports []contracts.ReloadReport) {
	entry := h.adding[gate]
	if entry == nil {
		return
	}
	for i := range entry.result.Added {
		added := &entry.result.Added[i]
		added.Status = "removed"
		if p := h.pods[added.Plugin]; p != nil {
			if p.info.Status != wrapper.Live && p.info.Status != wrapper.Failed {
				p.openingFailed = true
				h.Handle(LaunchFailedMsg{PluginID: added.Plugin, Err: openingFailure(p.info)})
			}
			added.Status, added.Log = string(p.info.Status), p.info.LogPath
			if h.opts.Publisher != nil {
				h.opts.Publisher.Publish(PluginAddedEvent, map[string]any{"plugin_id": added.Plugin, "status": added.Status})
			}
		}
	}
	entry.result.Reports = append([]contracts.ReloadReport{}, reports...)
	close(entry.done)
	delete(h.adding, gate)
}

func (h *Host) cancelAdditions() {
	for gate, entry := range h.adding {
		entry.result = contracts.PluginAddition{}
		entry.err = shutdownCancelled()
		close(entry.done)
		delete(h.adding, gate)
	}
}
