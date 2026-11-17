package report

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// fakeGitHub keeps statuses and comments in memory; with jitter, every call
// waits a little so parallel writers interleave.
type fakeGitHub struct {
	mu       sync.Mutex
	statuses map[string][]CommitStatus // by sha and context, every write
	comments map[int][]IssueComment
	nextID   int64
	jitter   time.Duration
	fail     error
	calls    int
	posts    int // statuses and comment writes
	subjects map[string]string
}

func newFake() *fakeGitHub {
	return &fakeGitHub{statuses: map[string][]CommitStatus{}, comments: map[int][]IssueComment{}, nextID: 100, subjects: map[string]string{}}
}

var errFake = errors.New("github is down")

func (f *fakeGitHub) wait() {
	if f.jitter > 0 {
		time.Sleep(time.Duration(rand.Int64N(int64(f.jitter)))) //nolint:gosec // jitter, not a secret
	}
}

func (f *fakeGitHub) SetStatus(_ context.Context, sha string, st CommitStatus) error {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if f.fail != nil {
		return f.fail
	}

	now := time.Now()
	st.UpdatedAt = &now
	f.statuses[sha+" "+st.Context] = append(f.statuses[sha+" "+st.Context], st)
	f.posts++

	return nil
}

func (f *fakeGitHub) Statuses(_ context.Context, sha string) ([]CommitStatus, error) {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.fail != nil {
		return nil, f.fail
	}

	var out []CommitStatus

	for k, all := range f.statuses {
		if strings.HasPrefix(k, sha+" ") {
			out = append(out, all[len(all)-1])
		}
	}

	return out, nil
}

func (f *fakeGitHub) CommitSubject(_ context.Context, sha string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.subjects[sha], nil
}

func (f *fakeGitHub) status(sha, ctx string) (CommitStatus, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	all := f.statuses[sha+" "+ctx]
	if len(all) == 0 {
		return CommitStatus{}, false
	}

	return all[len(all)-1], true
}

// IssueComment is one comment the fake keeps on a pull request.
type IssueComment struct {
	ID        int64
	Body      string
	UpdatedAt time.Time
}

// Comment keeps one comment as the GitHub connector does: the oldest that
// starts with the marker, the others removed.
func (f *fakeGitHub) Comment(_ context.Context, pr int, marker, body string) error {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if f.fail != nil {
		return f.fail
	}

	normal := func(s string) string { return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), " \n\t") }

	var mine []int64

	for _, c := range f.comments[pr] {
		if b := normal(c.Body); strings.HasPrefix(b, marker+"\n") || b == marker {
			mine = append(mine, c.ID)
		}
	}

	slices.Sort(mine)

	if len(mine) == 0 {
		f.nextID++
		f.posts++
		f.comments[pr] = append(f.comments[pr], IssueComment{ID: f.nextID, Body: body, UpdatedAt: time.Now()})

		return nil
	}

	f.comments[pr] = slices.DeleteFunc(f.comments[pr], func(c IssueComment) bool { return c.ID != mine[0] && slices.Contains(mine, c.ID) })

	for i := range f.comments[pr] {
		if c := &f.comments[pr][i]; c.ID == mine[0] && normal(c.Body) != normal(body) {
			c.Body, c.UpdatedAt = body, time.Now()
			f.posts++
		}
	}

	return nil
}

func (f *fakeGitHub) bodies(pr int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, 0, len(f.comments[pr]))
	for _, c := range f.comments[pr] {
		out = append(out, c.Body)
	}

	return out
}

func bareRemote(t *testing.T) string {
	t.Helper()

	return storetest.Bare(t)
}

func testStore(t *testing.T, remote string) *gitstore.Store {
	t.Helper()

	s := storetest.Open(t, remote)

	return s
}

func testConfig(format string, reasoning bool) *config.Config {
	run := config.Run{
		Trigger: config.Trigger{PullRequests: &config.PullRequests{Base: "main"}},
		Record:  true, Report: config.RunReport{Reasoning: reasoning},
	}
	if format != "" {
		run.Report.Comment = &config.Comment{Format: format}
	}

	return &config.Config{
		GitHub: config.GitHub{Repository: "o/r", StatusPrefix: "pipelinegen"},
		Runs:   map[string]config.Run{"pr": run},
		Suites: map[string]config.Suite{
			"e2e":  {Bind: config.BindRecorded},
			"go":   {Bind: config.BindRecorded},
			"lint": {},
		},
	}
}

func writeResult(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return filepath.Join(dir, "result.json")
}

// TestMain runs every test on refs, and those that read and write the
// store again on a branch.
func TestMain(m *testing.M) {
	storetest.Both(m, "^(TestRunFromStartToFinish|TestAPassingRunPasses|TestARerunAfterTheSuiteEndedShowsAsRunning|"+
		"TestLastWriterShowsTheNewestState|TestABatchReportsInOneWrite|TestBatchEndClosesTheRest|TestStatesForARerun|"+
		"TestExtrasAreKeptAndCollected|TestAPassedBranchRunAttachesItsLists|TestBatchEndsThatMeetAreAllRecorded|"+
		"TestANightlyTracesFailuresToPullRequestsThatLanded|TestSweepRemovesWholeRunsOutOfKeep|"+
		"TestSweepCountsRunsByTheirPlansTime|TestAQuietBranchKeepsItsNewestLists|TestAKilledBatchLosesNothingItsEnsureCanRecover|TestARunWithNoHostLivesInTheStore|TestPlannersThatMeetLeaveOnePlan|"+
		"TestAPlanOfEverySuiteCountsTheRunsOwn|TestARunWhoseSuitesNothingReachesSucceeds|TestABranchRunOfSomeSuitesAttachesItsLists|"+
		"TestARoundThatFailedAttachesWhatPassed|TestARoundThatFailedMarksOnlyWhatPassed|TestARunEndedByHandAttachesWhatPassed|TestTheSweepKeepsAFailedRoundsLists|TestPassesAreTheNewestWithTestsRun)$")
}
