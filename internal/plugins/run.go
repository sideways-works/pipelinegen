package plugins

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// CollectTimeout bounds each correlator's collect.
const CollectTimeout = 10 * time.Second

// Collect has every correlator that collects take its part of what a
// batch's processes left behind, in the config's order: action is
// setup-end, slice-start or slice-end, dir the slice's coverage directory
// (each plugin works in <dir>/<plugin>), stack whether the suite runs
// long-lived processes. The first failure stops it and names the plugin.
func (s *Set) Collect(ctx context.Context, action, slice, dir string, stack bool) error {
	for _, l := range s.Plugins {
		if !l.can(plugin.CallCollect) || !l.Desc.Handles(action) {
			continue
		}

		p := l.P
		p.Timeout = CollectTimeout

		req := plugin.CollectRequest{Request: p.NewRequest(s.work()), Action: action, Slice: slice, Dir: filepath.Join(dir, l.Conf.Name), Stack: stack, Claimed: s.claimed[l.Conf.Name]}
		if err := p.Call(ctx, plugin.CallCollect, req, nil); err != nil {
			return err
		}
	}

	return nil
}

// Claims are each plugin's claims in the tree checked out at root, by
// plugin name.
func (s *Set) Claims(ctx context.Context, root string) (map[string][]plugin.Claim, error) {
	root = RealPath(root)

	files, err := blobs(ctx, root)
	if err != nil {
		return nil, err
	}

	out := make(map[string][]plugin.Claim, len(s.Plugins))

	for _, l := range s.Plugins {
		c, _, err := s.claims(ctx, l, root, files)
		if err != nil {
			return nil, err
		}

		out[l.Conf.Name] = append(c, s.fragments(ctx, l, files, c)...)
	}

	return out, nil
}

func (s *Set) fragments(ctx context.Context, l *Loaded, files map[string]string, claims []plugin.Claim) []plugin.Claim {
	seen := map[string]bool{}
	for _, c := range claims {
		seen[c.Path] = true
	}

	var out []plugin.Claim

	for _, call := range []string{plugin.CallUnits, plugin.CallLinks} {
		if !l.can(call) || l.Desc.Inputs[call] != plugin.PerSet {
			continue
		}

		for _, set := range s.lookup(ctx, l, call, files, unitDigests{}, claims).served {
			for _, in := range set.Inputs {
				if !seen[in] {
					seen[in] = true
					out = append(out, plugin.Claim{Path: in, Role: "fragment"})
				}
			}
		}
	}

	slices.SortFunc(out, func(a, b plugin.Claim) int { return strings.Compare(a.Path, b.Path) })

	return out
}

// SetClaimed has the set's observe and collect calls carry each plugin's
// claims.
func (s *Set) SetClaimed(claimed map[string][]plugin.Claim) { s.claimed = claimed }

// Observation is what one correlator saw a slice touch.
type Observation struct {
	Plugin string
	// CantTell is set when the plugin couldn't tell; Reason is what it said,
	// its secrets masked.
	CantTell  bool
	Reason    string
	Touched   []string
	SetupLike []string
}

// Observe asks every correlator that observes what a slice touched.
func (s *Set) Observe(ctx context.Context, tree, slice, setup string, parts []string) ([]Observation, error) {
	tree = RealPath(tree)

	// the setup's directory always comes first, so a plugin tells it by
	// position: a batch without one gets a path that doesn't exist, which
	// is a setup that left no evidence
	if setup == "" {
		setup = filepath.Join(s.work(), "no-setup")
	}

	var out []Observation

	for _, l := range s.Plugins {
		if !l.can(plugin.CallObserve) {
			continue
		}

		dirs := make([]string, 0, 1+len(parts))
		dirs = append(dirs, filepath.Join(setup, l.Conf.Name))

		for _, p := range parts {
			dirs = append(dirs, filepath.Join(p, l.Conf.Name))
		}

		work := s.work()

		var ans plugin.ObserveAnswer

		err := l.P.Call(ctx, plugin.CallObserve, plugin.ObserveRequest{Request: l.P.NewRequest(work), Tree: tree, Slice: slice, Dirs: dirs, Claimed: s.claimed[l.Conf.Name]}, &ans)
		if errors.Is(err, plugin.ErrCantTell) {
			out = append(out, Observation{Plugin: l.Conf.Name, CantTell: true, Reason: err.Error()})

			continue
		}

		if err == nil {
			ans.Touched, err = plugin.ReadLines(work, named(ans.Files, "touched"), ans.Touched)
		}

		if err != nil {
			return nil, err
		}

		out = append(out, Observation{Plugin: l.Conf.Name, Touched: ans.Touched, SetupLike: ans.SetupLike})
	}

	return out, nil
}
