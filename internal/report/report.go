// Package report turns a run's slice results into the one result per suite
// that reaches a pull request or commit.
package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

type State string

const (
	Pending State = "pending"
	Running State = "running"
	Passed  State = "passed"
	Failed  State = "failed"
	Errored State = "errored"
	// NotRun is a slice whose batch ended before any of its slices
	// started: it neither passed nor failed, and a rerun runs it.
	NotRun    State = "not run"
	Untouched State = "untouched"
)

// Failure is one failing test and the last lines of its output.
type Failure struct {
	Test string `json:"test"`
	Tail string `json:"tail"`
}

// Reason is one cause of a slice's selection, as planning records it.
type Reason struct {
	Kind    string   `json:"kind"`
	What    string   `json:"what"`
	Through []string `json:"through"`
}

// Cause is a reason several selected slices of a suite share, named once
// with how many slices it selects.
type Cause struct {
	Kind    string   `json:"kind"`
	What    string   `json:"what"`
	Through []string `json:"through"`
	Slices  int      `json:"slices"`
}

func (c Cause) of(r Reason) bool {
	if r.Kind != c.Kind || r.What != c.What {
		return false
	}

	return r.Kind == "depends" || r.Kind == "executed" || slices.Equal(r.Through, c.Through)
}

// Narrowed is a selection a plan sampled or narrowed below what the change reaches; the
// nightly runs everything, so it catches what one left out.
type Narrowed struct {
	Kind      string   `json:"kind"`
	Narrowing string   `json:"narrowing"`
	OffAt     int      `json:"off_at"`
	Reason    string   `json:"reason"`
	Reached   int      `json:"reached"`
	Dropped   []string `json:"dropped"`
}

// NarrowingSet is a narrowing whose level a plan's config set: OffAt in
// force in place of Declared, the level its declarer (By) gives.
type NarrowingSet struct {
	Name     string `json:"name"`
	OffAt    int    `json:"off_at"`
	Declared int    `json:"declared"`
	By       string `json:"by"`
	Note     string `json:"note"`
}

// Text says where the level came from.
func (n NarrowingSet) Text() string {
	return fmt.Sprintf("off from %d by the config (narrowings.%s: %s); %s says %d",
		n.OffAt, n.Name, strings.TrimSuffix(oneLine(n.Note), "."), n.By, n.Declared)
}

// SetBy finds the level the config set for a narrowing, when it is offAt, the level a slice was
// left out by: a plan united with the base's may hold an entry of the base's level.
func SetBy(set []NarrowingSet, narrowing string, offAt int) (NarrowingSet, bool) {
	i := slices.IndexFunc(set, func(n NarrowingSet) bool { return n.Name == narrowing && n.OffAt == offAt })
	if i < 0 {
		return NarrowingSet{}, false
	}

	return set[i], true
}

// Trace ties a slice that failed in a run of everything to an earlier change
// whose plan left it out.
type Trace struct {
	Suite, Slice                 string
	Run, Change, Commit, Subject string
	Kind, Narrowing              string
	OffAt                        int
	// Set is the config's level of the narrowing in that plan, when it set
	// one.
	Set *NarrowingSet
}

// Net is what a run of everything says about failures earlier runs left
// out.
type Net struct {
	Traces []Trace
	Alarm  int
}

// Failing counts the failed slices that trace to a narrowed change.
func (n *Net) Failing() int {
	seen := map[string]bool{}
	for _, t := range n.Traces {
		seen[t.Suite+"/"+t.Slice] = true
	}

	return len(seen)
}

// Raise is the narrowing level to raise to, when the traced failures are
// above the alarm: the lowest that would have run them all.
func (n *Net) Raise() (int, bool) {
	if n == nil || n.Failing() <= n.Alarm {
		return 0, false
	}

	least := map[string]int{}

	for _, t := range n.Traces {
		k := strings.Join([]string{t.Run, t.Change, t.Commit, t.Suite, t.Slice}, "\x00")
		// a level of 0 is a narrowing nobody declares: no level of its own
		if l, ok := least[k]; !ok || l == 0 || (t.OffAt > 0 && t.OffAt < l) {
			least[k] = t.OffAt
		}
	}

	level := 0
	for _, l := range least {
		level = max(level, l)
	}

	return level, true
}

// SetLevels name the config's keys that set the level of a narrowing a
// traced failure was left out by, sorted.
func (n *Net) SetLevels() []string {
	var out []string

	for _, t := range n.Traces {
		if t.Set != nil {
			out = append(out, "`narrowings."+t.Narrowing+".off_at`")
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

type Slice struct {
	// Name is how the slice is known (a test file's path); when empty, its
	// label stands for it.
	Name       string           `json:"name,omitempty"`
	Label      string           `json:"label"`
	Group      string           `json:"group,omitempty"`
	State      State            `json:"state"`
	URL        string           `json:"url,omitempty"`
	Failures   []Failure        `json:"failures,omitempty"`
	Reasons    []Reason         `json:"reasons,omitempty"`
	NoTests    bool             `json:"no_tests,omitempty"`
	Recorded   bool             `json:"recorded,omitempty"`
	CannotTell []recording.Mark `json:"cannot_tell,omitempty"`
	PeakMB     int64            `json:"peak_mb,omitempty"`
	// Files is how many failure files the slice's ref keeps.
	Files int `json:"files,omitempty"`
}

type Suite struct {
	Name      string     `json:"name"`
	Reason    string     `json:"reason,omitempty"`
	Slices    []Slice    `json:"slices"`
	Untouched []string   `json:"untouched"`
	Removed   []string   `json:"removed,omitempty"`
	Narrowed  []Narrowed `json:"narrowed,omitempty"`
	Causes    []Cause    `json:"causes,omitempty"`
}

type Run struct {
	Name         string                      `json:"name"`
	Change       string                      `json:"change,omitempty"`
	Commit       string                      `json:"commit"`
	URL          string                      `json:"url,omitempty"`
	Suites       []Suite                     `json:"suites"`
	Reasoning    bool                        `json:"reasoning,omitempty"`
	Net          *Net                        `json:"net,omitempty"`
	Plugins      *Plugins                    `json:"plugins,omitempty"`
	PlanWarnings []string                    `json:"plan_warnings,omitempty"`
	Rebuild      *Rebuild                    `json:"rebuild,omitempty"`
	All          bool                        `json:"all,omitempty"`
	Marked       map[string][]recording.Mark `json:"marked,omitempty"`
	Narrowings   []NarrowingSet              `json:"narrowings,omitempty"`
	Now          time.Time                   `json:"-"`
}

type Plugins struct {
	Calls    []PluginCall `json:"calls,omitempty"`
	Warnings []string     `json:"warnings,omitempty"`
}

// PluginCall is one plugin call's share of a plan: Asked is how many files
// were sent to the plugin, Cached how many the cache answered instead.
type PluginCall struct {
	Plugin string `json:"plugin"`
	Call   string `json:"call"`
	Asked  int    `json:"asked"`
	Cached int    `json:"cached"`
}

func (p *Plugins) Totals() (asked, cached int) {
	if p == nil {
		return 0, 0
	}

	for _, c := range p.Calls {
		asked += c.Asked
		cached += c.Cached
	}

	return asked, cached
}

// Warnings are everything the plan warns of, its own and its plugins',
// masked.
func (r Run) Warnings() []string {
	out := make([]string, 0, len(r.PlanWarnings))
	for _, w := range r.PlanWarnings {
		out = append(out, secret.Redact(w))
	}

	out = append(out, r.Plugins.warnings()...)

	// a rebuild that fails the run says so on its own, ahead of these
	if !r.Rebuild.failing() {
		out = append(out, r.Rebuild.Lines()...)
	}

	return out
}

func (p *Plugins) warnings() []string {
	if p == nil {
		return nil
	}

	out := make([]string, 0, len(p.Warnings))
	for _, w := range p.Warnings {
		out = append(out, secret.Redact(w))
	}

	return out
}

const (
	maxFailuresPerSlice = 10
	maxTailLines        = 15
	maxTailBytes        = 1500
	maxDescription      = 140
	maxTestName         = 300
)

// ResultFile is what a repository's slice task writes.
type ResultFile struct {
	Passed   bool      `json:"passed"`
	Failures []Failure `json:"failures"`
	Tests    *int      `json:"tests,omitempty"`
	// Error says why the slice has no result of its own, for a slice its
	// driver ran and never reported; such a slice is errored.
	Error string `json:"error,omitempty"`
	// Files are what a failed slice left to show why, kept with its state
	// when it failed or errored.
	Files []FailureFile `json:"files,omitempty"`
}

var ErrResult = errors.New("result file")

// ReadResult reads a slice task's result file. A missing or unreadable file
// is an errored slice: the task never got to say how it went.
func ReadResult(p string) (State, []Failure) { return readResult(p, nil) }

// readResult reads a result file, redacting before anything is cut short so
// no cut leaves part of a secret behind.
func readResult(p string, redact func(string) string) (State, []Failure) {
	data, err := os.ReadFile(p) //nolint:gosec // the task's own output
	if err != nil {
		return Errored, []Failure{{Test: "(no result file)", Tail: err.Error()}}
	}

	var r ResultFile
	if err := json.Unmarshal(data, &r); err != nil {
		return Errored, []Failure{{Test: "(unreadable result file)", Tail: err.Error()}}
	}

	if r.Error != "" {
		tail := r.Error
		if redact != nil {
			tail = redact(tail)
		}

		return Errored, capFailures([]Failure{{Test: "(no result)", Tail: tail}})
	}

	if r.Passed && len(r.Failures) == 0 {
		return Passed, nil
	}

	if redact != nil {
		for i, f := range r.Failures {
			r.Failures[i] = Failure{Test: redact(f.Test), Tail: redact(f.Tail)}
		}
	}

	return Failed, capFailures(r.Failures)
}

func testsRan(p string) int {
	data, err := os.ReadFile(p) //nolint:gosec // the task's own output
	if err != nil {
		return 0
	}

	var r ResultFile
	if json.Unmarshal(data, &r) != nil || r.Tests == nil {
		return 0
	}

	return *r.Tests
}

func resultFiles(p string) []FailureFile {
	data, err := os.ReadFile(p) //nolint:gosec // the task's own output
	if err != nil {
		return nil
	}

	var r ResultFile
	if json.Unmarshal(data, &r) != nil {
		return nil
	}

	return r.Files
}

// RanNoTests tells whether a result file is a passed one with "tests": 0; a file that doesn't
// say how many ran never counts as none.
func RanNoTests(p string) bool {
	data, err := os.ReadFile(p) //nolint:gosec // the task's own output
	if err != nil {
		return false
	}

	var r ResultFile

	return json.Unmarshal(data, &r) == nil && r.Tests != nil && *r.Tests == 0
}

func capFailures(fs []Failure) []Failure {
	out := make([]Failure, 0, min(len(fs), maxFailuresPerSlice))

	for i, f := range fs {
		if i == maxFailuresPerSlice {
			out = append(out, Failure{Test: fmt.Sprintf("(%d more)", len(fs)-maxFailuresPerSlice)})

			break
		}

		out = append(out, Failure{Test: capName(f.Test), Tail: capTail(f.Tail)})
	}

	return out
}

func capName(s string) string {
	if len(s) <= maxTestName {
		return s
	}

	return strings.ToValidUTF8(s[:maxTestName], "") + "..."
}

func capTail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > maxTailLines {
		lines = lines[len(lines)-maxTailLines:]
	}

	s = strings.Join(lines, "\n")
	if len(s) > maxTailBytes {
		s = s[len(s)-maxTailBytes:]
	}

	return s
}

// MemoryTest names the failure of a slice not run because its batch found the worker short of memory.
const MemoryTest = "(memory)"

func (sl Slice) ShortOfMemory() bool {
	return sl.State == NotRun && len(sl.Failures) > 0 && sl.Failures[0].Test == MemoryTest
}

func (s Suite) shortOfMemory() int {
	n := 0

	for _, sl := range s.Slices {
		if sl.ShortOfMemory() {
			n++
		}
	}

	return n
}

func (s Suite) Counts() map[State]int {
	c := map[State]int{Untouched: len(s.Untouched)}
	for _, sl := range s.Slices {
		c[sl.State]++
	}

	return c
}

// GitHub's commit status states.
const (
	statusPending = "pending"
	statusSuccess = "success"
	statusFailure = "failure"
	statusError   = "error"
)

func (s Suite) noTests() int {
	n := 0

	for _, sl := range s.Slices {
		if sl.NoTests {
			n++
		}
	}

	return n
}

func (s Suite) untold() (cannot, recorded int) {
	for _, sl := range s.Slices {
		if sl.Recorded {
			recorded++
		}

		if len(sl.CannotTell) > 0 {
			cannot++
		}
	}

	return cannot, recorded
}

func (s Suite) noneTold() (string, bool) {
	cannot, recorded := s.untold()
	c := s.Counts()

	if cannot == 0 || cannot < recorded || c[Pending]+c[Running] > 0 {
		return "", false
	}

	for _, sl := range s.Slices {
		if len(sl.CannotTell) == 0 {
			continue
		}

		m := sl.CannotTell[0]

		text := fmt.Sprintf("no slice could be recorded: %s found no evidence of a recording in coverage/<slice>/%s", m.Plugin, m.Plugin)
		if m.Reason != "" {
			text += " (" + m.Reason + ")"
		}

		return text, true
	}

	return "", false
}

// Status is a suite's GitHub state and description of at most 140 characters; what a test or
// plugin wrote is masked before it is cut, so no cut leaves part of a secret behind.
func (s Suite) Status() (state, description string) {
	state, description = s.status()
	description = secret.Redact(description)

	if len(description) > maxDescription {
		description = description[:maxDescription]
	}

	return state, description
}

func (s Suite) status() (state, description string) {
	if why, ok := s.noneTold(); ok {
		return statusError, why
	}

	c := s.Counts()
	ran := len(s.Slices)
	total := ran + c[Untouched]

	switch {
	case c[Pending]+c[Running] > 0:
		state = statusPending
		description = fmt.Sprintf("%d of %d slices done", ran-c[Pending]-c[Running], ran)

		if bad := c[Failed] + c[Errored]; bad > 0 {
			description += fmt.Sprintf(", %d failed so far", bad)
		}

		if c[Untouched] > 0 {
			description += fmt.Sprintf(", %d untouched", c[Untouched])
		}
	case ran == 0:
		state = statusSuccess
		description = "nothing selected"

		if c[Untouched] > 0 {
			description += fmt.Sprintf(", %d untouched", c[Untouched])
		}
	case c[Errored]+c[NotRun] > 0:
		state, description = statusError, erroredText(c, s.shortOfMemory(), ran, total)
	case c[Failed] > 0:
		state = statusFailure
		description = fmt.Sprintf("%d of %d slices ran, %d failed (%s)", ran, total, c[Failed], plural(s.failedTests(), "test"))
	default:
		state = statusSuccess
		description = fmt.Sprintf("%d of %d slices ran, all passed", ran, total)
	}

	var narrowed strings.Builder
	for _, n := range s.Narrowed {
		fmt.Fprintf(&narrowed, ", %s left out %d", n.Narrowing, len(n.Dropped))
	}

	description += narrowed.String()

	// a test file that ran nothing is a finding, in every run's status
	if n := s.noTests(); n > 0 {
		description += fmt.Sprintf(", %d ran no tests", n)
	}

	// so is a slice whose recording no correlator could tell
	if n, _ := s.untold(); n > 0 {
		description += fmt.Sprintf(", %d could not be recorded", n)
	}

	return state, description
}

// RunStatus sums a run's suites: pending while any is, error or failure
// when any is, success when all are.
func (r Run) RunStatus() (state, description string) {
	n := map[string]int{}

	for _, s := range r.Suites {
		st, _ := s.Status()
		n[st]++
	}

	total := len(r.Suites)

	if level, ok := r.Net.Raise(); ok {
		state := statusFailure
		if n[statusError] > 0 {
			state = statusError
		}

		return state, fmt.Sprintf("%d failures trace to narrowed changes: raise narrowing to %d", r.Net.Failing(), level)
	}

	state, description = r.runStatus(n, total)

	// a cached graph that differs from its cold rebuild is a fault of a
	// plugin or of the cache, whatever the tests did
	if r.Rebuild.failing() && state != statusError {
		state, description = statusFailure, r.Rebuild.summary()+"; "+description
	}

	// a slice no correlator could tell passes, but never quietly
	untold := 0
	for _, s := range r.Suites {
		c, _ := s.untold()
		untold += c
	}

	if untold > 0 {
		description += fmt.Sprintf(", %d could not be recorded", untold)
	}

	if n := len(r.Warnings()); n > 0 {
		description += ", " + plural(n, "warning")
	}

	if asked, cached := r.Plugins.Totals(); asked+cached > 0 {
		description += fmt.Sprintf(", plugins: %d of %d files from the cache", cached, asked+cached)
	}

	return state, statusText(secret.Redact(description))
}

func (r Run) runStatus(n map[string]int, total int) (state, description string) {
	switch {
	case n[statusPending] > 0:
		return statusPending, fmt.Sprintf("%d of %d suites done", total-n[statusPending], total)
	case n[statusError] > 0:
		return statusError, fmt.Sprintf("%d of %d suites errored, %d failed", n[statusError], total, n[statusFailure])
	case n[statusFailure] > 0:
		return statusFailure, fmt.Sprintf("%d of %d suites failed", n[statusFailure], total)
	default:
		return statusSuccess, plural(total, "suite") + " passed"
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}

	return fmt.Sprintf("%d %ss", n, word)
}

// failedTests counts the distinct failing tests of the suite's failed
// slices: a test file may sit in several slices, and its test counts once.
func (s Suite) failedTests() int {
	seen := map[string]bool{}
	more := 0

	for _, sl := range s.Slices {
		if sl.State != Failed {
			continue
		}

		for _, f := range sl.Failures {
			var n int
			if _, err := fmt.Sscanf(f.Test, "(%d more)", &n); err == nil {
				more += n

				continue
			}

			// notes such as "(recording)" or "(extras)" are no tests
			if strings.HasPrefix(f.Test, "(") {
				continue
			}

			seen[f.Test] = true
		}
	}

	return len(seen) + more
}

func (r Run) Sorted() Run {
	out := r
	out.Suites = append([]Suite(nil), r.Suites...)

	sort.Slice(out.Suites, func(i, j int) bool { return out.Suites[i].Name < out.Suites[j].Name })

	for i := range out.Suites {
		s := &out.Suites[i]
		s.Slices = append([]Slice(nil), s.Slices...)
		sort.Slice(s.Slices, func(a, b int) bool { return s.Slices[a].display() < s.Slices[b].display() })
		s.Untouched = append([]string(nil), s.Untouched...)
		sort.Strings(s.Untouched)
		s.Removed = append([]string(nil), s.Removed...)
		sort.Strings(s.Removed)
	}

	return out
}

func erroredText(c map[State]int, short, ran, total int) string {
	description := fmt.Sprintf("%d of %d slices ran", ran-c[NotRun], total)

	if n := c[NotRun] - short; n > 0 {
		description += fmt.Sprintf(", %d not run (a batch's setup failed)", n)
	}

	if short > 0 {
		description += fmt.Sprintf(", %d not run (worker short of memory)", short)
	}

	if c[Errored] > 0 {
		description += fmt.Sprintf(", %d errored", c[Errored])
	}

	if c[Failed] > 0 {
		description += fmt.Sprintf(", %d failed", c[Failed])
	}

	return description
}
