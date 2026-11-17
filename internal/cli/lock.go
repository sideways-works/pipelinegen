package cli

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// Locks between runs are gone. lock and unlock stay one release, doing nothing, so a pipeline written
// before still runs its lock steps; the release after removes them.
const (
	lockSummary   = "gone: does nothing and exits 0, so a pipeline written before still runs; removed in the next release"
	locksGone     = "locks are gone: a suite's batches start when their node has room, and this step does nothing; regenerate the pipeline (pipelinegen pipeline) to drop it before the next release, which no longer has it\n"
	oldLeasesRefs = "refs/pipelinegen/locks/"
	oldLeasesDir  = "locks"
)

func runLock(ctx context.Context, args []string, stdout io.Writer) error {
	if describing(ctx) {
		return flag.ErrHelp
	}

	// an older pipeline's batch end judges the batch's time limit by the stamp its lock step left
	if len(args) > 0 && args[0] == "acquire" {
		if dir := startedArg(args[1:]); dir != "" {
			if err := writeStarted(dir, time.Now()); err != nil {
				warnf(os.Stderr, "pipelinegen lock: %v\n", err)
			}
		}
	}

	warnf(stdout, "pipelinegen lock: "+locksGone)

	return nil
}

func runUnlock(ctx context.Context, _ []string, stdout io.Writer) error {
	if describing(ctx) {
		return flag.ErrHelp
	}

	warnf(stdout, "pipelinegen unlock: "+locksGone)

	return nil
}

// describing hands over no flags: the commands read none.
func describing(ctx context.Context) bool {
	seen, ok := ctx.Value(describeKey{}).(*[]*flag.FlagSet)
	if ok {
		*seen = append(*seen, flag.NewFlagSet("", flag.ContinueOnError))
	}

	return ok
}

func startedArg(args []string) string {
	for i, a := range args {
		name, value, eq := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "started" {
			continue
		}

		if !eq && i+1 < len(args) {
			value = args[i+1]
		}

		if value != "" {
			return filepath.Clean(value)
		}
	}

	return ""
}

// sweepOldLeases removes the leases an older pipelinegen's locks left in the store: its refs under
// refs/pipelinegen/locks/, or its files under locks/ on the branch store's -locks branch. Nothing reads
// them; a plan step that finds none writes nothing, and one that fails only warns. A run that isn't
// trusted leaves them to one that is.
func sweepOldLeases(ctx context.Context, cfg *config.Config, store *gitstore.Store, stderr io.Writer) {
	if store.Proposed || store.ReadOnly {
		return
	}

	var err error

	if cfg.OnBranch() {
		err = sweepOldLeaseFiles(ctx, cfg, store.Remote)
	} else {
		err = sweepOldLeaseRefs(ctx, store)
	}

	if err != nil {
		warnf(stderr, "pipelinegen plan: the leases an older pipelinegen's locks left stay until the next plan step: %v\n", err)
	}
}

func sweepOldLeaseRefs(ctx context.Context, store *gitstore.Store) error {
	refs, err := store.List(ctx, oldLeasesRefs)
	if err != nil || len(refs) == 0 {
		return err
	}

	ups := make([]gitstore.Update, 0, len(refs))
	for name := range refs {
		ups = append(ups, gitstore.Update{Ref: name, Any: true, Delete: true})
	}

	_, err = store.Write(ctx, "sweep the leases of the retired locks", ups...)

	return err
}

func sweepOldLeaseFiles(ctx context.Context, cfg *config.Config, remote string) (err error) {
	b := branchStore(cfg, remote, cfg.LocksBranchName())
	b.Roots = true

	defer func() {
		if cerr := b.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	return b.Update(ctx, "sweep the leases of the retired locks", func(ctx context.Context, v *branchstore.View) (branchstore.Change, error) {
		files, err := v.Files(ctx, oldLeasesDir)

		return branchstore.Change{Deletes: files}, err
	})
}
