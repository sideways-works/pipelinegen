// Package pluginstore keeps pipelinegen's records in a store plugin: each of the store's calls is
// one call of the plugin (docs/writing-plugins/store.md).
package pluginstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/plugin"
)

// ErrStore is an answer of the plugin's the core can't use.
var ErrStore = errors.New("store plugin")

// Records are a store plugin's records.
type Records struct {
	Plugin plugin.Plugin
	// Resolve finds the plugin's program on the first call when Plugin has no path: it may have to
	// be fetched, which a store opened without a context can't do.
	Resolve func(ctx context.Context) (string, error)
	// Work is where each call's request may write; empty, a directory of its own.
	Work  string
	Clock func() time.Time

	work string
}

var _ gitstore.Records = (*Records)(nil)

func (r *Records) workDir() (string, error) {
	if r.Work != "" {
		return r.Work, nil
	}

	if r.work == "" {
		dir, err := os.MkdirTemp("", "pipelinegen-store-")
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrStore, err)
		}

		r.work = dir
	}

	return r.work, nil
}

func (r *Records) call(ctx context.Context, call string, req func(plugin.Request) any, answer any) error {
	work, err := r.workDir()
	if err != nil {
		return err
	}

	if r.Plugin.Path == "" && r.Resolve != nil {
		path, err := r.Resolve(ctx)
		if err != nil {
			return fmt.Errorf("%w %s: %w", ErrStore, r.Plugin.Name, err)
		}

		r.Plugin.Path = path
	}

	if err := r.Plugin.CallStore(ctx, call, req(r.Plugin.NewRequest(work)), answer); err != nil {
		return fmt.Errorf("%w %s: %s: %w", ErrStore, r.Plugin.Name, call, err)
	}

	return nil
}

func (r *Records) List(ctx context.Context, prefixes []string) (map[string]gitstore.Ref, error) {
	var ans plugin.StoreListAnswer

	err := r.call(ctx, plugin.CallStoreList, func(q plugin.Request) any {
		return plugin.StoreListRequest{Request: q, Prefixes: prefixes}
	}, &ans)
	if err != nil {
		return nil, err
	}

	out := make(map[string]gitstore.Ref, len(ans.Records))

	for _, rec := range ans.Records {
		if !under(rec.Name, prefixes) {
			return nil, fmt.Errorf("%w %s: listed %q, under none of the prefixes asked", ErrStore, r.Plugin.Name, rec.Name)
		}

		out[rec.Name] = ref(rec)
	}

	return out, nil
}

func under(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}

	return false
}

func ref(rec plugin.StoreRecord) gitstore.Ref {
	return gitstore.Ref{Name: rec.Name, Commit: rec.Version, Time: rec.Time.UTC()}
}

func (r *Records) First(ctx context.Context, names []string) (gitstore.Ref, bool, error) {
	var ans plugin.StoreGetAnswer

	err := r.call(ctx, plugin.CallStoreGet, func(q plugin.Request) any {
		return plugin.StoreGetRequest{Request: q, Names: names}
	}, &ans)
	if err != nil || ans.Record == nil {
		return gitstore.Ref{}, false, err
	}

	return ref(*ans.Record), true, nil
}

func (r *Records) Read(ctx context.Context, versions, paths []string) (map[string]map[string][]byte, error) {
	if len(versions) == 0 {
		return map[string]map[string][]byte{}, nil
	}

	var ans plugin.StoreReadAnswer

	err := r.call(ctx, plugin.CallStoreRead, func(q plugin.Request) any {
		return plugin.StoreReadRequest{Request: q, Versions: versions, Paths: paths}
	}, &ans)
	if err != nil {
		return nil, err
	}

	if ans.Files == nil {
		ans.Files = map[string]map[string][]byte{}
	}

	return ans.Files, nil
}

func (r *Records) Write(ctx context.Context, message string, updates []gitstore.Update) (map[string]string, error) {
	ups := make([]plugin.StoreUpdate, 0, len(updates))
	for _, u := range updates {
		ups = append(ups, plugin.StoreUpdate{Name: u.Ref, Expect: u.Old, Any: u.Any, Delete: u.Delete, Files: u.Files})
	}

	now := time.Now
	if r.Clock != nil {
		now = r.Clock
	}

	var ans plugin.StoreWriteAnswer

	err := r.call(ctx, plugin.CallStoreWrite, func(q plugin.Request) any {
		return plugin.StoreWriteRequest{Request: q, Message: message, Updates: ups, Time: now().UTC()}
	}, &ans)
	if err != nil {
		return nil, err
	}

	if len(ans.Stale) > 0 {
		return ans.Versions, fmt.Errorf("%w: %s", gitstore.ErrStale, strings.Join(ans.Stale, ", "))
	}

	return ans.Versions, nil
}

// Close removes the calls' own work directory.
func (r *Records) Close() error {
	if r.work == "" {
		return nil
	}

	if err := os.RemoveAll(r.work); err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}

	return nil
}
