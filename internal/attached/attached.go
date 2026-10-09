// Package attached names the runs whose recordings went into a commit's
// lists: one file per run under _attached/ in the commit's lists ref,
// written with the lists. Older lists keep them in a ref of their own,
// refs/pipelinegen/attached/<commit>, read the same until swept.
package attached

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// Prefix holds, per commit, which runs were attached to its lists, as
// lists written before kept them.
const Prefix = "refs/pipelinegen/attached/"

// ListsPrefix holds the lists, and with them which runs went into them.
const ListsPrefix = "refs/pipelinegen/lists/"

// Dir is where a lists ref keeps the runs attached to it: a lists file
// is <suite>/<slice>.txt, and no suite's name starts with "_".
const Dir = "_attached/"

// Ref is a commit's attached ref.
func Ref(commit string) string { return Prefix + commit }

// Attachment names a run whose recordings went into a commit's lists.
type Attachment struct {
	Run    string `json:"run"`
	Change string `json:"change"`
	// Commit is the commit the run tested, as given to attach.
	Commit string `json:"commit"`
	// Seed is set when the recordings came from a person's machine
	// (pipelinegen seed); the commit is then a base for the branch's first
	// runs.
	Seed *Seed `json:"seed,omitempty"`
}

// Seed says where seeded lists came from.
type Seed struct {
	From    string       `json:"from"`
	Tool    string       `json:"tool"`
	Plugins []SeedPlugin `json:"plugins,omitempty"`
	At      time.Time    `json:"at"`
	// Baseline is set for a baseline set by hand (pipelinegen baseline):
	// no recordings came with it, the commit is only declared good for
	// the run its note names as its change.
	Baseline bool `json:"baseline,omitempty"`
}

// SeedPlugin is one plugin of a seed: its name and the build the config
// pins (a release's version and checksum, or a commit).
type SeedPlugin struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Pin     string `json:"pin"`
}

// File is where an attachment lives in the attached ref: one file per run,
// so several runs attached to one commit each keep their own.
func (a Attachment) File() string {
	return url.PathEscape(a.Run) + "/" + url.PathEscape(a.Change) + "/" + url.PathEscape(a.Commit) + ".json"
}

// ErrAttached wraps an attached ref that can't be read.
var ErrAttached = errors.New("attached runs")

// Read lists the runs attached to commit; none is an empty list.
func Read(ctx context.Context, store *gitstore.Store, commit string) ([]Attachment, error) {
	byFile := map[string]Attachment{}

	for _, ref := range []string{ListsPrefix + commit, Ref(commit)} {
		found, ok, err := store.First(ctx, []string{ref})
		if err != nil {
			return nil, err
		}

		if !ok {
			continue
		}

		got, err := attachments(ctx, store, found)
		if err != nil {
			return nil, err
		}

		for _, a := range got {
			byFile[a.File()] = a
		}
	}

	out := slices.Collect(maps.Values(byFile))
	slices.SortFunc(out, func(x, y Attachment) int { return strings.Compare(x.File(), y.File()) })

	return out, nil
}

func attachments(ctx context.Context, store *gitstore.Store, ref gitstore.Ref) ([]Attachment, error) {
	var (
		files map[string][]byte
		err   error
	)

	if strings.HasPrefix(ref.Name, ListsPrefix) {
		files, err = store.Under(ctx, ref.Commit, Dir)
	} else {
		files, err = store.Files(ctx, ref.Commit)
	}

	if err != nil {
		return nil, err
	}

	out := make([]Attachment, 0, len(files))

	for name, data := range files {
		var a Attachment
		if err := json.Unmarshal(data, &a); err != nil {
			return nil, fmt.Errorf("%w: %s in %s: %w", ErrAttached, name, ref.Name, err)
		}

		out = append(out, a)
	}

	return out, nil
}

// Landed is a commit's attached ref: the runs whose recordings went into
// its lists, and when the last of them was attached.
type Landed struct {
	Commit string
	Time   time.Time
	Runs   []Attachment
}

// Since lists every commit attached to after from, oldest first.
func Since(ctx context.Context, store *gitstore.Store, from time.Time) ([]Landed, error) {
	refs, err := store.List(ctx, Prefix, ListsPrefix)
	if err != nil {
		return nil, err
	}

	byCommit := map[string]*Landed{}

	for _, name := range slices.Sorted(maps.Keys(refs)) {
		ref := refs[name]
		if !ref.Time.After(from) {
			continue
		}

		commit := strings.TrimPrefix(strings.TrimPrefix(name, Prefix), ListsPrefix)

		got, err := attachments(ctx, store, ref)
		if err != nil {
			return nil, err
		}

		if len(got) == 0 {
			continue
		}

		l := byCommit[commit]
		if l == nil {
			l = &Landed{Commit: commit}
			byCommit[commit] = l
		}

		if ref.Time.After(l.Time) {
			l.Time = ref.Time
		}

		for _, a := range got {
			if !slices.ContainsFunc(l.Runs, func(b Attachment) bool { return b.File() == a.File() }) {
				l.Runs = append(l.Runs, a)
			}
		}
	}

	out := make([]Landed, 0, len(byCommit))
	for _, l := range byCommit {
		out = append(out, *l)
	}

	slices.SortFunc(out, func(a, b Landed) int {
		if c := a.Time.Compare(b.Time); c != 0 {
			return c
		}

		return strings.Compare(a.Commit, b.Commit)
	})

	return out, nil
}
