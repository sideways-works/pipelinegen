package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
)

type monthScale struct {
	days                                             int
	recorded                                         map[string]int
	plain                                            int
	units, perUnit                                   int
	mainPerDay, prsPerDay                            int
	picked                                           float64
	batch                                            int
	plugins, coldAnswers, answersPerPlan, buildEvery int
	measure                                          []int
	listsCommits                                     int
}

var adopterScale = monthScale{
	days:     30,
	recorded: map[string]int{"backend": 803, "e2e": 241}, plain: 25,
	units: 12000, perUnit: 1200,
	mainPerDay: 10, prsPerDay: 20, picked: 0.05,
	batch:   25,
	plugins: 4, coldAnswers: 8000, answersPerPlan: 30, buildEvery: 10,
	measure: []int{1, 7, 14, 30}, listsCommits: 200,
}

// smallScale keeps the month's shape affordable yet long enough for every keep rule to remove: runs
// after 7 days, lists past 20 commits, answers after two weeks unused.
var smallScale = monthScale{
	days:     22,
	recorded: map[string]int{"backend": 9, "e2e": 3}, plain: 2,
	units: 300, perUnit: 30,
	mainPerDay: 2, prsPerDay: 2, picked: 0.3,
	batch:   5,
	plugins: 1, coldAnswers: 40, answersPerPlan: 4, buildEvery: 8,
	measure: []int{8, 15, 22}, listsCommits: 20,
}

type monthSim struct {
	t     *testing.T
	sc    monthScale
	rnd   *rand.Rand
	url   string
	bare  string
	code  string
	now   time.Time
	day   int
	pr    int
	build map[int]int // plugin -> build number
	work  string
	tree  [][]int
	local string
	asked int
}

type dayNumbers struct {
	day                                int
	refs                               map[string]int
	total                              int
	packed                             int64
	lsRemoteBytes, advertised          int
	planFetchBytes, coldFetchBytes     int64
	planTime                           time.Duration
	cacheEntries, cacheRefs, listsRefs int
	asked                              int
}

func newMonth(t *testing.T, sc monthScale) *monthSim {
	t.Helper()

	m := &monthSim{
		t: t, sc: sc, rnd: rand.New(rand.NewPCG(1, 2)), //nolint:gosec // a fixed sequence, not a secret
		url: bareRemote(t), code: filepath.Join(t.TempDir(), "code"),
		now: time.Date(2026, 1, 5, 6, 0, 0, 0, time.UTC), build: map[int]int{}, work: t.TempDir(), local: t.TempDir(),
	}
	m.bare = strings.TrimPrefix(m.url, "file://")

	m.git(m.code, "init", "-q", "-b", "main", m.code)

	return m
}

func (m *monthSim) git(dir string, args ...string) string {
	m.t.Helper()

	cmd := exec.CommandContext(m.t.Context(), "git", args...)
	if dir != "" && args[0] != "init" {
		cmd.Dir = dir
	}

	date := strconv.FormatInt(m.now.Unix(), 10) + " +0000"
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date,
		"GIT_AUTHOR_NAME=dev", "GIT_AUTHOR_EMAIL=dev@example.com", "GIT_COMMITTER_NAME=dev", "GIT_COMMITTER_EMAIL=dev@example.com")

	out, err := cmd.Output()
	if err != nil {
		m.t.Fatalf("git %v: %v", args, err)
	}

	return strings.TrimSpace(string(out))
}

func (m *monthSim) store() *gitstore.Store {
	dir, err := os.MkdirTemp(m.work, "job-")
	if err != nil {
		m.t.Fatal(err)
	}

	// as every command's store: a write the remote refused goes again
	return &gitstore.Store{Remote: m.url, Dir: dir, Retries: config.DefaultRetries, Clock: func() time.Time { return m.now }}
}

func (m *monthSim) done(s *gitstore.Store) {
	if s.Seed != "" {
		if err := s.Close(); err != nil {
			m.t.Fatal(err)
		}

		return
	}

	if err := os.RemoveAll(s.Dir); err != nil {
		m.t.Fatal(err)
	}

	if err := os.RemoveAll(s.Dir + ".groups"); err != nil {
		m.t.Fatal(err)
	}
}

func (m *monthSim) planStore() *gitstore.Store {
	return &gitstore.Store{Remote: m.url, Seed: filepath.Join(m.work, "seed"), Retries: config.DefaultRetries, Clock: func() time.Time { return m.now }}
}

func (m *monthSim) slices() [][3]string {
	var out [][3]string

	for _, suite := range sortedKeys(m.sc.recorded) {
		for i := range m.sc.recorded[suite] {
			out = append(out, [3]string{suite, fmt.Sprintf("%s/pkg%03d/file%04d_test.go", suite, i%97, i), "rec"})
		}
	}

	for i := range m.sc.plain {
		out = append(out, [3]string{"checks", fmt.Sprintf("checks/c%03d", i), ""})
	}

	return out
}

func sortedKeys(m map[string]int) []string {
	return slices.Sorted(maps.Keys(m))
}

func (m *monthSim) recordingOf(i int) []byte {
	var b bytes.Buffer

	b.WriteString("# observed\n")

	base := rand.New(rand.NewPCG(uint64(i), 7)) //nolint:gosec // a fixed sequence, not a secret
	seen := map[int]bool{}

	for len(seen) < m.sc.perUnit {
		u := base.IntN(m.sc.units)
		if m.rnd.IntN(200) == 0 {
			u = m.rnd.IntN(m.sc.units)
		}

		seen[u] = true
	}

	ids := make([]string, 0, len(seen))
	for u := range seen {
		ids = append(ids, fmt.Sprintf("pkg%02d:Func%05d", u%40, u))
	}

	slices.Sort(ids)

	for _, id := range ids {
		b.WriteString(id + "\n")
	}

	return b.Bytes()
}

func (m *monthSim) run(id ID, picked []int, attach bool) {
	ctx := m.t.Context()
	all := m.slices()

	type sel struct {
		Name   string              `json:"name"`
		Label  string              `json:"label"`
		Reason []map[string]string `json:"why"`
	}

	bySuite := map[string][]sel{}

	for _, i := range picked {
		bySuite[all[i][0]] = append(bySuite[all[i][0]], sel{Name: all[i][1], Reason: []map[string]string{
			{"kind": "changed", "unit": fmt.Sprintf("pkg%02d:Func%05d", i%40, i), "via": strings.Repeat("x", 300)},
		}})
	}

	var suites []map[string]any
	for _, s := range slices.Sorted(maps.Keys(bySuite)) {
		suites = append(suites, map[string]any{"suite": s, "selected": bySuite[s], "untouched": []string{}, "whole_suite": []string{}})
	}

	plan, err := json.Marshal(map[string]any{"head": id.Commit, "suites": suites})
	if err != nil {
		m.t.Fatal(err)
	}

	s := m.store()
	defer m.done(s)

	if err := WritePlan(ctx, s, id, plan); err != nil {
		m.t.Fatal(err)
	}

	for b, start := 0, 0; start < len(picked); b, start = b+1, start+m.sc.batch {
		var ups []gitstore.Update

		for _, i := range picked[start:min(start+m.sc.batch, len(picked))] {
			sl := all[i]
			sf := StateFile{Label: sl[1], State: Passed, Updated: m.now, Seconds: 12.5, Recorded: sl[2] != ""}

			st, err := json.Marshal(sf)
			if err != nil {
				m.t.Fatal(err)
			}

			files := map[string][]byte{stateFile: st}
			if sl[2] != "" {
				files[recordingFile] = m.recordingOf(i)
			}

			ups = append(ups, gitstore.Update{Ref: id.StateRef(sl[0], sl[1]), Any: true, Files: files})
		}

		bs := m.store()
		bs.Batch = strconv.Itoa(b)

		if _, err := bs.Write(ctx, "batch", ups...); err != nil {
			m.t.Fatal(err)
		}

		m.done(bs)
	}

	for _, suite := range slices.Sorted(maps.Keys(bySuite)) {
		end, err := json.Marshal(endMark{Ended: m.now})
		if err != nil {
			m.t.Fatal(err)
		}

		if _, err := s.Write(ctx, "end", gitstore.Update{Ref: id.EndRef(suite), Any: true, Files: map[string][]byte{endFile: end}}); err != nil {
			m.t.Fatal(err)
		}

		if _, err := s.Compact(ctx, id.Prefix(), suite); err != nil {
			m.t.Fatal(err)
		}
	}

	if attach {
		m.attach(s, id, len(picked) == len(all))
	}
}

func (m *monthSim) attach(s *gitstore.Store, id ID, all bool) {
	ctx := m.t.Context()

	files, err := Recordings(ctx, s, id)
	if err != nil {
		m.t.Fatal(err)
	}

	recs, marks, err := lists.Recordings(files)
	if err != nil {
		m.t.Fatal(err)
	}

	shrink := map[string]bool{}

	if all {
		for k := range recs {
			shrink[k] = true
		}
	}

	if _, err := (lists.Attach{
		Repo: m.code, Run: id.Run, Change: id.Change, Commit: id.Commit, To: id.Commit,
		Recordings: recs, CannotTell: marks, Shrinkable: shrink, Trusted: true,
	}).Do(ctx, s); err != nil {
		m.t.Fatal(err)
	}
}

func (m *monthSim) planAnswers(onMain bool) {
	ctx := m.t.Context()
	s := m.planStore()

	defer m.done(s)

	perPlugin := m.sc.coldAnswers / m.sc.plugins
	if m.tree == nil {
		m.tree = make([][]int, m.sc.plugins)
		for p := range m.tree {
			m.tree[p] = make([]int, perPlugin)
		}
	}

	own := map[[2]int]string{}

	for range m.sc.answersPerPlan {
		p, f := m.rnd.IntN(m.sc.plugins), m.rnd.IntN(perPlugin)
		if onMain {
			m.tree[p][f]++
		} else {
			own[[2]int{p, f}] = fmt.Sprintf("pr-%d", m.pr)
		}
	}

	c := &plugincache.Cache{Dir: m.local, Remote: &plugincache.Refs{Store: s, Clock: func() time.Time { return m.now }}, Push: true}

	for p := range m.sc.plugins {
		b := m.buildOf(p)

		for f := range perPlugin {
			content := fmt.Sprintf("file%05d@%d", f, m.tree[p][f])
			if tag, ok := own[[2]int{p, f}]; ok {
				content += tag
			}

			k := plugincache.Key{Plugin: b.Plugin, Version: b.Version, SHA256: b.SHA256, Call: "units", Content: []string{content}}

			if _, err := c.Get(ctx, k); err == nil {
				continue
			} else if !errors.Is(err, plugincache.ErrMiss) {
				m.t.Fatal(err)
			}

			m.asked++

			if err := c.Put(k, m.answer(content)); err != nil {
				m.t.Fatal(err)
			}
		}
	}

	if err := c.Flush(ctx); err != nil {
		m.t.Fatal(err)
	}
}

func (m *monthSim) buildOf(p int) plugincache.Build {
	return plugincache.Build{Plugin: fmt.Sprintf("plugin%d", p), Version: fmt.Sprintf("v1.%d", m.build[p]), SHA256: fmt.Sprintf("%064d", m.build[p])}
}

func (m *monthSim) answer(content string) []byte {
	h := fnv.New64a()
	h.Write([]byte(content))

	rnd := rand.New(rand.NewPCG(h.Sum64(), 3)) //nolint:gosec // a fixed sequence, not a secret
	n := 40 + rnd.IntN(80)

	switch r := rnd.IntN(100); {
	case r == 0:
		n = 20000 + rnd.IntN(70000)
	case r < 10:
		n = 1000 + rnd.IntN(5000)
	}

	var b bytes.Buffer

	b.WriteString(`{"units":[`)

	for b.Len() < n {
		u := rnd.IntN(m.sc.units)
		fmt.Fprintf(&b, `{"id":"pkg%02d:Func%05d","digest":"%016x"},`, u%40, u, rnd.Uint64())
	}

	b.WriteString("]}")

	return b.Bytes()
}

func (m *monthSim) pick(share float64) []int {
	n := len(m.slices())

	var out []int

	for i := range n {
		if m.rnd.Float64() < share {
			out = append(out, i)
		}
	}

	return out
}

func (m *monthSim) sweep(head string) {
	s := m.planStore()
	defer m.done(s)

	keep := Keep{Runs: KeepRuns{Age: 7 * 24 * time.Hour}, PerRun: map[string]KeepRuns{}, Lists: KeepLists{
		Age: 90 * 24 * time.Hour, Commits: m.sc.listsCommits, Mainline: strings.Fields(m.git(m.code, "rev-list", "--first-parent", "HEAD")),
	}}
	if _, err := Sweep(m.t.Context(), s, m.now, keep, []string{head}); err != nil {
		m.t.Fatal(err)
	}

	if _, err := plugincache.Drop(m.t.Context(), s, m.now); err != nil {
		m.t.Fatal(err)
	}
}

func (m *monthSim) oneDay() {
	m.day++

	if m.day == 1 {
		m.git(m.code, "commit", "-q", "--allow-empty", "-m", "start")
	}

	if m.day > 1 && m.day%m.sc.buildEvery == 0 {
		m.build[(m.day/m.sc.buildEvery)%m.sc.plugins]++
	}

	kinds := slices.Repeat([]bool{true}, m.sc.mainPerDay)
	kinds = append(kinds, slices.Repeat([]bool{false}, m.sc.prsPerDay)...)
	m.rnd.Shuffle(len(kinds), func(i, j int) { kinds[i], kinds[j] = kinds[j], kinds[i] })

	for _, onMain := range kinds {
		m.now = m.now.Add(30 * time.Minute)

		if onMain {
			m.git(m.code, "commit", "-q", "--allow-empty", "-m", fmt.Sprintf("day %d", m.day))
			m.run(ID{Run: "main", Change: "main", Commit: m.git(m.code, "rev-parse", "HEAD")}, m.pick(m.sc.picked), true)
		} else {
			m.pr++
			m.run(ID{Run: "pr", Change: fmt.Sprintf("pr-%d", m.pr), Commit: fmt.Sprintf("%040x", m.rnd.Uint64()), PR: m.pr}, m.pick(m.sc.picked), false)
		}

		m.planAnswers(onMain)
	}

	head := m.git(m.code, "rev-parse", "HEAD")
	all := make([]int, len(m.slices()))

	for i := range all {
		all[i] = i
	}

	m.now = m.now.Add(time.Hour)
	m.run(ID{Run: "nightly", Change: "main", Commit: head}, all, true)
	m.sweep(head)

	m.now = time.Date(2026, 1, 5, 6, 0, 0, 0, time.UTC).AddDate(0, 0, m.day)
}

func (m *monthSim) numbers() dayNumbers {
	n := dayNumbers{day: m.day, refs: map[string]int{}, asked: m.asked}

	for line := range strings.SplitSeq(m.git(m.bare, "for-each-ref", "--format=%(refname)", "refs/pipelinegen/"), "\n") {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "/", 4)
		n.refs[parts[2]]++
		n.total++
	}

	n.cacheRefs, n.listsRefs = n.refs["cache"], n.refs["lists"]

	m.git(m.bare, "gc", "-q", "--prune=now")

	for line := range strings.SplitSeq(m.git(m.bare, "count-objects", "-v"), "\n") {
		if v, ok := strings.CutPrefix(line, "size-pack: "); ok {
			kib, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				m.t.Fatal(err)
			}

			n.packed = kib * 1024
		}
	}

	n.lsRemoteBytes = len(m.git("", "ls-remote", m.url, "refs/pipelinegen/*"))
	n.advertised = len(m.git("", "ls-remote", m.url))

	n.planFetchBytes, n.planTime = m.planRead(m.planStore())
	n.coldFetchBytes, _ = m.planRead(m.store())

	for ref := range strings.SplitSeq(m.git(m.bare, "for-each-ref", "--format=%(refname)", plugincache.RefPrefix), "\n") {
		if ref != "" {
			n.cacheEntries += strings.Count(m.git(m.bare, "ls-tree", "-r", "--name-only", ref), "\n") + 1
		}
	}

	return n
}

func (m *monthSim) planRead(s *gitstore.Store) (int64, time.Duration) {
	ctx := m.t.Context()

	defer m.done(s)

	before := int64(0)
	if _, err := os.Stat(s.Seed); err == nil {
		before = dirSize(m.t, filepath.Join(s.Seed, "objects"))
	}

	start := time.Now()
	head := m.git(m.code, "rev-parse", "HEAD")

	if _, _, err := lists.ForWithin(ctx, s, m.code, head, m.sc.listsCommits); err != nil {
		m.t.Fatal(err)
	}

	r := &plugincache.Refs{Store: s, Clock: func() time.Time { return m.now }}

	for p := range m.sc.plugins {
		if _, _, err := r.Answer(ctx, m.buildOf(p), strings.Repeat("0", 64)); err != nil {
			m.t.Fatal(err)
		}
	}

	return dirSize(m.t, filepath.Join(s.Dir, "objects")) - before, time.Since(start)
}

func dirSize(t *testing.T, dir string) int64 {
	t.Helper()

	var total int64

	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return total
}

func (n dayNumbers) String() string {
	kinds := sortedKeys(n.refs)
	parts := make([]string, 0, len(kinds))

	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s %d", k, n.refs[k]))
	}

	return fmt.Sprintf("day %d: %d refs (%s); packed %.1f MB; ls-remote of the namespace %.1f KB, of everything %.1f KB; "+
		"a plan job fetches %.2f MB in %s (%.1f MB on a fresh worker); cache entries %d; answers computed so far %d",
		n.day, n.total, strings.Join(parts, ", "), float64(n.packed)/1e6, float64(n.lsRemoteBytes)/1e3, float64(n.advertised)/1e3,
		float64(n.planFetchBytes)/1e6, n.planTime.Round(time.Millisecond), float64(n.coldFetchBytes)/1e6, n.cacheEntries, n.asked)
}

// PIPELINEGEN_TEST_MONTH=adopter runs it at a large adopter's size and
// prints the numbers.
func TestAMonthOfRunsStaysBounded(t *testing.T) {
	if storetest.OnBranch {
		t.Skip("the month measures the refs store")
	}

	sc := smallScale
	if os.Getenv("PIPELINEGEN_TEST_MONTH") == "adopter" {
		sc = adopterScale
	}

	m := newMonth(t, sc)

	var got []dayNumbers

	start := time.Now()

	for m.day < sc.days {
		m.oneDay()
		t.Logf("day %d done after %s", m.day, time.Since(start).Round(time.Second))

		if slices.Contains(sc.measure, m.day) {
			n := m.numbers()
			got = append(got, n)

			t.Log(n)
		}
	}

	mid, last := got[len(got)-2], got[len(got)-1]

	if last.refs["batch"] != 0 {
		t.Errorf("batch refs left: %d", last.refs["batch"])
	}

	runsPerDay := sc.mainPerDay + sc.prsPerDay + 1
	if last.refs["run"] > runsPerDay*8 {
		t.Errorf("%d run refs; the 7-day keep holds about %d", last.refs["run"], runsPerDay*8)
	}

	if last.refs["lists"] > sc.listsCommits+2 {
		t.Errorf("%d lists refs past %d commits", last.refs["lists"], sc.listsCommits)
	}

	for _, kind := range []string{"run", "lists", "cache"} {
		if float64(last.refs[kind]) > 1.25*float64(mid.refs[kind])+2 {
			t.Errorf("%s refs grow: %d on day %d, %d on day %d", kind, mid.refs[kind], mid.day, last.refs[kind], last.day)
		}
	}

	if float64(last.cacheEntries) > 1.25*float64(mid.cacheEntries)+10 || float64(last.packed) > 1.5*float64(mid.packed) {
		t.Errorf("the store grows: %v then %v", mid, last)
	}
}
