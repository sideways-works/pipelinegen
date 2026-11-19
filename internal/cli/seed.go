package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/attached"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitssh"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/plugin"
)

const seedRun = "seed"

var (
	errSeedRefused = errors.New("seed refused")
	errNoRecording = errors.New("no local recording")
)

type seedFlags struct {
	common

	commit, store   string
	dryRun, replace bool
}

// The seeded commit becomes the base of the branch's runs until one of them passes.
func runSeed(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var f seedFlags

	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	f.register(fs)
	textVar(fs, &f.commit, "commit", "HEAD", "the commit to seed: HEAD, recorded with pipelinegen record")
	pathVar(fs, &f.store, "store", "", "the local store record wrote (default <git directory>/pipelinegen/local.git)", reads)
	fs.BoolVar(&f.dryRun, "dry-run", false, "print what would be written, and write nothing")
	fs.BoolVar(&f.replace, "replace", false, "let each slice that passed locally replace the list the commit has, not only add to it")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if err := personOnly(); err != nil {
		return err
	}

	cfg, err := f.load()
	if err != nil {
		return err
	}

	root := absRoot(f.root)

	commit, err := headCommit(ctx, root, f.commit)
	if err != nil {
		return err
	}

	if err := cleanTree(ctx, root, filepath.Join(root, "tools", "plugins")); err != nil {
		return fmt.Errorf("%w: %w", errSeedRefused, err)
	}

	if err := commitOnBranch(ctx, root, commit, cfg.Concourse.Branch); err != nil {
		return err
	}

	if !f.dryRun && !storeCredential(cfg) {
		return fmt.Errorf("%w: the store's writing credential isn't in the environment (%s); a seed writes the shared lists, so it needs it", errSeedRefused, storeCredentialName(cfg))
	}

	local, localPath, err := f.localStore(ctx, root)
	if err != nil {
		return err
	}

	defer closeStore(local, &err)

	if err := sharedRemote(ctx, cfg, root, local.Remote, localPath); err != nil {
		return err
	}

	runs, err := localRuns(ctx, local, commit)
	if err != nil {
		return err
	}

	shared := openStore(cfg)
	defer closeStore(shared, &err)

	return f.seed(ctx, cfg, root, commit, local, shared, runs, stdout)
}

func (f *seedFlags) localStore(ctx context.Context, root string) (*gitstore.Store, string, error) {
	path := f.store
	if path == "" {
		var err error
		if path, err = localStorePath(ctx, root); err != nil {
			return nil, "", err
		}
	}

	path = absOr(path)

	url, ok, err := localStoreURL(ctx, path, false)
	if err != nil {
		return nil, "", err
	}

	if !ok {
		return nil, "", fmt.Errorf("%w: no local store at %s: record the suites first (pipelinegen record --suite <suite>)", errNoRecording, path)
	}

	return &gitstore.Store{Remote: url, Retries: config.DefaultRetries}, path, nil
}

func storeCredential(cfg *config.Config) bool {
	if cfg.UsesSSH() {
		return os.Getenv(gitssh.KeyEnv(plugin.PurposeStore)) != ""
	}

	return tokenFor(cfg, plugin.PurposeStore) != ""
}

func storeCredentialName(cfg *config.Config) string {
	if cfg.UsesSSH() {
		return gitssh.KeyEnv(plugin.PurposeStore)
	}

	return tokenEnv(cfg, plugin.PurposeStore)
}

func sharedRemote(ctx context.Context, cfg *config.Config, root, localURL, localPath string) error {
	withOrigin(ctx, cfg, root)

	switch remote := codeRemote(cfg); remote {
	case "":
		return fmt.Errorf("%w: the config names no store to seed (git.remote, the github section or the checkout's origin)", errSeedRefused)
	case localURL, localPath:
		return fmt.Errorf("%w: the shared store is the local store %s", errSeedRefused, localPath)
	}

	return nil
}

func localRuns(ctx context.Context, local *gitstore.Store, commit string) ([]report.ID, error) {
	refs, err := local.List(ctx, report.PlansOf(localRun))
	if err != nil {
		return nil, err
	}

	var (
		out    []report.ID
		others []string
	)

	for _, name := range slices.Sorted(maps.Keys(refs)) {
		change, c12, ok := strings.Cut(strings.TrimPrefix(name, report.PlansOf(localRun)), "/")
		if !ok {
			continue
		}

		if c12 != short12(commit) {
			if !slices.Contains(others, c12) {
				others = append(others, c12)
			}

			continue
		}

		ch, err := report.NameFromRef(change)
		if err != nil {
			return nil, err
		}

		out = append(out, report.ID{Run: localRun, Change: ch, Commit: commit})
	}

	if len(out) == 0 {
		has := "none"
		if len(others) > 0 {
			has = strings.Join(others, ", ")
		}

		return nil, fmt.Errorf("%w of %s in the local store (it has recordings of: %s): record it first (pipelinegen record --suite <suite>)", errNoRecording, short12(commit), has)
	}

	return out, nil
}

type seedPart struct {
	id                 report.ID
	from               []string
	recordings         recording.Lists
	marks              lists.Marks
	shrink, whole      map[string]bool
	failed, unfinished []string
}

// Every slice its refs hold counts, not only those its plan names: two recordings started together may
// share a run and leave only the last one's plan.
func readPart(ctx context.Context, local *gitstore.Store, id report.ID) (*seedPart, error) {
	plan, ok, err := readPlan(ctx, local, id)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, fmt.Errorf("%w: %s has no plan", errNoRecording, id.Change)
	}

	p := &seedPart{id: id, from: []string{id.Change}, whole: map[string]bool{}}

	if p.recordings, p.marks, err = runRecordings(ctx, local, id); err != nil {
		return nil, err
	}

	selected := map[string]bool{}

	for _, sp := range plan.Suites {
		for _, sl := range sp.Selected {
			selected[sp.Suite+"/"+sl.Name] = true
		}
	}

	states, err := report.SliceStates(ctx, local, id)
	if err != nil {
		return nil, err
	}

	// A local run's passed slices are whole; a failed one's recording may have stopped part way.
	for _, key := range slices.Sorted(maps.Keys(states)) {
		delete(selected, key)

		switch states[key] {
		case report.Passed:
			p.whole[key] = true
		case report.Failed:
			p.failed = append(p.failed, key)
		default:
			p.unfinished = append(p.unfinished, key)
		}
	}

	p.unfinished = append(p.unfinished, slices.Collect(maps.Keys(selected))...)

	return p, nil
}

// Each slice takes its newest passed recording; one no recording passed only adds to the commit's list.
func mergeParts(parts []*seedPart, replace bool) *seedPart {
	out := &seedPart{id: parts[len(parts)-1].id, recordings: recording.Lists{}, marks: lists.Marks{}, whole: map[string]bool{}, shrink: map[string]bool{}}

	for _, p := range parts {
		out.from = append(out.from, p.id.Change)

		keys := slices.Collect(maps.Keys(p.recordings))
		keys = append(keys, slices.Collect(maps.Keys(p.marks))...)

		for _, key := range keys {
			// a passed recording replaces what an older one gave; one
			// that didn't pass stands only until a recording passes.
			if !p.whole[key] && out.whole[key] {
				continue
			}

			if ids, ok := p.recordings[key]; ok {
				out.recordings[key] = ids
			}

			if ms, ok := p.marks[key]; ok {
				out.marks[key] = ms
			} else {
				delete(out.marks, key)
			}
		}

		maps.Copy(out.whole, p.whole)
	}

	failed, unfinished := map[string]bool{}, map[string]bool{}

	for _, p := range parts {
		for _, key := range p.failed {
			failed[key], unfinished[key] = true, false
		}

		for _, key := range p.unfinished {
			unfinished[key], failed[key] = true, false
		}
	}

	for key, ok := range failed {
		if ok && !out.whole[key] {
			out.failed = append(out.failed, key)
		}
	}

	for key, ok := range unfinished {
		if ok && !out.whole[key] {
			out.unfinished = append(out.unfinished, key)
		}
	}

	slices.Sort(out.failed)
	slices.Sort(out.unfinished)

	if replace {
		maps.Copy(out.shrink, out.whole)
	}

	return out
}

func (f *seedFlags) seed(ctx context.Context, cfg *config.Config, root, commit string, local, shared *gitstore.Store, ids []report.ID, stdout io.Writer) error {
	parts := make([]*seedPart, 0, len(ids))

	for _, id := range ids {
		p, err := readPart(ctx, local, id)
		if err != nil {
			return err
		}

		parts = append(parts, p)
	}

	parts = []*seedPart{mergeParts(parts, f.replace)}

	if f.dryRun {
		return f.dry(ctx, cfg, root, commit, shared, parts, stdout)
	}

	note := seedNote(cfg, commit)

	var (
		b       bytes.Buffer
		written bool
	)

	for _, p := range parts {
		got, err := lists.Attach{
			Repo: root, Run: seedRun, Change: f.noteChange(p), Commit: commit, To: commit,
			Recordings: p.recordings, CannotTell: p.marks, Shrinkable: p.shrink, Whole: p.whole,
			Walk: cfg.ListsCommits(), Trusted: true, Seed: note, KeepMarks: !f.replace,
		}.Do(ctx, shared)
		if err != nil {
			return err
		}

		fmt.Fprintf(&b, "%s\n", attachedText(got, fromText(p)))

		if !got.Again {
			written = true

			writeLeftOut(&b, p)
		}
	}

	if written {
		fmt.Fprintf(&b, "seeded %s: runs on the default branch compare with it until one of them passes\n", short12(commit))
	}

	return emit(stdout, &b)
}

func fromText(p *seedPart) string {
	if len(p.from) == 1 {
		return "the local recording " + p.from[0]
	}

	return "the local recordings " + strings.Join(p.from, ", ")
}

// A replacing seed's change is apart, so --replace after a plain seed of the same recording isn't taken
// for the same seed.
func (f *seedFlags) noteChange(p *seedPart) string {
	if f.replace {
		return p.id.Change + "-replace"
	}

	return p.id.Change
}

func seedNote(cfg *config.Config, commit string) *attached.Seed {
	s := &attached.Seed{From: commit, Tool: Version, At: time.Now().UTC().Truncate(time.Second)}

	for _, p := range cfg.Plugins {
		sp := attached.SeedPlugin{Name: p.Name, Version: "commit", Pin: p.Commit}
		switch {
		case p.Release != nil:
			sp.Version, sp.Pin = p.Release.Version, p.Release.Pin()
		case p.Zip != nil:
			sp.Version, sp.Pin = "zip", p.Zip.SHA256
		}

		s.Plugins = append(s.Plugins, sp)
	}

	return s
}

func writeLeftOut(b *bytes.Buffer, p *seedPart) {
	if len(p.failed) > 0 {
		fmt.Fprintf(b, "  no list for %s that failed locally: %s\n", plural(len(p.failed), "slice"), strings.Join(p.failed, ", "))
	}

	if len(p.unfinished) > 0 {
		fmt.Fprintf(b, "  no list for %s that didn't finish locally: %s\n", plural(len(p.unfinished), "slice"), strings.Join(p.unfinished, ", "))
	}
}

func (f *seedFlags) dry(ctx context.Context, cfg *config.Config, root, commit string, shared *gitstore.Store, parts []*seedPart, stdout io.Writer) error {
	have, src, err := lists.NearestWithin(ctx, shared, root, commit, cfg.ListsCommits())
	if err != nil {
		return err
	}

	done, err := lists.Attachments(ctx, shared, commit)
	if err != nil {
		return err
	}

	var b bytes.Buffer

	fmt.Fprintf(&b, "dry run, nothing written: seeding %s would merge into %s\n", short12(commit), baseText(src))

	recorded := map[string]bool{}

	for _, p := range parts {
		for _, key := range append(append(slices.Collect(maps.Keys(p.whole)), p.failed...), p.unfinished...) {
			recorded[strings.SplitN(key, "/", 2)[0]] = true
		}

		me := lists.Attachment{Run: seedRun, Change: f.noteChange(p), Commit: commit}
		if slices.ContainsFunc(done, func(a lists.Attachment) bool { return a.File() == me.File() }) {
			fmt.Fprintf(&b, "%s: seeded already, nothing would be written\n", fromText(p))

			continue
		}

		var made, added, replaced int

		for key := range p.whole {
			_, listed := have[key]

			switch {
			case !listed:
				made++
			case p.shrink[key]:
				replaced++
			default:
				added++
			}
		}

		fmt.Fprintf(&b, "%s: a new list for %s, more units for %s, a replaced list for %s\n",
			fromText(p), plural(made, "slice"), plural(added, "slice"), plural(replaced, "slice"))
		writeLeftOut(&b, p)
	}

	for _, suite := range cfg.SuiteNames() {
		if cfg.Suites[suite].Bind == config.BindRecorded && !recorded[suite] {
			fmt.Fprintf(&b, "suite %s: not recorded locally; its slices without a list run whole\n", suite)
		}
	}

	return emit(stdout, &b)
}
