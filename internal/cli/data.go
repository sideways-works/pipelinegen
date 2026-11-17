package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/buildfile"
	"github.com/sideways-works/pipelinegen/internal/config"
)

// sliceData finds what each test file depends on besides what the plugins bind. These are explicit
// edges, so they stay whatever the lists say.
func sliceData(ctx context.Context, bz *bazel.Client, tree, name string, s config.Suite, files []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(files) == 0 {
		return out, nil
	}

	var (
		owners  = map[string][]string{} // owner label → its files
		unowned []string
	)

	for _, f := range files {
		if o, ok := s.OwnerOf(f); ok {
			// Bazel's short form //a/b names //a/b:b; the query answers in the long form.
			if canon, ok := buildfile.Canonical("", o); ok {
				o = canon
			}

			owners[bazel.NormalLabel(o)] = append(owners[bazel.NormalLabel(o)], f)
		} else {
			unowned = append(unowned, f)
		}
	}

	// An owner the template names but the tree lacks: fall back to the test rules taking the file as a source.
	if len(owners) > 0 {
		exist, err := bz.Existing(ctx, sortedKeys(owners))
		if err != nil {
			return nil, err
		}

		var missing []string

		for _, o := range sortedKeys(owners) {
			if !exist[o] {
				missing = append(missing, o)
				unowned = append(unowned, owners[o]...)
				delete(owners, o)
			}
		}

		if len(missing) > 0 {
			warnf(os.Stderr, "pipelinegen: suite %s: owner pattern %s names targets the tree lacks: %s\n", name, s.Owner, strings.Join(firstOf(missing, 5), ", "))
		}
	}

	// A query naming a file no target takes yet (before gazelle ran) fails whole; planning then sees it
	// changed with no owner and runs everything.
	unowned, err := inSomeTarget(ctx, bz, unowned)
	if err != nil {
		return nil, err
	}

	if err := ownerData(ctx, bz, owners, unowned, s.Bind == config.BindRecorded, out); err != nil {
		return nil, err
	}

	for _, f := range files {
		for _, ed := range s.ExtraDeps {
			if matchesAny(ed.Tests, f) {
				out[f] = append(out[f], labelsOnly(ed.Deps)...)
			}
		}

		out[f] = append(out[f], labelsOnly(commentData(filepath.Join(tree, filepath.FromSlash(f))))...)

		slices.Sort(out[f])
		out[f] = slices.Compact(out[f])

		if len(out[f]) == 0 {
			delete(out, f)
		}
	}

	return out, nil
}

func inSomeTarget(ctx context.Context, bz *bazel.Client, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	exist, err := bz.Existing(ctx, paths)
	if err != nil {
		return nil, err
	}

	known := map[string]bool{}

	for l := range exist {
		if p, ok := bazel.LabelPath(l); ok {
			known[p] = true
		}
	}

	return slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return !known[p] }), nil
}

func ownerData(ctx context.Context, bz *bazel.Client, owners map[string][]string, unowned []string, recorded bool, out map[string][]string) error {
	var parts []string

	if len(owners) > 0 {
		parts = append(parts, "set("+strings.Join(sortedKeys(owners), " ")+")")
	}

	if len(unowned) > 0 {
		parts = append(parts, "tests(rdeps(//..., set("+strings.Join(unowned, " ")+"), 1))")
	}

	if len(parts) == 0 {
		return nil
	}

	rules, err := bz.Rules(ctx, strings.Join(parts, " + "))
	if err != nil {
		return err
	}

	isUnowned := map[string]bool{}
	for _, f := range unowned {
		isUnowned[f] = true
	}

	for _, r := range rules {
		// A graph-bound suite takes its owner whole; a recorded one only the owner's data, its code coming
		// from the recording.
		edges := r.Data
		if !recorded {
			edges = []string{r.Label}
		}

		for _, f := range owners[r.Label] {
			out[f] = append(out[f], edges...)
		}

		for _, src := range r.Srcs {
			if p, ok := bazel.LabelPath(src); ok && isUnowned[p] {
				out[p] = append(out[p], edges...)
			}
		}
	}

	return nil
}

func sliceUnits(tree string, s config.Suite, files []string) map[string][]string {
	out := map[string][]string{}

	for _, f := range files {
		var ids []string

		for _, ed := range s.ExtraDeps {
			if matchesAny(ed.Tests, f) {
				ids = append(ids, unitsOnly(ed.Deps)...)
			}
		}

		ids = append(ids, unitsOnly(commentData(filepath.Join(tree, filepath.FromSlash(f))))...)

		if len(ids) > 0 {
			slices.Sort(ids)
			out[f] = slices.Compact(ids)
		}
	}

	return out
}

func unitsOnly(deps []string) []string {
	var out []string

	for _, d := range deps {
		if !strings.HasPrefix(d, "//") && !strings.HasPrefix(d, "@") && strings.Contains(d, ":") {
			out = append(out, d)
		}
	}

	return out
}

func commentData(file string) []string {
	f, err := os.Open(file) //nolint:gosec // a tracked file of the tree under test
	if err != nil {
		return nil
	}
	defer f.Close() //nolint:errcheck // read only

	return config.DataComments(f)
}

func labelsOnly(deps []string) []string {
	var out []string

	for _, d := range deps {
		if strings.HasPrefix(d, "//") || strings.HasPrefix(d, "@") {
			out = append(out, d)
		}
	}

	return out
}

func matchesAny(patterns []string, name string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return config.MatchGlob(p, name) })
}

func firstOf(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("and %d more", len(s)-n))
	}

	return s
}

// groupsVersion changes whenever what a cached opening of groups means does.
const groupsVersion = "1"

const groupsDir = "groups"

// groupOpener opens the filegroups of one tree's recorded slices' data, once per label, and keeps what
// it learnt by commit: a commit's groups never change.
type groupOpener struct {
	bz    *bazel.Client
	dir   string // "" keeps nothing
	key   string
	g     *bazel.Groups
	asked map[string]bool
}

func newGroupOpener(cfg *config.Config, bz *bazel.Client, cache, commit string) *groupOpener {
	o := &groupOpener{bz: bz, g: &bazel.Groups{Members: map[string][]string{}, As: map[string]string{}}, asked: map[string]bool{}}

	if cache == "" || commit == "" {
		return o
	}

	sum := sha256.Sum256([]byte(strings.Join(slices.Concat([]string{groupsVersion, cfg.Bazel.Workspace, cfg.Bazel.Bazelisk}, cfg.Bazel.StartupFlags), "\x00")))
	o.dir, o.key = filepath.Join(cache, groupsDir), commit+"-"+hex.EncodeToString(sum[:])[:12]

	return o
}

func (o *groupOpener) open(ctx context.Context, labels []string) (*bazel.Groups, error) {
	var todo []string

	for _, l := range labels {
		if c, ok := bazel.MainCanonical(l); ok && !o.asked[c] {
			o.asked[c] = true
			todo = append(todo, c)
		}
	}

	if len(todo) == 0 {
		return o.g, nil
	}

	slices.Sort(todo)

	got, err := o.cached(ctx, todo)
	if err != nil {
		return nil, err
	}

	maps.Copy(o.g.Members, got.Members)
	maps.Copy(o.g.As, got.As)

	return o.g, nil
}

func (o *groupOpener) cached(ctx context.Context, todo []string) (*bazel.Groups, error) {
	if o.dir == "" {
		return o.bz.OpenGroups(ctx, todo)
	}

	sum := sha256.Sum256([]byte(strings.Join(todo, "\n")))
	file := filepath.Join(o.dir, o.key+"-"+hex.EncodeToString(sum[:])[:16]+".json")

	if data, err := os.ReadFile(file); err == nil { //nolint:gosec // pipelinegen's own cache
		var g bazel.Groups
		if json.Unmarshal(data, &g) == nil && g.Members != nil {
			// the cache's sweep keeps what plans use
			now := time.Now()
			os.Chtimes(file, now, now) //nolint:errcheck // only the sweep's order depends on it

			return &g, nil
		}
	}

	g, err := o.bz.OpenGroups(ctx, todo)
	if err != nil {
		return nil, err
	}

	// a cache that can't be written costs only the queries next time
	if data, err := json.Marshal(g); err == nil && os.MkdirAll(o.dir, 0o750) == nil {
		if tmp, err := os.CreateTemp(o.dir, ".tmp-"); err == nil {
			_, werr := tmp.Write(data)
			cerr := tmp.Close()

			if werr != nil || cerr != nil || os.Rename(tmp.Name(), file) != nil {
				os.Remove(tmp.Name()) //nolint:errcheck // a scratch file
			}
		}
	}

	return g, nil
}

// openedData is each file's data with its filegroups opened, less the labels the file names in its own
// pipelinegen:data lines, which stay whole.
func openedData(ctx context.Context, o *groupOpener, data, declared map[string][]string) (map[string][]string, *bazel.Groups, error) {
	var starts []string

	for f, labels := range data {
		for _, l := range labels {
			if !slices.Contains(declared[f], l) {
				starts = append(starts, l)
			}
		}
	}

	g, err := o.open(ctx, starts)
	if err != nil {
		return nil, nil, err
	}

	out := make(map[string][]string, len(data))

	for f, labels := range data {
		var leaves []string

		for _, l := range labels {
			if slices.Contains(declared[f], l) || !g.IsGroup(l) {
				leaves = append(leaves, l)

				continue
			}

			for leaf := range g.Leaves(l) {
				leaves = append(leaves, leaf)
			}
		}

		slices.Sort(leaves)
		out[f] = slices.Compact(leaves)
	}

	return out, g, nil
}
