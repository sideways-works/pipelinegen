package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/plugin"
)

// The exit codes of status: what a server's hook or a shell acts on.
const (
	statusPassed  = 0
	statusFailed  = 1
	statusPending = 2
	statusNoRun   = 3
)

// exitCodeError is a command's answer as an exit code, after it printed what
// it had to say: Run exits with it and adds nothing.
type exitCodeError int

func (e exitCodeError) Error() string { return fmt.Sprintf("exit %d", int(e)) }

// StatusLine is one run's state for a commit, as status prints it.
type StatusLine struct {
	Run    string `json:"run"`
	Change string `json:"change"`
	Commit string `json:"commit"`
	// State is passed, failed, errored or pending.
	State       string `json:"state"`
	Description string `json:"description,omitempty"`
}

func runStatus(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		c             common
		run           string
		asJSON, local bool
	)

	what, args := takeName(args)

	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	c.register(fs)
	textVar(fs, &run, "run", "", "only this run")
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	fs.BoolVar(&local, "local", false, "read the local store `pipelinegen record` writes, not the remote")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if what == "" {
		return fmt.Errorf("%w: status needs a commit or a branch first: status <commit|branch> [flags]", errUsage)
	}

	root := absRoot(c.root)

	commit, err := statusCommit(ctx, root, what)
	if err != nil {
		return err
	}

	cfg, err := statusConfig(ctx, c, root, commit)
	if err != nil {
		return err
	}

	from := listsRemote
	if local {
		from = listsLocal
	}

	store := listsStore(ctx, cfg, root, from, os.Stderr, "status")
	if store == nil {
		return errNoListsStore
	}

	defer closeStore(store, &err)

	lines, err := statusLines(ctx, store, cfg, commit, run)
	if err != nil {
		return err
	}

	if err := writeStatus(stdout, lines, what, commit, asJSON); err != nil {
		return err
	}

	return statusExit(cfg.Runs, run, lines)
}

func statusLines(ctx context.Context, store *gitstore.Store, cfg *config.Config, commit, run string) ([]StatusLine, error) {
	plans, err := store.List(ctx, report.AllPlans)
	if err != nil {
		return nil, err
	}

	var lines []StatusLine

	for ref := range plans {
		r, change, short, ok := planParts(ref)
		if !ok || (run != "" && r != run) || !strings.HasPrefix(commit, short) {
			continue
		}

		state, desc, err := report.RunStatusOf(ctx, store, cfg, report.ID{Run: r, Change: change, Commit: commit})
		if err != nil {
			return nil, err
		}

		if state != "" {
			lines = append(lines, StatusLine{Run: r, Change: change, Commit: commit, State: stateWord(state), Description: desc})
		}
	}

	more, err := supersededLines(ctx, store, commit, run, lines)
	if err != nil {
		return nil, err
	}

	lines = append(lines, more...)

	sort.Slice(lines, func(i, j int) bool { return lines[i].Run+lines[i].Change < lines[j].Run+lines[j].Change })

	return lines, nil
}

const stateSuperseded = "superseded"

func supersededLines(ctx context.Context, store *gitstore.Store, commit, run string, planned []StatusLine) ([]StatusLine, error) {
	refs, err := store.List(ctx, supersededPrefix)
	if err != nil {
		return nil, err
	}

	var out []StatusLine

	for name := range refs {
		r, short, ok := strings.Cut(strings.TrimPrefix(name, supersededPrefix), "/")
		if !ok || (run != "" && r != run) || !strings.HasPrefix(commit, short) ||
			slices.ContainsFunc(planned, func(l StatusLine) bool { return l.Run == r }) {
			continue
		}

		s, found, err := supersededOf(ctx, store, r, commit)
		if err != nil || !found {
			return nil, err
		}

		out = append(out, StatusLine{Run: r, Change: s.Change, Commit: commit, State: stateSuperseded, Description: "superseded by " + short12(s.Tip) + ": not run, the newer commit's run covers it"})
	}

	return out, nil
}

// Without a config on disk (a server's bare repository), statusConfig reads the commit's own.
func statusConfig(ctx context.Context, c common, root, commit string) (*config.Config, error) {
	if c.config != "" || fileExists(filepath.Join(root, config.FileName)) {
		return c.load()
	}

	data, err := gitx.Repo{Dir: root}.Git(ctx, "show", commit+":"+config.FileName)
	if err != nil {
		return nil, fmt.Errorf("%w: no %s in %s, nor in %s", errUsage, config.FileName, root, short12(commit))
	}

	cfg, err := config.ParseHistory([]byte(data))
	if err != nil {
		return nil, fmt.Errorf("config of %s: %w", short12(commit), err)
	}

	return cfg, nil
}

func statusCommit(ctx context.Context, root, what string) (string, error) {
	if full, ok, err := (gitx.Repo{Dir: root}).Resolve(ctx, what); err == nil && ok {
		return full, nil
	}

	if commitHex(what) {
		return strings.ToLower(what), nil
	}

	return "", fmt.Errorf("%w: %s is no commit or branch of %s", errUsage, what, root)
}

func commitHex(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}

	return strings.Trim(strings.ToLower(s), "0123456789abcdef") == ""
}

// planParts reads a plan ref back: plans/<run>/<change>/<commit's start>.
func planParts(ref string) (run, change, short string, ok bool) { //nolint:nonamedreturns // three parts of one ref
	parts := strings.Split(strings.TrimPrefix(ref, report.AllPlans), "/")
	if len(parts) != 3 {
		return "", "", "", false
	}

	name, err := report.NameFromRef(parts[1])
	if err != nil {
		return "", "", "", false
	}

	return parts[0], name, parts[2], true
}

func stateWord(state string) string {
	switch state {
	case plugin.StateSuccess:
		return "passed"
	case plugin.StateFailure:
		return "failed"
	case plugin.StateError:
		return "errored"
	default:
		return plugin.StatePending
	}
}

func writeStatus(stdout io.Writer, lines []StatusLine, asked, commit string, asJSON bool) error {
	var b bytes.Buffer

	if asJSON {
		if lines == nil {
			lines = []StatusLine{}
		}

		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")

		if err := enc.Encode(lines); err != nil {
			return fmt.Errorf("encode: %w", err)
		}

		return emit(stdout, &b)
	}

	if len(lines) == 0 {
		fmt.Fprintf(&b, "pipelinegen: no run planned %s (%s)\n", asked, short12(commit))
	}

	for _, l := range lines {
		fmt.Fprintf(&b, "pipelinegen: %s %s %s: %s", l.Run, l.Change, short12(l.Commit), l.State)

		if l.Description != "" {
			fmt.Fprintf(&b, " (%s)", l.Description)
		}

		b.WriteString("\n")
	}

	return emit(stdout, &b)
}

func statusExit[R any](runs map[string]R, run string, lines []StatusLine) error {
	if _, known := runs[run]; run != "" && !known && len(lines) == 0 {
		return exitCodeError(statusPending)
	}

	// a superseded build ran nothing: alone, the commit has no run.
	code := statusNoRun
	if slices.ContainsFunc(lines, func(l StatusLine) bool { return l.State != stateSuperseded }) {
		code = statusPassed
	}

	for _, l := range lines {
		switch l.State {
		case "failed", "errored":
			code = statusFailed
		case plugin.StatePending:
			if code != statusFailed {
				code = statusPending
			}
		}
	}

	if code == statusPassed {
		return nil
	}

	return exitCodeError(code)
}
