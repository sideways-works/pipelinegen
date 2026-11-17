package plugins

import (
	"path"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
)

// Leaf says whether a recorded slice keeps one of its data labels.
type Leaf int

const (
	// LeafKept stays a label of the slice.
	LeafKept Leaf = iota
	// LeafUnits is a file whose units replace it: those the slice touched
	// are roots already.
	LeafUnits
	// LeafBuild is a BUILD or .bzl file: what it declares reaches the
	// slice through the rules it holds.
	LeafBuild
	// LeafUnobserved stays whole: its sbom lists units for it, but no
	// recording of the suite holds any.
	LeafUnobserved
)

// Leaves judges the data labels of a tree's slices, alike for the walk and
// for binding.
type Leaves struct {
	cfg *config.Config
	t   *deps.Tree

	recorded map[string]map[string]bool            // suite → sbom
	dirs     map[string]map[string]map[string]bool // suite → sbom → directory of a listed unit's file
	setup    map[string]map[string]map[string]bool // the same for the suites' setup lists alone
	lists    map[string]map[string]bool            // suite/slice → unit
	setups   map[string]map[string]bool            // suite → unit
	build    bool
}

// NewLeaves reads what the slices' lists hold. A nil config judges no
// suite recorded, so no BUILD file is left out.
func NewLeaves(cfg *config.Config, t *deps.Tree, bs []BindSlice) *Leaves {
	f := &Leaves{
		cfg: cfg, t: t, recorded: RecordedSBOMs(t, bs), dirs: map[string]map[string]map[string]bool{}, setup: map[string]map[string]map[string]bool{},
		lists: map[string]map[string]bool{}, setups: map[string]map[string]bool{}, build: slices.Contains(t.Narrowings, NarrowBuildFiles),
	}

	for _, sl := range bs {
		held := map[string]bool{}

		for _, id := range sl.List {
			held[id] = true

			if p, file := t.Providers[id], t.Units[id].File; p != "" && file != "" {
				note(f.dirs, sl.Suite, p, path.Dir(file))

				if sl.Setup {
					note(f.setup, sl.Suite, p, path.Dir(file))
				}
			}
		}

		if !sl.Setup {
			f.lists[sl.Suite+"/"+sl.Name] = held

			continue
		}

		if f.setups[sl.Suite] == nil {
			f.setups[sl.Suite] = map[string]bool{}
		}

		for id := range held {
			f.setups[sl.Suite][id] = true
		}
	}

	return f
}

func note(m map[string]map[string]map[string]bool, suite, sbom, dir string) {
	if m[suite] == nil {
		m[suite] = map[string]map[string]bool{}
	}

	if m[suite][sbom] == nil {
		m[suite][sbom] = map[string]bool{}
	}

	m[suite][sbom][dir] = true
}

// Judge says whether a slice keeps a data label. inGroup marks a label
// found inside one of its filegroups: such a file without an anchor is
// replaced only where some list of the suite holds units its sbom lists
// for files of the same directory.
func (f *Leaves) Judge(sl BindSlice, label string, inGroup bool) Leaf {
	p, ok := bazel.LabelPath(label)
	if !ok || !sl.HasList || slices.Contains(sl.Declared, label) {
		return LeafKept
	}

	if f.cfg != nil && f.cfg.Suites[sl.Suite].ReadsText(sl.Name, p) {
		return LeafKept
	}

	if f.build && IsBuildFile(p) && f.cfg != nil && f.cfg.Suites[sl.Suite].Bind == config.BindRecorded {
		return LeafBuild
	}

	units, ok := f.t.FileUnits[p]
	if !ok {
		return LeafKept
	}

	sbom := ProviderOf(f.t, units)

	if anchor, anchored := f.t.Anchors[p]; anchored {
		if f.lists[sl.Suite+"/"+sl.Name][anchor] || f.setups[sl.Suite][anchor] {
			return LeafUnits
		}

		// its recording doesn't cover the file
		return LeafKept
	}

	switch {
	case !f.recorded[sl.Suite][sbom]:
		return LeafUnobserved
	case inGroup && !f.dirs[sl.Suite][sbom][path.Dir(p)]:
		return LeafKept
	}

	return LeafUnits
}

// SetupTakes tells whether a suite's setup list holds units the sbom of
// the file at p lists for files of its directory: the setup is then where
// adding or removing such a file shows.
func (f *Leaves) SetupTakes(suite, p string) bool {
	units, ok := f.t.FileUnits[p]
	if !ok {
		return false
	}

	return f.setup[suite][ProviderOf(f.t, units)][path.Dir(p)]
}

// IsBuildFile tells a BUILD file or a Starlark file from data.
func IsBuildFile(p string) bool {
	base := path.Base(p)

	return base == "BUILD" || base == "BUILD.bazel" || strings.HasSuffix(base, ".bzl")
}

func (s *Set) config() *config.Config {
	if s == nil {
		return nil
	}

	return s.o.Config
}
