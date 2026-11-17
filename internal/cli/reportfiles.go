package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
)

type filesFlags struct {
	common

	run, change, commit, suite, slice, out string
	local, asJSON                          bool
}

func (f *filesFlags) register(fs *flag.FlagSet) {
	f.common.register(fs)
	textVar(fs, &f.run, "run", "", "the run, as the config names it")
	textVar(fs, &f.change, "change", "", "the change it ran, as its comment and why name it")
	textVar(fs, &f.commit, "commit", "", "the commit it ran (default: the change's newest plan)")
	textVar(fs, &f.suite, "suite", "", "only this suite")
	textVar(fs, &f.slice, "slice", "", "only this slice, by its name or its label")
	pathVar(fs, &f.out, "out", "", "the directory to write into: <out>/<suite>/<slice>/<file>", writes)
	fs.BoolVar(&f.local, "local", false, "read the run from the local store `pipelinegen record` writes")
	fs.BoolVar(&f.asJSON, "json", false, "print what was kept and dropped as JSON")
}

// reportFiles writes what failed slices of a kept run left to show why
// into a directory, one directory per slice.
func reportFiles(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var f filesFlags

	fs := flag.NewFlagSet("report files", flag.ContinueOnError)
	f.register(fs)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if f.run == "" || f.change == "" {
		return fmt.Errorf("%w: --run and --change are needed", errUsage)
	}

	cfg, err := f.load()
	if err != nil {
		return err
	}

	if f.suite != "" {
		if _, err := pickSuites(cfg, f.suite); err != nil {
			return err
		}
	}

	store, err := keptStore(ctx, cfg, absRoot(f.root), f.local)
	if err != nil {
		return err
	}

	defer closeStore(store, &err)

	id, err := keptRun(ctx, store, f.run, f.change, f.commit)
	if err != nil {
		return err
	}

	list, err := report.RunFiles(ctx, store, id, f.suite, f.slice)
	if err != nil {
		return err
	}

	n := 0
	if f.out != "" {
		if n, err = report.WriteFiles(ctx, store, list, f.out); err != nil {
			return err
		}
	}

	if !f.asJSON {
		return emit(stdout, filesText(list, id, f.out, n))
	}

	var b bytes.Buffer

	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")

	if err := enc.Encode(list); err != nil {
		return fmt.Errorf("encode: %w", err)
	}

	return emit(stdout, &b)
}

func filesText(list []report.SliceFiles, id report.ID, out string, n int) *bytes.Buffer {
	var b bytes.Buffer

	title := fmt.Sprintf("%s %s at %s", id.Run, id.Change, short12(id.Commit))

	if len(list) == 0 {
		fmt.Fprintf(&b, "no failed slice of %s kept failure files\n", title)

		return &b
	}

	for _, sf := range list {
		fmt.Fprintf(&b, "%s %s (%s):\n", sf.Suite, sf.Slice, sf.State)

		for _, f := range sf.Files {
			fmt.Fprintf(&b, "  %s%s, %s\n", f.Path, labelText(f.Label), sizeText(f.Bytes))
		}

		for _, d := range sf.Dropped {
			fmt.Fprintf(&b, "  dropped %s%s: %s\n", d.Path, labelText(d.Label), d.Why)
		}
	}

	if out == "" {
		b.WriteString("nothing written: --out names the directory to write them into\n")
	} else {
		fmt.Fprintf(&b, "wrote %d files of %s into %s\n", n, title, out)
	}

	return &b
}

func labelText(label string) string {
	if label == "" {
		return ""
	}

	return " (" + label + ")"
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}

	return fmt.Sprintf("%d bytes", n)
}

// keptStore is the store a person reads a kept run from, read only: the
// repository's remote, or with local the store `pipelinegen record` writes.
func keptStore(ctx context.Context, cfg *config.Config, root string, local bool) (*gitstore.Store, error) {
	if local {
		path, err := localStorePath(ctx, root)
		if err != nil {
			return nil, err
		}

		url, ok, err := localStoreURL(ctx, path, false)

		switch {
		case err != nil:
			return nil, err
		case !ok:
			return nil, fmt.Errorf("%w: no local store at %s (`pipelinegen record` makes it)", errNoRemote, path)
		}

		return &gitstore.Store{Remote: url, Retries: config.DefaultRetries, ReadOnly: true}, nil
	}

	withOrigin(ctx, cfg, root)

	if codeRemote(cfg) == "" {
		return nil, fmt.Errorf("%w: a kept run is read from the repository's remote: git.remote, the github section or the checkout's origin (or --local for a local recording's)", errNoRemote)
	}

	s := openStore(cfg)
	s.ReadOnly = true

	return s, nil
}
