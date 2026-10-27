package kit

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// fills checks what describe's fill declares: each the core can run, and,
// over the fixture, that a second run changes nothing the first left in the
// cache.
func (k *kit) fills(ctx context.Context) {
	if len(k.desc.Fill) == 0 {
		return
	}

	var problems []string

	for _, cache := range sortedFills(k.desc.Fill) {
		f := k.desc.Fill[cache]

		if p := plugins.FillProblem(cache, f); p != "" {
			problems = append(problems, p)

			continue
		}

		if !slices.Contains(k.desc.Caches, cache) {
			problems = append(problems, fmt.Sprintf("fill of %s: a cache describe's caches don't declare", cache))
		}

		for _, name := range f.Files {
			if !slices.ContainsFunc(k.desc.Candidates, func(g string) bool { return config.MatchGlob(g, name) || config.MatchGlob(g, "a/"+name) }) {
				problems = append(problems, fmt.Sprintf("fill of %s reads %s, which no candidate names", cache, name))
			}
		}
	}

	k.check("fill is declared as the core runs it", strings.Join(problems, "; "))

	if len(problems) > 0 {
		return
	}

	if k.o.Tree == "" {
		k.check("a second fill changes nothing", "no --tree, so no fill ran")

		return
	}

	for _, cache := range sortedFills(k.desc.Fill) {
		k.check("a second fill of "+cache+" changes nothing", k.fillTwice(ctx, cache))
	}
}

func (k *kit) fillTwice(ctx context.Context, cache string) string {
	f := k.desc.Fill[cache]
	target := filepath.Join(k.o.Work, "fill", cache)

	// a module cache is read-only by design: without making it writable
	// first, the kit's work directory couldn't be removed after
	defer removeTree(target)

	var dirs []string

	_ = filepath.WalkDir(k.o.Tree, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // an unreadable fixture shows as no directory
		if err == nil && d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}

		if err == nil && !d.IsDir() && d.Name() == f.Files[0] {
			dirs = append(dirs, filepath.Dir(p))
		}

		return nil
	})

	if len(dirs) == 0 {
		return fmt.Sprintf("the fixture holds no %s, so the fill never ran", f.Files[0])
	}

	var first map[string]string

	for round := range 2 {
		for _, dir := range dirs {
			if out, err := plugins.RunFill(ctx, dir, target, cache, f); err != nil {
				return fmt.Sprintf("`%s` in %s: %v: %s", strings.Join(f.Run, " "), dir, err, strings.TrimSpace(string(out)))
			}
		}

		now := snapshot(target)
		if round == 0 {
			first = now

			continue
		}

		var changed []string

		for p, v := range now {
			if first[p] != v {
				changed = append(changed, p)
			}
		}

		for p := range first {
			if _, ok := now[p]; !ok {
				changed = append(changed, p)
			}
		}

		sort.Strings(changed)

		return ifNot(len(changed) == 0, "the second run changed "+strings.Join(firstFew(changed), ", "))
	}

	return ""
}

func snapshot(dir string) map[string]string {
	out := map[string]string{}

	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // the walk itself returns nothing to act on
		if err == nil && !d.IsDir() {
			note(out, dir, p)
		}

		return nil
	})

	return out
}

func note(out map[string]string, dir, p string) {
	info, err := os.Lstat(p)
	if err != nil {
		return
	}

	rel, _ := filepath.Rel(dir, p) //nolint:errcheck // p is under dir
	out[rel] = fmt.Sprintf("%d %v", info.Size(), info.Mode())
}

func removeTree(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error { //nolint:errcheck // whatever stays is the temporary directory's
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700) //nolint:errcheck,gosec // the kit's own scratch directory, removed next
		}

		return nil
	})

	_ = os.RemoveAll(dir) //nolint:errcheck // whatever stays is the temporary directory's
}

func sortedFills[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}
