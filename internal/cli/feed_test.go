package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/piperun"
	"github.com/sideways-works/pipelinegen/internal/report"
)

// itemScript is the suite's each command: it logs the slice, sleeps for a slow or hanging one, and fails
// one whose name holds "bad", leaving a screenshot and four failures with long tails.
const itemScript = `name=$1 result=$2 files=$3
echo "$name" >> "$FEED_LOG"
case "$name" in
  *slow*) sleep 8 ;;
  *hang*) [ -n "$FEED_HANG" ] && sleep 30 ;;
  *quick*) sleep 0.6 ;;
esac
case "$name" in
  *bad*)
    mkdir -p "$files"; echo shot > "$files/shot.png"
    tail=$(head -c 9000 /dev/zero | tr '\0' x)
    f="{\"test\": \"it <breaks>\", \"tail\": \"$tail\"}"
    printf '{"passed": false, "failures": [%s, %s, %s, %s], "files": [{"path": "shot.png", "label": "screenshot"}]}' "$f" "$f" "$f" "$f" > "$result"
    exit 1 ;;
esac
exit 0
`

type feedRun struct {
	root, log string
	store     func() map[string]report.StateFile
}

// feedFixture is a pull request's run of e2e, a suite run by the command driver, planned with the
// given slices, whose pool file plan --emit would have written; the test works in a directory of its own.
func feedFixture(t *testing.T, names ...string) feedRun {
	t.Helper()

	root, _, s := runEnv(t)
	writeFile(t, filepath.Join(root, config.FileName), strings.Replace(reportConfig, "    run_slice: ci/tasks/e2e-slice.yml\n",
		"    steps:\n      - {run: command, with: {each: [sh, ci/item.sh, '{name}', '{result}', '{files}']}}\n", 1))
	writeFile(t, filepath.Join(root, "ci", "item.sh"), itemScript)

	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}

	var sel, pool []string
	for _, n := range names {
		sel = append(sel, fmt.Sprintf(`{"name": %q, "label": "//e2e:%s"}`, n, n))
		pool = append(pool, fmt.Sprintf(`{"name": %q, "label": "//e2e:%s"}`, n, n))
	}

	if err := report.WritePlan(t.Context(), s, id, []byte(`{"suites": [{"suite": "e2e", "selected": [`+strings.Join(sel, ", ")+`], "whole_suite": []}]}`)); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	writeFile(t, "pools/e2e-slices.json", "["+strings.Join(pool, ", ")+"]")

	log := filepath.Join(t.TempDir(), "items")
	t.Setenv("FEED_LOG", log)

	old := feedEvery
	feedEvery = 5 * time.Millisecond

	t.Cleanup(func() { feedEvery = old })

	states := func() map[string]report.StateFile {
		t.Helper()

		got, err := report.StateFiles(t.Context(), s, id, "e2e")
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	return feedRun{root: root, log: log, store: states}
}

// feeder plays piperun's agent for one session: it writes the chunks, each only once every item of the
// one before has a line (with early, chunk 2 at once with chunk 1), then the end, and gives up after a
// minute.
func feeder(ctx context.Context, dir string, chunks [][]string, early bool) <-chan error {
	errc := make(chan error, 1)

	write := func(c int) error {
		ch := piperun.Chunk{Chunk: c, Items: []piperun.Item{}}
		for _, k := range chunks[c-1] {
			ch.Items = append(ch.Items, piperun.Item{Key: k})
		}

		data, err := json.Marshal(ch)
		if err != nil {
			return fmt.Errorf("chunk: %w", err)
		}

		tmp := filepath.Join(dir, ".chunk")
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return fmt.Errorf("chunk: %w", err)
		}

		if err := os.Rename(tmp, filepath.Join(dir, strconv.Itoa(c)+".json")); err != nil {
			return fmt.Errorf("chunk: %w", err)
		}

		return nil
	}

	reported := func(keys []string) error {
		for start := time.Now(); time.Since(start) < time.Minute; time.Sleep(5 * time.Millisecond) {
			if ctx.Err() != nil {
				return fmt.Errorf("feeder: %w", ctx.Err())
			}

			got, err := piperun.Reported(filepath.Join(dir, piperun.ResultsFile))
			if err != nil {
				return fmt.Errorf("feeder: %w", err)
			}

			if !slices.ContainsFunc(keys, func(k string) bool { return got[k] == "" }) {
				return nil
			}
		}

		return errors.New("the session left items of its chunk without a line") //nolint:err113 // the test's own
	}

	go func() {
		errc <- func() error {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return fmt.Errorf("feed: %w", err)
			}

			for c := 1; c <= len(chunks); c++ {
				if !early || c <= 2 {
					if err := write(c); err != nil {
						return err
					}
				}

				if early && c == 1 && len(chunks) > 1 {
					if err := write(2); err != nil {
						return err
					}
				}

				if err := reported(chunks[c-1]); err != nil {
					return err
				}

				if early && c >= 2 && c < len(chunks) {
					if err := write(c + 1); err != nil {
						return err
					}
				}
			}

			if err := os.WriteFile(filepath.Join(dir, piperun.EndFile), nil, 0o600); err != nil {
				return fmt.Errorf("end: %w", err)
			}

			return nil
		}()
	}()

	return errc
}

type resultLine struct {
	Key     string   `json:"key"`
	Start   bool     `json:"start"`
	Requeue bool     `json:"requeue"`
	Reason  string   `json:"reason"`
	Outcome string   `json:"outcome"`
	Summary string   `json:"summary"`
	Files   []string `json:"files"`
}

func resultLines(t *testing.T, dir string) []resultLine {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, piperun.ResultsFile))
	if err != nil {
		t.Fatal(err)
	}

	var out []resultLine

	for l := range bytes.Lines(data) {
		if len(l) > piperun.MaxLine || l[len(l)-1] != '\n' {
			t.Fatalf("a result line of %d bytes: %.100s", len(l), l)
		}

		var r resultLine
		if err := json.Unmarshal(l, &r); err != nil {
			t.Fatal(err)
		}

		out = append(out, r)
	}

	return out
}

func (r resultLine) String() string {
	switch {
	case r.Start:
		return "start " + r.Key
	case r.Requeue:
		return "requeue " + r.Key
	}

	return "end " + r.Key + " " + r.Outcome
}

func lineNames(lines []resultLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.String())
	}

	return out
}

func runSession(ctx context.Context, t *testing.T, f feedRun, feed string) (string, string, int) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := Run(ctx, []string{"slice", "run", "--root", f.root, "--feed", feed}, &stdout, &stderr)

	return stdout.String(), stderr.String(), code
}

func TestASessionRunsItsChunksInOrderAndReportsEachSlice(t *testing.T) {
	f := feedFixture(t, "a", "bad", "c")

	errc := feeder(t.Context(), "feed", [][]string{{"a", "bad"}, {"c"}}, false)

	stdout, stderr, code := runSession(t.Context(), t, f, "feed")
	if code != 0 || !strings.Contains(stdout, "session 1: ran 3 slices, 1 of them not passed") {
		t.Fatalf("slice run --feed: %d %s\n%s", code, stdout, stderr)
	}

	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	lines := resultLines(t, "feed")
	if got := lineNames(lines); !slices.Equal(got, []string{"start a", "end a passed", "start bad", "end bad failed", "start c", "end c passed"}) {
		t.Fatalf("result lines: %q", got)
	}

	bad := lines[3]
	if !strings.Contains(bad.Summary, "it <breaks>: xxx") || !strings.HasSuffix(bad.Summary, "[cut]") || !slices.Equal(bad.Files, []string{"bad/shot.png"}) {
		t.Fatalf("the failed slice's end: %d %q %q", len(bad.Summary), bad.Summary[:min(80, len(bad.Summary))], bad.Files)
	}

	if data, err := os.ReadFile("feed/files/bad/shot.png"); err != nil || string(data) != "shot\n" {
		t.Fatalf("its screenshot: %q %v", data, err)
	}

	ends, err := report.JournalIn("result").Ends()
	if err != nil || len(ends) != 3 {
		t.Fatalf("the journal's ends: %+v %v", ends, err)
	}

	// nothing is written to the store but by the bookkeeping
	if got := f.store(); len(got) != 0 {
		t.Fatalf("the session wrote states: %v", got)
	}
}

func TestASessionNeverStartsTheNextChunkBeforeReportingThisOne(t *testing.T) {
	f := feedFixture(t, "a", "b", "c", "d")

	errc := feeder(t.Context(), "feed", [][]string{{"a", "b"}, {"c"}, {"d"}}, true)

	if stdout, stderr, code := runSession(t.Context(), t, f, "feed"); code != 0 {
		t.Fatalf("slice run --feed: %d %s\n%s", code, stdout, stderr)
	}

	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	got := lineNames(resultLines(t, "feed"))
	if !slices.Equal(got, []string{"start a", "end a passed", "start b", "end b passed", "start c", "end c passed", "start d", "end d passed"}) {
		t.Fatalf("result lines: %q", got)
	}
}

func TestEachSliceHasTheLimitNotTheSession(t *testing.T) {
	f := feedFixture(t, "quick1", "slow", "quick2", "quick3")

	old := feedMinute
	feedMinute = 2 * time.Second

	t.Cleanup(func() { feedMinute = old })
	t.Setenv("PIPELINEGEN_LIMIT", "suites.e2e.minutes=1")

	errc := feeder(t.Context(), "feed", [][]string{{"quick1", "slow"}, {"quick2", "quick3"}}, false)

	start := time.Now()

	stdout, stderr, code := runSession(t.Context(), t, f, "feed")
	if code != 0 {
		t.Fatalf("slice run --feed: %d %s\n%s", code, stdout, stderr)
	}

	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	lines := resultLines(t, "feed")
	if got := lineNames(lines); !slices.Equal(got, []string{
		"start quick1", "end quick1 passed", "start slow", "end slow errored", "start quick2", "end quick2 passed", "start quick3", "end quick3 passed",
	}) {
		t.Fatalf("result lines: %q", got)
	}

	if !strings.Contains(lines[3].Summary, "stopped at its time limit of 1 minute (suites.e2e.minutes)") {
		t.Fatalf("the slow slice's end: %q", lines[3].Summary)
	}

	// the session outlived one slice's limit, and its slices passed
	if took := time.Since(start); took < 3*time.Second || took > 10*time.Second {
		t.Fatalf("the session took %s", took)
	}
}

func TestASliceTheWorkerCantFitIsRequeuedBeforeItsStart(t *testing.T) {
	f := feedFixture(t, "a", "big", "c")
	writeFile(t, "pools/e2e-slices.json", `[{"name": "a", "label": "//e2e:a", "predicted_mb": 500}, {"name": "big", "label": "//e2e:big", "predicted_mb": 6600}, {"name": "c", "label": "//e2e:c"}]`)
	fakeMemory(t, 3000, 50*time.Millisecond)

	errc := feeder(t.Context(), "feed", [][]string{{"a", "big"}, {"c"}}, false)

	stdout, stderr, code := runSession(t.Context(), t, f, "feed")
	if code != 0 {
		t.Fatalf("slice run --feed: %d %s\n%s", code, stdout, stderr)
	}

	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	lines := resultLines(t, "feed")
	if got := lineNames(lines); !slices.Equal(got, []string{"start a", "end a passed", "requeue big", "start c", "end c passed"}) {
		t.Fatalf("result lines: %q", got)
	}

	if !strings.Contains(lines[2].Reason, "needs ~6600 MB more for a peak of 6600 MB, 3000 MB free") {
		t.Fatalf("the reason: %q", lines[2].Reason)
	}

	if got := drivenLog(t, f.log); slices.Contains(got, "big") {
		t.Fatalf("a requeued slice ran: %q", got)
	}
}

func TestASessionStartedAgainTakesOnlyWhatItIsFed(t *testing.T) {
	f := feedFixture(t, "a", "hang", "b")

	t.Setenv("FEED_HANG", "1")

	ctx, cancel := context.WithCancel(t.Context())
	errc := feeder(ctx, "feed", [][]string{{"a", "hang"}}, false)

	// the worker is lost while hang runs
	go func() {
		for ctx.Err() == nil {
			if got, _ := piperun.Reported("feed/" + piperun.ResultsFile); got["hang"] == piperun.Started { //nolint:errcheck // read again
				cancel()
			}

			time.Sleep(5 * time.Millisecond)
		}
	}()

	if _, _, code := runSession(ctx, t, f, "feed"); code == 0 {
		t.Fatal("a lost session exited 0")
	}

	<-errc

	// what piperun restores: the result directory as uploaded, and a feed holding the lines of before
	t.Setenv("FEED_HANG", "")
	writeFile(t, "feed2/"+piperun.ResultsFile, string(mustRead(t, "feed/"+piperun.ResultsFile)))

	errc = feeder(t.Context(), "feed2", [][]string{{"a", "b"}, {"hang"}}, false)

	stdout, stderr, code := runSession(t.Context(), t, f, "feed2")
	if code != 0 {
		t.Fatalf("the session started again: %d %s\n%s", code, stdout, stderr)
	}

	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	got := lineNames(resultLines(t, "feed2"))
	if !slices.Equal(got, []string{"start a", "end a passed", "start hang", "start b", "end b passed", "start hang", "end hang passed"}) {
		t.Fatalf("result lines: %q", got)
	}

	ends, err := report.JournalIn("result").Ends()
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, ev := range ends {
		names = append(names, ev.Slice)
	}

	if !slices.Equal(names, []string{"a", "b", "hang"}) {
		t.Fatalf("the journal's ends: %q", names)
	}

	if n := bytes.Count(mustRead(t, "result/.pipelinegen/journal.jsonl"), []byte(`"event":"end","at"`)); n != 3 {
		t.Fatalf("%d ends in the journal, want one each", n)
	}

	// hang's lost part is gone: its time is its second run's alone
	if hang := ends[2]; hang.Seconds > 5 || len(hang.Parts) > 1 {
		t.Fatalf("hang kept its lost part: %+v", hang)
	}

	if open, err := report.JournalIn("result").Unended(); err != nil || len(open) != 0 {
		t.Fatalf("still open: %+v %v", open, err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestFeedArgsAndTheSessionsBatch(t *testing.T) {
	for args, want := range map[string]bool{
		"run --feed f": true, "run -feed=f": true, "run --root s --feed f": true,
		"run --root s": false, "run -- --feed": false, "start --feed f": false,
	} {
		a := strings.Fields(args)
		if got := limitsEachSlice(sliceCommand, a); got != want {
			t.Errorf("%q: %v", args, got)
		}
	}

	if limitsEachSlice("report", []string{"run", "--feed", "f"}) {
		t.Error("another command")
	}

	t.Setenv(envBatch, "")
	t.Setenv(envSession, "4")

	if got := batchFromEnv(); got != "4" {
		t.Fatalf("the session's number: %q", got)
	}

	t.Setenv(envBatch, "2")

	if got := batchFromEnv(); got != "2" {
		t.Fatalf("PIPELINEGEN_BATCH first: %q", got)
	}
}

// session runs a session of the fixture's slices through to its end.
func (f feedRun) session(t *testing.T, chunks ...[]string) {
	t.Helper()

	errc := feeder(t.Context(), "feed", chunks, false)

	if stdout, stderr, code := runSession(t.Context(), t, f, "feed"); code != 0 {
		t.Fatalf("slice run --feed: %d %s\n%s", code, stdout, stderr)
	}

	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func stateNames(got map[string]report.StateFile) map[string]report.State {
	out := map[string]report.State{}
	for k, v := range got {
		out[k] = v.State
	}

	return out
}

func TestAFlushWritesOnlyConfirmedSlicesAndAgainTheSame(t *testing.T) {
	f := feedFixture(t, "a", "bad", "c")
	f.session(t, []string{"a", "bad"}, []string{"c"})

	journal := mustRead(t, "result/.pipelinegen/journal.jsonl")

	// piperun is still writing c's line
	writeFile(t, "confirmed", "a\nbad\nc")

	flush := func() {
		t.Helper()

		if stdout, stderr, code := run(t, "report", "flush", "--root", f.root, "--confirmed", "confirmed"); code != 0 {
			t.Fatalf("report flush: %d %s\n%s", code, stdout, stderr)
		}
	}

	flush()

	if got := stateNames(f.store()); len(got) != 2 || got["a"] != report.Passed || got["bad"] != report.Failed {
		t.Fatalf("after the first flush: %v", got)
	}

	flush()

	if got := stateNames(f.store()); len(got) != 2 || got["a"] != report.Passed || got["bad"] != report.Failed {
		t.Fatalf("flushed twice: %v", got)
	}

	writeFile(t, "confirmed", "a\nbad\nc\n")
	flush()

	if got := stateNames(f.store()); len(got) != 3 || got["c"] != report.Passed {
		t.Fatalf("after c was confirmed: %v", got)
	}

	if !bytes.Equal(journal, mustRead(t, "result/.pipelinegen/journal.jsonl")) {
		t.Fatal("a flush wrote to the session's journal")
	}
}

func TestAFlushEveryPeriodWritesWhatIsConfirmedSince(t *testing.T) {
	f := feedFixture(t, "a", "c")
	f.session(t, []string{"a", "c"})
	writeFile(t, "confirmed", "a\n")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan string, 1)

	go func() {
		var stdout, stderr bytes.Buffer

		code := Run(ctx, []string{"report", "flush", "--root", f.root, "--confirmed", "confirmed", "--every", "20ms"}, &stdout, &stderr)
		done <- fmt.Sprintf("%d %s %s", code, stdout.String(), stderr.String())
	}()

	wait := func(n int) {
		t.Helper()

		for start := time.Now(); len(f.store()) < n; time.Sleep(20 * time.Millisecond) {
			if time.Since(start) > 30*time.Second {
				t.Fatalf("flushed %v", stateNames(f.store()))
			}
		}
	}

	wait(1)
	writeFile(t, "confirmed", "a\nc\n")
	wait(2)
	cancel()

	if got := <-done; !strings.HasPrefix(got, "0 flushing stopped") {
		t.Fatalf("stopped: %s", got)
	}
}

func TestASessionsEndClosesConfirmedSlicesAndLeavesRequeuedOnesOpen(t *testing.T) {
	f := feedFixture(t, "a", "big", "c", "lost")
	writeFile(t, "pools/e2e-slices.json", `[{"name": "a", "label": "//e2e:a"}, {"name": "big", "label": "//e2e:big", "predicted_mb": 6600}, {"name": "c", "label": "//e2e:c"}, {"name": "lost", "label": "//e2e:lost"}]`)
	fakeMemory(t, 3000, 50*time.Millisecond)

	f.session(t, []string{"a", "big"}, []string{"c"})

	// piperun ended lost itself, whose session never reported it
	writeFile(t, "confirmed", "a\nc\nlost\n")
	writeFile(t, "requeued", "big\n")

	stdout, stderr, code := run(t, "report", "batch-end", "--root", f.root, "--confirmed", "confirmed", "--requeued", "requeued")
	if code != 1 || !strings.Contains(stderr, "batch 1: lost errored") || !strings.Contains(stdout, "session 1: 1 slice handed back to the pool, left open: big") {
		t.Fatalf("batch-end: %d %s\n%s", code, stdout, stderr)
	}

	got := f.store()
	if st := stateNames(got); len(st) != 3 || st["a"] != report.Passed || st["c"] != report.Passed || st["lost"] != report.Errored {
		t.Fatalf("states: %v", st)
	}

	if lost := got["lost"]; lost.Label != "//e2e:lost" || len(lost.Failures) == 0 || !strings.Contains(lost.Failures[0].Tail, "the session never reported its end") {
		t.Fatalf("lost: %+v", lost)
	}

	writeFile(t, "confirmed", "")
	writeFile(t, "requeued", "big\n")

	if stdout, stderr, code := run(t, "report", "batch-end", "--root", f.root, "--confirmed", "confirmed", "--requeued", "requeued"); code != 0 || !strings.Contains(stdout, "session 1: no slice confirmed, nothing to close") {
		t.Fatalf("a session with nothing confirmed: %d %s %s", code, stdout, stderr)
	}
}
