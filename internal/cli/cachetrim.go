package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
)

// keptSnapshots is how many snapshots a cache keeps besides the step's own: the next plan's base is
// most often a head planned just before.
const keptSnapshots = 4

// The work trees are checked out afresh from a trigger path the CI system changes per build.
var alwaysDropped = []string{workTree, rebuildDir}

// workTree must match the directory internal/affected uses.
const workTree = "tree"

// overBudget is in drop order, cheapest to get back first; Bazel's output is last, as a plan without it
// starts cold.
var overBudget = []string{
	"answers", "plugins", "caches/gocache", "caches/npm", "caches/gomodcache", "caches",
	"store", bazelDir + "/bazelisk", bazelDir + "/base", bazelDir,
}

type trimmed struct {
	kept      map[string]int64
	dropped   map[string]int64
	snapshots int
	busy      bool
}

// trimCache keeps a step's cache within budget bytes (0 for no cap); a cache another plan holds is left as it is.
func trimCache(cache string, budget int64, start time.Time) (*trimmed, error) {
	t := &trimmed{kept: map[string]int64{}, dropped: map[string]int64{}}

	if _, err := os.Stat(cache); errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}

	unlock, ok, err := tryLockCache(cache)
	if err != nil || !ok {
		t.busy = !ok

		return t, err
	}
	defer unlock()

	claims, err := filepath.Glob(filepath.Join(cache, "claims-*"))
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}

	for _, p := range append(underCache(cache, alwaysDropped), claims...) {
		if err := t.drop(cache, p); err != nil {
			return nil, err
		}
	}

	if err := t.dropExtracted(cache); err != nil {
		return nil, err
	}

	if err := t.dropSnapshots(cache, start); err != nil {
		return nil, err
	}

	total, err := t.measure(cache)
	if err != nil {
		return nil, err
	}

	for _, part := range overBudget {
		if budget <= 0 || total <= budget {
			break
		}

		p := filepath.Join(cache, filepath.FromSlash(part))

		freed := int64(0)
		if part == "answers" {
			freed, err = t.dropOldest(cache, p, total-budget)
		} else if _, serr := os.Stat(p); serr == nil {
			freed = sizeOf(p)
			err = t.drop(cache, p)
		}

		if err != nil {
			return nil, err
		}

		total -= freed
	}

	_, err = t.measure(cache)

	return t, err
}

func underCache(dir string, parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, filepath.Join(dir, filepath.FromSlash(p)))
	}

	return out
}

func tryLockCache(cache string) (func(), bool, error) {
	f, err := os.OpenFile(filepath.Join(cache, "lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // pipelinegen's own cache
	if err != nil {
		return nil, false, fmt.Errorf("cache lock: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close() //nolint:errcheck // not taken

		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf("cache lock: %w", err)
	}

	return func() { f.Close() }, true, nil //nolint:errcheck // closing frees the lock
}

func (t *trimmed) drop(cache, p string) error {
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	t.dropped[partOf(cache, p)] += sizeOf(p)

	return removeAll(p)
}

const (
	moduleSources    = "gomodcache sources"
	bazelRepoContent = "bazel repository contents"
)

// A CI system copies a step's cache file by file, and extracted copies are most of its files. Go and
// Bazel extract them again from the kept archives without the network.
func (t *trimmed) dropExtracted(cache string) error {
	if err := t.dropModuleSources(filepath.Join(cache, "caches", "gomodcache")); err != nil {
		return err
	}

	repos, err := filepath.Glob(filepath.Join(cache, bazelDir, "root", "cache", "repos", "*", "contents"))
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}

	for _, contents := range repos {
		if _, err := os.Stat(filepath.Join(filepath.Dir(contents), "content_addressable")); err != nil {
			continue
		}

		n := sizeOf(contents)
		if err := removeAll(contents); err != nil {
			return err
		}

		t.dropped[bazelRepoContent] += n
	}

	return nil
}

func (t *trimmed) dropModuleSources(modcache string) error {
	download := filepath.Join(modcache, "cache", "download")
	if _, err := os.Stat(download); err != nil {
		return nil //nolint:nilerr // no module cache, or none Go filled: nothing to drop
	}

	var dirs []string

	err := filepath.WalkDir(modcache, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == modcache {
			return nil //nolint:nilerr // a part that vanished frees nothing
		}

		rel, _ := filepath.Rel(modcache, p) //nolint:errcheck // p is under modcache
		if rel == "cache" {
			return filepath.SkipDir
		}

		path, version, module := strings.Cut(filepath.ToSlash(rel), "@")
		if !module {
			dirs = append(dirs, p)

			return nil
		}

		if _, err := os.Stat(filepath.Join(download, filepath.FromSlash(path), "@v", version+".zip")); err == nil {
			n := sizeOf(p)
			if err := removeAll(p); err != nil {
				return err
			}

			t.dropped[moduleSources] += n
		}

		return filepath.SkipDir
	})
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}

	// the deepest first, so a parent left empty by its children goes too.
	slices.Reverse(dirs)

	for _, d := range dirs {
		_ = os.Remove(d) //nolint:errcheck // only an empty one goes
	}

	return nil
}

func (t *trimmed) dropSnapshots(cache string, start time.Time) error {
	dir := filepath.Join(cache, "snapshots")

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}

	type snap struct {
		key  string
		when time.Time
	}

	var snaps []snap

	for _, e := range entries {
		key, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}

		if info, err := e.Info(); err == nil {
			snaps = append(snaps, snap{key, info.ModTime()})
		}
	}

	sort.Slice(snaps, func(i, j int) bool { return snaps[i].when.After(snaps[j].when) })

	keep := map[string]bool{}
	others := 0

	for _, s := range snaps {
		switch {
		case !s.when.Before(start):
			keep[s.key] = true
		case others < keptSnapshots:
			keep[s.key] = true
			others++
		}
	}

	for _, e := range entries {
		name := e.Name()
		key := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".tmp"), ".json"), ".hashes"), ".edges")

		if keep[key] && !strings.HasSuffix(name, ".tmp") {
			continue
		}

		if strings.HasSuffix(name, ".json") {
			t.snapshots++
		}

		if err := t.drop(cache, filepath.Join(dir, name)); err != nil {
			return err
		}
	}

	return nil
}

func (t *trimmed) dropOldest(cache, dir string, want int64) (int64, error) {
	type file struct {
		path string
		size int64
		when time.Time
	}

	var files []file

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a part that vanished frees nothing
		}

		if info, ierr := d.Info(); ierr == nil && info.Mode().IsRegular() {
			files = append(files, file{p, diskSize(info), info.ModTime()})
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("cache: %w", err)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].when.Before(files[j].when) })

	freed := int64(0)

	for _, f := range files {
		if freed >= want {
			break
		}

		if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return freed, fmt.Errorf("cache: %w", err)
		}

		t.dropped[partOf(cache, dir)] += f.size
		freed += f.size
	}

	return freed, nil
}

func (t *trimmed) measure(cache string) (int64, error) {
	t.kept = map[string]int64{}

	entries, err := os.ReadDir(cache)
	if err != nil {
		return 0, fmt.Errorf("cache: %w", err)
	}

	total := int64(0)

	for _, e := range entries {
		n := sizeOf(filepath.Join(cache, e.Name()))
		t.kept[partOf(cache, filepath.Join(cache, e.Name()))] += n
		total += n
	}

	return total, nil
}

func partOf(cache, p string) string {
	rel, err := filepath.Rel(cache, p)
	if err != nil {
		return p
	}

	part, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	if strings.HasPrefix(part, "claims-") {
		return "claims"
	}

	return part
}

func sizeOf(p string) int64 {
	total := int64(0)

	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error { //nolint:errcheck // a part that vanished counts nothing
		if err != nil {
			return nil //nolint:nilerr // as above
		}

		if info, ierr := d.Info(); ierr == nil {
			total += diskSize(info)
		}

		return nil
	})

	return total
}

func diskSize(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}

	return info.Size()
}

// removeAll handles read-only files, as Go's module cache and Bazel's external repositories make them.
func removeAll(p string) error {
	if err := os.RemoveAll(p); err == nil {
		return nil
	}

	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error { //nolint:errcheck // the removal below says what failed
		if err == nil && d.IsDir() {
			_ = os.Chmod(q, 0o700) //nolint:errcheck,gosec // our own cache's directories
		}

		return nil
	})

	if err := os.RemoveAll(p); err != nil {
		return fmt.Errorf("cache: %w", err)
	}

	return nil
}

func (t *trimmed) say(w io.Writer, cache string) {
	if t.busy {
		warnf(w, "pipelinegen: cache %s: another plan holds it, not trimmed\n", cache)

		return
	}

	total := int64(0)
	for _, n := range t.kept {
		total += n
	}

	line := fmt.Sprintf("pipelinegen: cache: kept %s (%s)", gb(total), parts(t.kept))

	if len(t.dropped) > 0 {
		line += "; dropped " + parts(t.dropped)

		if t.snapshots > 0 {
			line += fmt.Sprintf(", %d snapshots among them", t.snapshots)
		}
	}

	warnf(w, "%s\n", line)
}

func parts(m map[string]int64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if m[k] > 0 {
			keys = append(keys, k)
		}
	}

	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] || m[keys[i]] == m[keys[j]] && keys[i] < keys[j] })

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+" "+gb(m[k]))
	}

	if len(out) == 0 {
		return "nothing"
	}

	return strings.Join(out, ", ")
}

func gb(n int64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d KB", n>>10)
	}

	if n < 1<<30 {
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}

	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

// The user's own cache, which every repository on the machine shares, is never trimmed here.
func trimAfter(cfg *config.Config, cache string, start time.Time) {
	if cache == "" || cache == defaultCache() {
		return
	}

	t, err := trimCache(cache, cfg.CacheBudget(), start)
	if err != nil {
		warnf(os.Stderr, "pipelinegen: cache %s: not trimmed: %v\n", cache, err)

		return
	}

	t.say(os.Stderr, cache)
}
