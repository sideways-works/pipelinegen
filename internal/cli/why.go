package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

type whyPlan struct {
	Base       string                `json:"base"`
	Head       string                `json:"head"`
	All        bool                  `json:"all"`
	Reason     string                `json:"reason"`
	Narrowing  int                   `json:"narrowing"`
	Narrowings []report.NarrowingSet `json:"narrowings"`
	Suites     []whySuite            `json:"suites"`
	Plugins    *report.Plugins       `json:"plugins"`
	Lists      *lists.Source         `json:"lists"`
	Warnings   []string              `json:"warnings"`

	// unread says the lists couldn't be read, so every recorded slice was bound to everything.
	unread bool
}

type whySuite struct {
	Suite      string                       `json:"suite"`
	All        bool                         `json:"all"`
	Reason     string                       `json:"reason"`
	Selected   []whySelected                `json:"selected"`
	Reasons    map[string][]affected.Reason `json:"reasons"`
	Untouched  []string                     `json:"untouched"`
	Removed    []string                     `json:"removed"`
	Narrowed   []report.Narrowed            `json:"narrowed"`
	ObservedBy []string                     `json:"observed_by"`
	Unlisted   []string                     `json:"unlisted"`
}

type whySelected struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// Answer is why's answer for one slice in one suite.
type Answer struct {
	Suite string `json:"suite"`
	Slice string `json:"slice"`
	Label string `json:"label"`
	Ran   bool   `json:"ran"`
	// Reasons say why it ran, or why not: a plan's reasons, or one of
	// untouched, narrowed, sampled-out and removed.
	Reasons    []WhyReason     `json:"reasons"`
	Plugins    *report.Plugins `json:"plugins,omitempty"`
	ObservedBy []string        `json:"observed_by,omitempty"`
	// Files, for a kept run's slice that failed, say what it kept to show
	// why and how to fetch it.
	Files *FilesNote `json:"failure_files,omitempty"`
}

// FilesNote is how many failure files a slice kept or dropped in a run,
// and the command that writes them out.
type FilesNote struct {
	Kept    int    `json:"kept"`
	Dropped int    `json:"dropped,omitempty"`
	Command string `json:"command"`
}

// WhyReason is one reason with the line a person reads.
type WhyReason struct {
	// Kind, What and Through are as in the plan's reasons.
	Kind    string   `json:"kind"`
	What    string   `json:"what"`
	Through []string `json:"through,omitempty"`
	// Hops are the plan's chain from the recorded unit to what changed.
	Hops []affected.Hop `json:"hops,omitempty"`
	// Narrowing and OffAt name the narrowing that left a slice out and the
	// lowest level at which it is off.
	Narrowing string `json:"narrowing,omitempty"`
	OffAt     int    `json:"off_at,omitempty"`
	Text      string `json:"text"`
}

const (
	whyUntouched  = "untouched"
	whyNarrowed   = "narrowed"
	whySampledOut = "sampled-out"
	whyRemoved    = "removed"
	whySampled    = "sampled"
	whyCannotTell = "cannot-tell"
)

var errNoSlice = errors.New("no such slice")

type whyFlags struct {
	common

	slice, suite, base, head, run, change, commit, cache, bazelDiff, plugins string
	asJSON, noLists, local, ids, verbose                                     bool
	noFill                                                                   *bool
}

func (w *whyFlags) register(fs *flag.FlagSet) {
	w.common.register(fs)
	textVar(fs, &w.slice, "slice", "", "the slice, by its name (a test file's path) or its label")
	textVar(fs, &w.suite, "suite", "", "only this suite, where several have the slice")
	textVar(fs, &w.base, "base", "", "plan again: the commit to compare with")
	textVar(fs, &w.head, "head", "HEAD", "plan again: the commit to plan")
	textVar(fs, &w.run, "run", "", "read the kept plan of this run")
	textVar(fs, &w.change, "change", "", "read the kept plan of this change")
	textVar(fs, &w.commit, "commit", "", "read the kept plan of this commit (default: the change's newest)")
	pathVar(fs, &w.cache, "cache", defaultCache(), "where work trees and hashes are kept, when planning again", reads|writes)
	pathVar(fs, &w.bazelDiff, "bazel-diff", "", "a bazel-diff binary to run instead of the pinned release, when planning again", readsFile)
	pathVar(fs, &w.plugins, "plugins", "", "the directory holding the configured sbom and correlator plugins, when planning again (default: tools/plugins under the root)", reads)
	fs.BoolVar(&w.asJSON, "json", false, "print JSON")
	fs.BoolVar(&w.ids, "ids", false, "show each unit's id beside its name")
	fs.BoolVar(&w.verbose, "verbose", false, "also say how many files each plugin answered from the cache")
	fs.BoolVar(&w.noLists, "no-lists", false, "when planning again, bind recorded slices to everything instead of reading the lists")
	fs.BoolVar(&w.local, "local", false, "read the lists, or a kept plan, from the local store `pipelinegen record` writes")
	w.noFill = fillFlag(fs)
}

func (w *whyFlags) mode() (bool, error) {
	kept := w.run != "" || w.change != ""

	switch {
	case w.slice == "":
		return false, fmt.Errorf("%w: --slice is needed", errUsage)
	case kept && w.base != "":
		return false, fmt.Errorf("%w: either --base and --head, or --run and --change", errUsage)
	case kept && (w.run == "" || w.change == ""):
		return false, fmt.Errorf("%w: --run and --change go together", errUsage)
	case !kept && w.base == "":
		return false, fmt.Errorf("%w: --base (to plan again) or --run and --change (a kept plan) is needed", errUsage)
	}

	return kept, nil
}

var errNoRemote = errors.New("no remote")

func runWhy(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var w whyFlags

	fs := flag.NewFlagSet("why", flag.ContinueOnError)
	w.register(fs)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	ctx = withFill(ctx, "why", *w.noFill)

	kept, err := w.mode()
	if err != nil {
		return err
	}

	cfg, err := w.load()
	if err != nil {
		return err
	}

	withOrigin(ctx, cfg, w.root)

	if w.suite != "" {
		if _, err := pickSuites(cfg, w.suite); err != nil {
			return err
		}
	}

	var (
		plan  *whyPlan
		title string
		id    report.ID
	)

	from, err := fromFlags(w.noLists, w.local)
	if err != nil {
		return err
	}

	if kept && from == listsNone {
		return fmt.Errorf("%w: --no-lists is for planning again", errUsage)
	}

	if kept && from == listsRemote && codeRemote(cfg) == "" {
		return fmt.Errorf("%w: a kept plan is read from the repository's remote: git.remote, the github section or the checkout's origin (or --local for a local recording's)", errNoRemote)
	}

	// the remote holds the plans the pipeline kept; planning again needs a
	// store only for the lists, which may come from the local store.
	store := listsStore(ctx, cfg, w.root, from, listsWarnings(cfg, os.Stderr), "why")
	if store != nil {
		defer closeStore(store, &err)
	}

	if kept {
		if store == nil {
			return fmt.Errorf("%w: no store holds kept plans here", errNoRemote)
		}

		plan, title, id, err = w.keptPlan(ctx, store)

		// A kept plan may hold suites the run never ran.
		if plan != nil {
			plan.Suites = slices.DeleteFunc(plan.Suites, func(s whySuite) bool { return !cfg.RunCovers(w.run, s.Suite) })
		}
	} else {
		plan, title, err = w.planAgain(ctx, cfg, store)
	}

	if err != nil {
		return err
	}

	showIDs, showCache = w.ids, w.verbose

	answers := failureFiles(ctx, store, id, explain(plan, w.suite, w.slice))
	if len(answers) == 0 {
		return fmt.Errorf("%w: %s is in no suite of %s", errNoSlice, w.slice, title)
	}

	return writeAnswers(stdout, answers, title, w.asJSON, plan.Warnings)
}

func writeAnswers(stdout io.Writer, answers []Answer, title string, asJSON bool, warnings []string) error {
	var b bytes.Buffer

	if asJSON {
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")

		if err := enc.Encode(answers); err != nil {
			return fmt.Errorf("encode: %w", err)
		}

		return emit(stdout, &b)
	}

	for _, a := range answers {
		// a plan says what it selected; whether the slice ran, why can't
		// know.
		verb := "was left out"
		if a.Ran {
			verb = "was selected"
		}

		fmt.Fprintf(&b, "%s %s %s %s:\n", a.Suite, a.Slice, verb, title)

		for _, r := range a.Reasons {
			fmt.Fprintf(&b, "  %s\n", r.Text)
		}

		if len(a.ObservedBy) > 0 {
			fmt.Fprintf(&b, "  observed by: %s\n", strings.Join(a.ObservedBy, ", "))
		}

		if f := a.Files; f != nil {
			fmt.Fprintf(&b, "  failure files: %d kept, %d dropped; %s writes them out and says why any were dropped\n", f.Kept, f.Dropped, f.Command)
		}
	}

	pluginLines(&b, answers[0].Plugins)

	for _, w := range warnings {
		fmt.Fprintf(&b, "warning: %s\n", secret.Redact(w))
	}

	return emit(stdout, &b)
}

func pluginLines(b *bytes.Buffer, p *report.Plugins) {
	if asked, cached := p.Totals(); showCache && asked+cached > 0 {
		fmt.Fprintf(b, "plugins: %d of %d files answered from the cache\n", cached, asked+cached)

		for _, c := range p.Calls {
			fmt.Fprintf(b, "  %s %s: %d asked, %d from the cache\n", c.Plugin, c.Call, c.Asked, c.Cached)
		}
	}

	if p == nil {
		return
	}

	for _, w := range p.Warnings {
		fmt.Fprintf(b, "warning: %s\n", secret.Redact(w))
	}
}

func (w *whyFlags) keptPlan(ctx context.Context, store *gitstore.Store) (*whyPlan, string, report.ID, error) {
	id, err := keptRun(ctx, store, w.run, w.change, w.commit)
	if err != nil {
		return nil, "", id, err
	}

	ref := id.PlanRef()

	got, ok, err := store.First(ctx, []string{ref})
	if err != nil {
		return nil, "", id, err
	}

	if !ok {
		return nil, "", id, fmt.Errorf("%w: %s %s at %s has no kept plan", errNoPlan, w.run, w.change, short12(id.Commit))
	}

	data, ok, err := store.File(ctx, got.Commit, "plan.json")
	if err != nil {
		return nil, "", id, err
	}

	if !ok {
		return nil, "", id, fmt.Errorf("%w: %s holds no plan.json", errNoPlan, ref)
	}

	var p whyPlan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, "", id, fmt.Errorf("plan of %s %s: %w", w.run, w.change, err)
	}

	return &p, fmt.Sprintf("in %s %s at %s", w.run, w.change, short12(id.Commit)), id, nil
}

// failureFiles notes, for each answer whose slice kept failure files in a kept run, how many and how to
// fetch them; a plan made again, a swept run or a store that can't say notes nothing.
func failureFiles(ctx context.Context, store *gitstore.Store, id report.ID, answers []Answer) []Answer {
	if id.Run == "" || store == nil {
		return answers
	}

	for i, a := range answers {
		if !a.Ran {
			continue
		}

		list, err := report.RunFiles(ctx, store, id, a.Suite, a.Slice)
		if err != nil || len(list) != 1 {
			continue
		}

		answers[i].Files = &FilesNote{
			Kept: len(list[0].Files), Dropped: len(list[0].Dropped),
			Command: report.FilesCommand(id.Run, id.Change, id.Commit, a.Slice),
		}
	}

	return answers
}

func keptRun(ctx context.Context, store *gitstore.Store, run, change, commit string) (report.ID, error) {
	id := report.ID{Run: run, Change: change, Commit: commit}

	if commit != "" {
		if err := id.Check(); err != nil {
			return id, fmt.Errorf("%w: %w", errUsage, err)
		}

		return id, nil
	}

	plans, err := store.List(ctx, "refs/pipelinegen/plans/"+run+"/"+report.RefPart(change)+"/")
	if err != nil {
		return id, err
	}

	var newest gitstore.Ref

	for _, r := range plans {
		if newest.Name == "" || r.Time.After(newest.Time) || r.Time.Equal(newest.Time) && r.Name > newest.Name {
			newest = r
		}
	}

	if newest.Name == "" {
		return id, fmt.Errorf("%w: %s %s has no kept plan", errNoPlan, run, change)
	}

	id.Commit = newest.Name[strings.LastIndex(newest.Name, "/")+1:]

	return id, nil
}

func (w *whyFlags) planAgain(ctx context.Context, cfg *config.Config, store *gitstore.Store) (*whyPlan, string, error) {
	root := absRoot(w.root)
	repo := gitx.Repo{Dir: root}

	base, ok, err := repo.Resolve(ctx, w.base)
	if err != nil {
		return nil, "", err
	}

	if !ok {
		return nil, "", fmt.Errorf("%w: no commit %s in %s", errUsage, w.base, root)
	}

	head, ok, err := repo.Resolve(ctx, w.head)
	if err != nil {
		return nil, "", err
	}

	if !ok {
		return nil, "", fmt.Errorf("%w: no commit %s in %s", errUsage, w.head, root)
	}

	pluginsAt := w.plugins
	if pluginsAt == "" {
		pluginsAt = filepath.Join(root, "tools", "plugins")
	}

	pluginsAt, err = planningPluginsDir(ctx, cfg, absOr(pluginsAt), root, pluginCache(w.cache))
	if err != nil {
		return nil, "", err
	}

	// As affected does, a plan whose lists can't be read binds the recorded slices to everything.
	var unread error

	plan, err := planCommitsBy(ctx, cfg, store, root, "", base, head, false, repoCache(ctx, w.cache, root), w.bazelDiff, pluginsAt, planExtra{unread: &unread})
	if err != nil {
		return nil, "", err
	}

	if unread != nil {
		plan.Warnings = append(plan.Warnings, unreadText(cfg, unread))
	}

	data, err := json.Marshal(plan)
	if err != nil {
		return nil, "", fmt.Errorf("plan: %w", err)
	}

	var p whyPlan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, "", fmt.Errorf("plan: %w", err)
	}

	p.unread = unread != nil

	return &p, fmt.Sprintf("from %s to %s", short12(base), short12(head)), nil
}

func unreadText(cfg *config.Config, err error) string {
	why, _, _ := strings.Cut(err.Error(), "\n")
	if !hasRecorded(cfg) {
		return "the lists couldn't be read (" + why + "); no suite is bound by recording, so this plan needs none"
	}

	return "the lists couldn't be read (" + why + "), so every slice bound by recording was bound to everything"
}

func labelName(label string) string {
	if p, ok := strings.CutPrefix(label, "@pipelinegen_deps//slices:"); ok {
		return p
	}

	return report.SliceName(label)
}

func observedBy(p *whyPlan, out []Answer) []Answer {
	for i := range out {
		for _, sp := range p.Suites {
			if sp.Suite == out[i].Suite {
				out[i].ObservedBy = sp.ObservedBy
			}
		}
	}

	return out
}

func explain(p *whyPlan, suite, slice string) []Answer {
	var out []Answer

	is := func(name, label string) bool { return slice == name || slice == label }

	for _, sp := range p.Suites {
		if suite != "" && sp.Suite != suite {
			continue
		}

		if i := slices.IndexFunc(sp.Selected, func(s whySelected) bool { return is(s.Name, s.Label) }); i >= 0 {
			sel := sp.Selected[i]
			out = append(out, Answer{Suite: sp.Suite, Slice: sel.Name, Label: sel.Label, Ran: true, Reasons: ranBecause(p, sp, sel.Name, sel.Label)})

			continue
		}

		for _, label := range sp.Untouched {
			if name := labelName(label); is(name, label) {
				out = append(out, Answer{Suite: sp.Suite, Slice: name, Label: label, Reasons: leftOut(p, sp, name, label)})
			}
		}

		// a slice a narrowing left out, whether or not the plan also
		// lists it as untouched.
		for _, n := range sp.Narrowed {
			for _, name := range n.Dropped {
				if is(name, name) && !slices.ContainsFunc(out, func(a Answer) bool { return a.Suite == sp.Suite && a.Slice == name }) {
					out = append(out, Answer{Suite: sp.Suite, Slice: name, Label: name, Reasons: leftOut(p, sp, name, name)})
				}
			}
		}

		for _, label := range sp.Removed {
			if name := labelName(label); is(name, label) {
				out = append(out, Answer{Suite: sp.Suite, Slice: name, Label: label, Reasons: []WhyReason{{
					Kind: whyRemoved, What: label,
					Text: "the base has it and the head doesn't: its test file or target was removed",
				}}})
			}
		}
	}

	for i := range out {
		out[i].Plugins = p.Plugins
		out[i].Reasons = append(out[i].Reasons, cannotTell(p, out[i].Suite, out[i].Slice)...)
	}

	return observedBy(p, out)
}

func cannotTell(p *whyPlan, suite, slice string) []WhyReason {
	if p.Lists == nil {
		return nil
	}

	var out []WhyReason

	for _, m := range p.Lists.CannotTell[suite+"/"+slice] {
		text := m.Plugin + " could not tell what it touched when it last recorded"
		if m.Reason != "" {
			text += " (" + secret.Redact(m.Reason) + ")"
		}

		if !m.Since.IsZero() {
			text += ", first on " + m.Since.UTC().Format("2006-01-02")
		}

		out = append(out, WhyReason{Kind: whyCannotTell, What: m.Plugin, Text: text + "; it is bound to everything until a recording tells again, and its list is the one from before"})
	}

	return out
}

func ranBecause(p *whyPlan, sp whySuite, name, label string) []WhyReason {
	var out []WhyReason

	switch {
	case p.All:
		out = append(out, WhyReason{Kind: affected.KindEverything, What: p.Reason, Text: "everything ran: " + p.Reason})
	case sp.All && sp.Reason != affected.ReachesEverySlice:
		out = append(out, WhyReason{Kind: affected.KindEverything, What: sp.Reason, Text: "every slice of " + sp.Suite + " ran: " + sp.Reason})
	}

	// a suite whose every slice ran for reasons of its own has no cause of
	// its own: each slice's reasons are the cause.
	whole := p.All || (sp.All && sp.Reason != affected.ReachesEverySlice)

	for _, r := range sp.Reasons[label] {
		if whole && r.Kind == affected.KindEverything {
			continue
		}

		text := reasonLine(r)
		if chain := chainText(r, sp.ObservedBy, showIDs); chain != "" {
			text = chain
		}

		// a slice bound to everything ran on this change, not everything
		// with it.
		if r.Kind == affected.KindEverything {
			text = boundLine(p, sp, name, r)
		}

		out = append(out, WhyReason{Kind: r.Kind, What: r.What, Through: r.Through, Hops: r.Hops, Text: text})
	}

	// a slice with reasons of its own runs whatever the sample; one without
	// ran because the sample kept it.
	for _, n := range sp.Narrowed {
		if len(sp.Reasons[label]) > 0 {
			break
		}

		if n.Kind == whySampled && !slices.Contains(n.Dropped, name) && !slices.Contains(n.Dropped, label) {
			out = append(out, WhyReason{
				Kind: whySampled, What: n.Reason,
				Narrowing: n.Narrowing, OffAt: n.OffAt,
				Text: fmt.Sprintf("sampled: %s; it is one of %d the sample kept", n.Reason, max(n.Reached-len(n.Dropped), 0)),
			})
		}
	}

	if len(out) == 0 {
		out = append(out, WhyReason{Kind: affected.KindTarget, What: label, Text: "the plan gives no reason"})
	}

	return out
}

func leftOut(p *whyPlan, sp whySuite, name, label string) []WhyReason {
	var out []WhyReason

	for _, n := range sp.Narrowed {
		if !slices.Contains(n.Dropped, name) && !slices.Contains(n.Dropped, label) {
			continue
		}

		r := WhyReason{Kind: whyNarrowed, What: n.Reason, Narrowing: n.Narrowing, OffAt: n.OffAt}

		if n.Kind == whySampled {
			r.Kind = whySampledOut
			r.Text = fmt.Sprintf("not in the sample: %s; the nightly runs it", n.Reason)
		} else {
			r.Text = fmt.Sprintf("the narrowing %s left it out: %s", n.Narrowing, n.Reason)
		}

		r.Text += levelText(p.Narrowing, n.OffAt)

		if lv, ok := report.SetBy(p.Narrowings, n.Narrowing, n.OffAt); ok {
			r.Text += "; " + lv.Text()
		}

		out = append(out, r)
	}

	if len(out) == 0 && slices.Contains(sp.Unlisted, name) {
		why := "it has no list yet"
		if p.unread {
			why = "the lists couldn't be read"
		}

		out = append(out, WhyReason{
			Kind: whyUntouched, What: label,
			Text: why + ", so it is bound to every unit the plugins list, and none of them changed; " +
				"a file no plugin claims reaches it only through its Bazel data and extra_deps",
		})
	}

	if len(out) == 0 {
		out = append(out, WhyReason{
			Kind: whyUntouched, What: label,
			Text: "nothing it is bound to changed",
		})
	}

	return out
}

func levelText(level, offAt int) string {
	if offAt == 0 {
		return ""
	}

	if level == 0 {
		return fmt.Sprintf("; it would have run at narrowing level %d%s", offAt, orAbove(offAt))
	}

	return fmt.Sprintf("; it would have run at narrowing level %d%s (the plan used %d)", offAt, orAbove(offAt), level)
}

func orAbove(level int) string {
	if level >= config.MaxNarrowing {
		return ""
	}

	return " or above"
}

func boundLine(p *whyPlan, sp whySuite, name string, r affected.Reason) string {
	if p.unread && slices.Contains(sp.Unlisted, name) {
		return "the lists couldn't be read, so it runs on any change"
	}

	if slices.Contains(sp.Unlisted, name) {
		return "it has no recording yet, so it runs on any change until a run records what it touches"
	}

	// it says on its own that it runs on every change.
	if r.What == affected.AloneReason {
		return r.What
	}

	return "it runs on any change: " + r.What
}

func inputLine(r affected.Reason) string {
	switch r.Kind {
	case affected.KindInputAdded:
		return r.What + " is a new input of it"
	case affected.KindInputRemoved:
		return r.What + " is no longer an input of it"
	}

	return "it depends on " + r.What + ", which this commit's tree lacks or bazel-diff doesn't hash, so whether it changed can't be told"
}

func newSliceLine(r affected.Reason) string {
	if r.What == affected.AddedSliceReason || r.What == affected.AddedSuiteReason {
		return "a new slice: " + r.What
	}

	return "a new slice: the base doesn't have it"
}

func reasonLine(r affected.Reason) string {
	var text string

	switch r.Kind {
	case affected.KindEverything:
		if r.What == affected.AloneReason {
			return r.What
		}

		return "everything ran: " + r.What
	case affected.KindNewSlice:
		return newSliceLine(r)
	case affected.KindTestAdded:
		text = "a test was added to it: " + r.What
	case affected.KindTestRemoved:
		text = "a test was removed from it: " + r.What
	case affected.KindTestChanged:
		text = "its test file changed: " + r.What
	case affected.KindExecuted:
		text = "it executed " + r.What + ", which changed"
	case affected.KindDepends:
		text = "it depends on " + r.What + ", which changed"
	case affected.KindOpens:
		text = r.What + " changed in an app it opens"
	case affected.KindTarget:
		text = "the target " + r.What + " changed"
	case affected.KindInputAdded, affected.KindInputRemoved, affected.KindInputUnknown:
		return inputLine(r)
	case affected.KindPlugin:
		text = entryWhat(r.What) + " changed"
	case affected.KindConfig:
		return "the config: " + r.What
	case affected.KindTextInput:
		return "it reads " + r.What + " as text (text_inputs), so a change no test running it as code could observe still selects it"
	case whySampled:
		return "sampled: " + r.What
	default:
		text = r.Kind + ": " + r.What
	}

	if len(r.Through) > 0 {
		text += ", through " + strings.Join(r.Through, " → ")
	}

	return text
}

// With no recorded suite no list could narrow anything, so listsWarnings discards what is said of them.
func listsWarnings(cfg *config.Config, w io.Writer) io.Writer {
	if !hasRecorded(cfg) {
		return io.Discard
	}

	return w
}
