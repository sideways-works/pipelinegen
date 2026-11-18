package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/progress"
	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/plugin"
)

type answer struct {
	Sets       []plugin.Set
	Rest       plugin.Set
	DigestFrom []plugin.DigestFrom
}

func blobs(ctx context.Context, root string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-tree", "-r", "-z", "HEAD") //nolint:gosec // git in pipelinegen's work tree
	cmd.Env = secret.HardenGit(os.Environ())

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: list the files of %s: %w", ErrPlugins, root, err)
	}

	files := map[string]string{}

	for rec := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		meta, path, ok := strings.Cut(rec, "\t")
		if f := strings.Fields(meta); ok && len(f) == 3 && f[1] == "blob" {
			files[path] = f[2]
		}
	}

	return files, nil
}

func (s *Set) claims(ctx context.Context, l *Loaded, root string, files map[string]string) (claims []plugin.Claim, untold []string, err error) { //nolint:nonamedreturns // two lists and an error
	byPath, untold, err := s.detected(ctx, l, root, files)
	if err != nil {
		return nil, nil, err
	}

	var out []plugin.Claim

	seen := map[string]bool{}

	for _, p := range sortedKeys(byPath) {
		for _, c := range byPath[p] {
			out = append(out, c)
			seen[c.Path] = true
		}
	}

	for _, p := range sortedKeys(files) {
		if !seen[p] && matchAny(l.Conf.Include, p) && !matchAny(l.Conf.Exclude, p) {
			out = append(out, plugin.Claim{Path: p, Role: "included"})
			seen[p] = true
		}
	}

	for _, c := range slicesClone(out) {
		for _, d := range c.Depends {
			if !seen[d] {
				out = append(out, plugin.Claim{Path: d, Role: "fragment"})
				seen[d] = true
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })

	return out, untold, nil
}

func (s *Set) detected(ctx context.Context, l *Loaded, root string, files map[string]string) (map[string][]plugin.Claim, []string, error) {
	byPath := map[string][]plugin.Claim{}

	var missing []string

	cached := map[string][]plugin.Claim{}

	for _, p := range sortedKeys(files) {
		if !matchAny(l.Desc.Candidates, p) || matchAny(l.Conf.Exclude, p) {
			continue
		}

		var cs []plugin.Claim

		data, err := s.stored(ctx, detectKey(l, p, files[p]))
		if err != nil || json.Unmarshal(data, &cs) != nil {
			missing = append(missing, p)

			continue
		}

		if s.o.Cold {
			cached[p] = cs
			missing = append(missing, p)

			continue
		}

		byPath[p] = cs
	}

	s.count(l.Conf.Name, plugin.CallDetect, len(missing), len(byPath))

	if len(missing) == 0 || !l.can(plugin.CallDetect) {
		return byPath, nil, nil
	}

	got, cacheable, told, err := s.detect(ctx, l, root, missing)
	if err != nil {
		return nil, nil, err
	}

	if !told {
		return byPath, missing, nil
	}

	s.compareClaims(l, cached, got)

	for _, p := range missing {
		byPath[p] = got[p]

		if !cacheable {
			continue
		}

		data, err := json.Marshal(got[p])
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrPlugins, err)
		}

		if err := s.o.Answers.Put(detectKey(l, p, files[p]), data); err != nil {
			return nil, nil, err
		}
	}

	return byPath, nil, nil
}

// detectKey keys a detect answer on the path as well as the blob: the answer names the path, so
// two files with the same content each need their own.
func detectKey(l *Loaded, p, blob string) plugincache.Key {
	return l.key(plugin.CallDetect, p, blob, detectSchema)
}

// detectSchema versions the cached detect answer's shape: an answer kept before claims carried
// recorded would read as one without it.
const detectSchema = "claims with recorded and erased"

func (s *Set) detect(ctx context.Context, l *Loaded, root string, paths []string) (claims map[string][]plugin.Claim, cacheable, told bool, err error) { //nolint:nonamedreturns // what it says and how far to trust it
	work := s.work()

	var ans plugin.DetectAnswer

	restore := progress.Stage(ctx, "plugin %s: detect of %d files", l.Conf.Name, len(paths))
	err = l.P.Call(ctx, plugin.CallDetect, plugin.DetectRequest{Request: l.P.NewRequest(work), Tree: root, Paths: paths}, &ans)

	restore()

	if errors.Is(err, plugin.ErrCantTell) {
		s.cantTell(l, plugin.CallDetect, err)

		return map[string][]plugin.Claim{}, false, false, nil
	}

	if err == nil {
		ans.Claims, err = plugin.ReadLines(work, named(ans.Files, "claims"), ans.Claims)
	}

	if err != nil {
		return nil, false, false, err
	}

	out := map[string][]plugin.Claim{}
	for _, c := range ans.Claims {
		out[c.Path] = append(out[c.Path], c)
	}

	if !isCacheable(ans.Answer) {
		s.notCacheable(l.Conf.Name, plugin.CallDetect)
	}

	return out, isCacheable(ans.Answer), true, nil
}

func (s *Set) sets(ctx context.Context, l *Loaded, call, root string, files map[string]string, units unitDigests, claims []plugin.Claim) (ans answer, untold []string, err error) { //nolint:nonamedreturns // what it says and what it couldn't
	// a plugin that claims nothing in particular reads the whole tree
	if len(claims) == 0 && !l.can(plugin.CallDetect) && len(l.Desc.Candidates) == 0 {
		for _, p := range sortedKeys(files) {
			claims = append(claims, plugin.Claim{Path: p, Role: "tree"})
		}
	}

	if s.o.Cold {
		return s.coldSets(ctx, l, call, root, files, units, claims)
	}

	out, missing, owed := s.cachedSets(ctx, l, call, files, units, claims)

	var asked []string

	defer func() { s.count(l.Conf.Name, call, len(asked), max(len(claims)-len(asked), 0)) }()

	// content outside sets (and digest_from) can't be cached by file: a plugin that ever answered
	// some is asked about every claim each time
	flag := l.key(call + "-rest-bearing")
	_, flagErr := s.o.Answers.Get(ctx, flag)
	restBearing := flagErr == nil

	if len(missing) == 0 && !restBearing {
		return out, nil, nil
	}

	asked = missing
	if restBearing {
		asked, out.Sets = claimPaths(claims), nil
	}

	fresh, ok, err := s.ask(ctx, l, call, root, asked, claims)
	if err != nil {
		return out, nil, err
	}

	if !ok {
		return out, asked, nil
	}

	if hasRest(fresh.answer) && !restBearing {
		if len(asked) < len(claims) {
			out.Sets = nil
		}

		if fresh, asked, ok, err = s.firstRest(ctx, l, call, root, flag, fresh, asked, claims); err != nil {
			return out, nil, err
		}

		if !ok {
			return out, asked, nil
		}
	}

	out.Sets = append(out.Sets, fresh.Sets...)
	out.Rest, out.DigestFrom = fresh.Rest, fresh.DigestFrom

	if !restBearing {
		var more []plugin.Set

		if more, asked, untold, err = s.settle(ctx, l, call, root, claims, out.Sets, asked, owed); err != nil {
			return out, nil, err
		}

		out.Sets = append(out.Sets, more...)
		fresh.Sets = append(fresh.Sets, more...)
	}

	untold, err = s.keepFresh(ctx, l, call, files, units, fresh, asked, untold)

	return out, untold, err
}

func (s *Set) keepFresh(ctx context.Context, l *Loaded, call string, files map[string]string, units unitDigests, fresh asked, asked, untold []string) ([]string, error) {
	before := len(untold)
	fresh.Sets, untold = s.treeInputs(l, call, fresh.Sets, files, untold)

	if !fresh.cacheable {
		return untold, nil
	}

	// a path of a set kept out of the cache isn't recorded as answered either, or the next plan
	// would take it for one holding no set
	wide := untold[before:]
	answered := slices.DeleteFunc(slices.Clone(asked), func(p string) bool { return slices.Contains(wide, p) })

	return untold, s.keep(ctx, l, call, files, units, fresh.Sets, answered)
}

// treeInputs keys each set on its absent paths as on its inputs. A set naming a path outside the
// tree can't be keyed: its files join untold, it is never cached, and the plan warns.
func (s *Set) treeInputs(l *Loaded, call string, sets []plugin.Set, files map[string]string, untold []string) ([]plugin.Set, []string) {
	kept := make([]plugin.Set, 0, len(sets))

	for _, set := range sets {
		for _, a := range set.Absent {
			if !slices.Contains(set.Inputs, a) {
				set.Inputs = append(set.Inputs, a)
			}
		}

		set.Absent = nil

		outside := slices.IndexFunc(set.Inputs, func(in string) bool { return !inTree(in) })
		if outside < 0 {
			kept = append(kept, set)

			continue
		}

		s.warnf("%s %s: the set of %s declares %s, which is outside the tree: bound wide until %s declares paths of the tree",
			l.Conf.Name, call, set.Group, set.Inputs[outside], l.Conf.Name)

		for _, in := range append([]string{set.Group}, set.Inputs...) {
			if _, ok := files[in]; ok && !slices.Contains(untold, in) {
				untold = append(untold, in)
			}
		}
	}

	return kept, untold
}

func inTree(p string) bool {
	return p != "" && !path.IsAbs(p) && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../")
}

const settleRounds = 3

// settle makes every set the asked paths were in last time come back, since a plugin asked about
// one path must answer every set holding it; what isn't after a few rounds is untold, bound wide.
func (s *Set) settle(ctx context.Context, l *Loaded, call, root string, claims []plugin.Claim, have []plugin.Set, asked []string, owed [][]string) ([]plugin.Set, []string, []string, error) {
	var more []plugin.Set

	isClaim := map[string]bool{}
	for _, c := range claims {
		isClaim[c.Path] = true
	}

	for range settleRounds {
		again := slices.DeleteFunc(unanswered(owed, slices.Concat(have, more), isClaim), func(in string) bool { return slices.Contains(asked, in) })
		if len(again) == 0 {
			break
		}

		fresh, ok, err := s.ask(ctx, l, call, root, again, claims)
		if err != nil {
			return nil, asked, nil, err
		}

		asked = append(asked, again...)

		if !ok {
			return more, asked, again, nil
		}

		more = append(more, fresh.Sets...)
	}

	untold := unanswered(owed, slices.Concat(have, more), isClaim)
	if len(untold) > 0 {
		s.warnf("plugin %s (%s): asked again, it answered no set for %s, which a set held last time: they are bound wide", l.Conf.Name, call, strings.Join(untold, ", "))
	}

	return more, asked, untold, nil
}

func unanswered(owed [][]string, sets []plugin.Set, isClaim map[string]bool) []string {
	covered := map[string]bool{}

	for _, set := range sets {
		for _, in := range set.Inputs {
			covered[in] = true
		}
	}

	var out []string

	for _, inputs := range owed {
		for _, in := range inputs {
			if !covered[in] && isClaim[in] && !slices.Contains(out, in) {
				out = append(out, in)
			}
		}
	}

	return out
}

// firstRest asks about all claims the first time a plugin answers content outside sets: the rest
// may hold what the cached sets' files say.
func (s *Set) firstRest(ctx context.Context, l *Loaded, call, root string, flag plugincache.Key, fresh asked, paths []string, claims []plugin.Claim) (asked, []string, bool, error) {
	if err := s.o.Answers.Put(flag, []byte("1")); err != nil {
		return fresh, paths, true, err
	}

	if len(paths) >= len(claims) {
		return fresh, paths, true, nil
	}

	all := claimPaths(claims)
	again, ok, err := s.ask(ctx, l, call, root, all, claims)

	return again, all, ok, err
}

func (s *Set) cachedSets(ctx context.Context, l *Loaded, call string, files map[string]string, units unitDigests, claims []plugin.Claim) (answer, []string, [][]string) {
	c := s.lookup(ctx, l, call, files, units, claims)

	// a set loaded through one claim may name one asked again: the fresh answer replaces it
	out := answer{Sets: slices.DeleteFunc(slices.Clone(c.served), func(set plugin.Set) bool {
		return slices.ContainsFunc(set.Inputs, func(in string) bool { return c.missing[in] })
	})}

	return out, c.asked, c.owed
}

type lookedUp struct {
	served  []plugin.Set
	missing map[string]bool
	asked   []string
	owed    [][]string
}

// lookup finds each claim's sets in the cache and picks the claims to ask about. A stale set is
// asked through its input the fewest sets hold: one every set holds would ask for every set.
func (s *Set) lookup(ctx context.Context, l *Loaded, call string, files map[string]string, units unitDigests, claims []plugin.Claim) lookedUp {
	// every claim is looked up on its own: sets may share an input, and a set one claim found
	// says nothing of the others another claim names
	entries := map[string][]memberEntry{}
	missing := map[string]bool{}

	for _, c := range claims {
		e, ok := s.members(ctx, l, call, files, c.Path)
		if !ok {
			missing[c.Path] = true

			continue
		}

		entries[c.Path] = e
	}

	owed := map[string][]string{}
	loaded := map[string]bool{}

	var (
		served []plugin.Set
		stale  []memberEntry
	)

	for _, c := range claims {
		for _, e := range entries[c.Path] {
			if loaded[e.id] {
				continue
			}

			loaded[e.id] = true

			if set, ok := s.storedSet(ctx, l, call, files, units, e.list); ok {
				served = append(served, set)

				continue
			}

			if slices.ContainsFunc(e.list.inputs, func(in string) bool { return missing[in] }) {
				owed[e.id] = e.list.inputs

				continue
			}

			stale = append(stale, e)
		}
	}

	askStale(stale, entries, owed, missing)
	askUncovered(claims, entries, served, owed, missing)

	var asked []string

	for _, c := range claims {
		if missing[c.Path] {
			asked = append(asked, c.Path)

			for _, e := range entries[c.Path] {
				owed[e.id] = e.list.inputs
			}
		}
	}

	lists := make([][]string, 0, len(owed))
	for _, id := range slices.Sorted(maps.Keys(owed)) {
		lists = append(lists, owed[id])
	}

	return lookedUp{served: served, missing: missing, asked: asked, owed: lists}
}

func askStale(stale []memberEntry, entries map[string][]memberEntry, owed map[string][]string, missing map[string]bool) {
	for _, e := range stale {
		if superseded(e, entries) {
			continue
		}

		if slices.ContainsFunc(e.list.inputs, func(in string) bool { return missing[in] }) {
			owed[e.id] = e.list.inputs

			continue
		}

		missing[fewestHeld(e, entries)] = true
	}
}

// askUncovered asks about each claim whose sets are neither served nor owed: two sets of one
// package can each lose to the other's newer record, leaving the package with none.
func askUncovered(claims []plugin.Claim, entries map[string][]memberEntry, served []plugin.Set, owed map[string][]string, missing map[string]bool) {
	covered := map[string]bool{}

	for _, set := range served {
		for _, in := range set.Inputs {
			covered[in] = true
		}
	}

	for _, inputs := range owed {
		for _, in := range inputs {
			covered[in] = true
		}
	}

	for _, c := range claims {
		if len(entries[c.Path]) > 0 && !covered[c.Path] {
			missing[c.Path] = true
		}
	}
}

// superseded says a set is no longer the plugin's: an input of it with a record doesn't name it.
// Asking about it again would never end.
func superseded(e memberEntry, entries map[string][]memberEntry) bool {
	for _, in := range e.list.inputs {
		held, ok := entries[in]
		if ok && !slices.ContainsFunc(held, func(h memberEntry) bool { return h.id == e.id }) {
			return true
		}
	}

	return false
}

func fewestHeld(e memberEntry, entries map[string][]memberEntry) string {
	best, n := "", 0

	for _, in := range slices.Sorted(slices.Values(e.list.inputs)) {
		held, ok := entries[in]
		if ok && (best == "" || len(held) < n) {
			best, n = in, len(held)
		}
	}

	return best
}

// keep caches fresh sets and records under each input every set naming it, each record edited as
// it stands when written, so another plan's answers added meanwhile stay named.
func (s *Set) keep(ctx context.Context, l *Loaded, call string, files map[string]string, units unitDigests, sets []plugin.Set, asked []string) error {
	lists := map[string]inputList{}
	inAnswer := map[string]bool{}

	for _, set := range sets {
		lists[listID(set.Inputs, set.InputUnits)] = inputList{set.Inputs, set.InputUnits}

		for _, in := range set.Inputs {
			inAnswer[in] = true
		}
	}

	earlier := map[string][]string{}

	for _, in := range asked {
		answers, err := s.memberAnswers(ctx, l, call, files, in)
		if err != nil {
			return err
		}

		earlier[in] = answers
	}

	id, err := s.putAnswer(l, call, lists)
	if err != nil {
		return err
	}

	for _, set := range sets {
		if err := s.putSet(l, call, files, units, set); err != nil {
			return err
		}
	}

	for _, p := range asked {
		if !inAnswer[p] {
			if err := s.putNothing(ctx, l, call, files, p, earlier[p]); err != nil {
				return err
			}
		}
	}

	for _, in := range sortedKeys(inAnswer) {
		e := plugincache.Edit{Add: []string{id}}
		for _, a := range earlier[in] {
			if a != id {
				e.Remove = append(e.Remove, a)
			}
		}

		if err := s.putMember(ctx, l, call, files, in, e); err != nil {
			return err
		}
	}

	if checkStored {
		return s.findable(ctx, l, call, files, sets)
	}

	return nil
}

// checkStored makes keep read back what it stored: tests set it.
var checkStored = false

// findable fails when a set just stored can't be found from one of its inputs, which would
// silently drop its answer.
func (s *Set) findable(ctx context.Context, l *Loaded, call string, files map[string]string, sets []plugin.Set) error {
	records := map[string]map[string]bool{}

	for _, set := range sets {
		id := listID(set.Inputs, set.InputUnits)

		for _, in := range set.Inputs {
			if records[in] == nil {
				records[in] = map[string]bool{}

				entries, _ := s.membersIn(ctx, s.o.Answers.Items, l, call, files, in)
				for _, e := range entries {
					records[in][e.id] = true
				}
			}

			if !records[in][id] {
				return fmt.Errorf("%w: %s %s: the set of %v can't be found from %s", ErrPlugins, l.Conf.Name, call, set.Inputs, in)
			}
		}
	}

	return nil
}

// A per-set answer's records: a path table (-paths), each input list once as gaps between its
// indices (-inputs), the answer (-answer), and per input and blob the answers holding it (-members).

type answerRecord struct {
	Lists []string `json:"lists"`
	// Paths names the path table its lists index; without it the record is older, its lists keyed
	// by their digest alone.
	Paths string `json:"paths,omitempty"`
}

// listRecord is an input list: sorted indices into a path table, each stored as its gap from the
// one before, and the input units, sorted.
type listRecord struct {
	Paths string   `json:"paths"`
	Gaps  []int    `json:"gaps"`
	Units []string `json:"units,omitempty"`
}

type inputList struct {
	inputs, units []string
}

type storedSetRecord struct {
	InputsID string     `json:"inputs_id"`
	Set      plugin.Set `json:"set"`
}

type memberEntry struct {
	id   string
	list inputList
}

// listID names a set's input list whatever the order of its files and units; a list without
// units is named by its files alone.
func listID(inputs, units []string) string {
	if len(units) == 0 {
		return inputsID(inputs)
	}

	all := slices.Clone(inputs)
	for _, u := range units {
		// no path holds a NUL, so no unit is taken for a file
		all = append(all, "\x00"+u)
	}

	return inputsID(all)
}

func inputsID(inputs []string) string {
	sorted := slices.Sorted(slices.Values(inputs))
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\x00")))

	return hex.EncodeToString(sum[:16])
}

func (s *Set) putAnswer(l *Loaded, call string, lists map[string]inputList) (string, error) {
	seen := map[string]bool{}

	for _, list := range lists {
		for _, in := range list.inputs {
			seen[in] = true
		}
	}

	paths := slices.Sorted(maps.Keys(seen))
	table := inputsID(paths)
	at := make(map[string]int, len(paths))

	for i, p := range paths {
		at[p] = i
	}

	if err := s.putJSON(l.key(call+"-paths", table), paths); err != nil {
		return "", err
	}

	ids := slices.Sorted(maps.Keys(lists))

	for _, id := range ids {
		idx := make([]int, 0, len(lists[id].inputs))
		for _, in := range lists[id].inputs {
			idx = append(idx, at[in])
		}

		slices.Sort(idx)

		gaps := make([]int, len(idx))
		for i, x := range idx {
			gaps[i] = x
			if i > 0 {
				gaps[i] = x - idx[i-1]
			}
		}

		rec := listRecord{Paths: table, Gaps: gaps, Units: sortedCopy(lists[id].units)}
		if len(rec.Units) == 0 {
			rec.Units = nil
		}

		if err := s.putJSON(listKey(l, call, table, id), rec); err != nil {
			return "", err
		}
	}

	answer := inputsID(ids)

	return answer, s.putJSON(l.key(call+"-answer", answer), answerRecord{Lists: ids, Paths: table})
}

func (s *Set) putJSON(k plugincache.Key, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlugins, err)
	}

	return s.o.Answers.Put(k, data)
}

func memberKey(l *Loaded, call string, files map[string]string, path string) plugincache.Key {
	return l.key(call+"-members", path, files[path])
}

func (s *Set) putMember(ctx context.Context, l *Loaded, call string, files map[string]string, path string, e plugincache.Edit) error {
	return s.o.Answers.Edit(ctx, memberKey(l, call, files, path), e)
}

func (s *Set) memberAnswers(ctx context.Context, l *Loaded, call string, files map[string]string, path string) ([]string, error) {
	answers, _, err := s.o.Answers.Items(ctx, memberKey(l, call, files, path))

	return answers, err
}

type answerIndex map[string][]memberEntry

func (s *Set) indexOf(ctx context.Context, l *Loaded, call, answer string) (answerIndex, bool) {
	s.mu.Lock()
	idx, ok := s.answers[answer]
	s.mu.Unlock()

	if ok {
		return idx, true
	}

	var rec answerRecord
	if !s.getJSON(ctx, l.key(call+"-answer", answer), &rec) {
		return nil, false
	}

	idx = answerIndex{}

	for _, id := range rec.Lists {
		list, ok := s.inputsOf(ctx, l, call, rec.Paths, id)
		if !ok {
			return nil, false
		}

		for _, in := range list.inputs {
			idx[in] = append(idx[in], memberEntry{id, list})
		}
	}

	s.mu.Lock()
	if s.answers == nil {
		s.answers = map[string]answerIndex{}
	}

	s.answers[answer] = idx
	s.mu.Unlock()

	return idx, true
}

func (s *Set) getJSON(ctx context.Context, k plugincache.Key, v any) bool {
	data, err := s.o.Answers.Get(ctx, k)

	return err == nil && json.Unmarshal(data, v) == nil
}

// listKey is the key of an input list stored with its path table; without a table, the older key.
func listKey(l *Loaded, call, table, id string) plugincache.Key {
	if table == "" {
		return l.key(call+"-inputs", id)
	}

	return l.key(call+"-inputs", table, id)
}

func (s *Set) inputsOf(ctx context.Context, l *Loaded, call, table, id string) (inputList, bool) {
	var rec listRecord
	if !s.getJSON(ctx, listKey(l, call, table, id), &rec) {
		return inputList{}, false
	}

	paths, ok := s.tableOf(ctx, l, call, rec.Paths)
	if !ok {
		return inputList{}, false
	}

	inputs := make([]string, 0, len(rec.Gaps))
	at := 0

	for _, gap := range rec.Gaps {
		at += gap

		if gap < 0 || at >= len(paths) {
			return inputList{}, false
		}

		inputs = append(inputs, paths[at])
	}

	return inputList{inputs, rec.Units}, true
}

func (s *Set) tableOf(ctx context.Context, l *Loaded, call, id string) ([]string, bool) {
	s.mu.Lock()
	paths, ok := s.pathTables[id]
	s.mu.Unlock()

	if ok {
		return paths, true
	}

	if !s.getJSON(ctx, l.key(call+"-paths", id), &paths) {
		return nil, false
	}

	s.mu.Lock()
	if s.pathTables == nil {
		s.pathTables = map[string][]string{}
	}

	s.pathTables[id] = paths
	s.mu.Unlock()

	return paths, true
}

func (s *Set) members(ctx context.Context, l *Loaded, call string, files map[string]string, path string) ([]memberEntry, bool) {
	return s.membersIn(ctx, s.storedItems, l, call, files, path)
}

func (s *Set) membersIn(ctx context.Context, items func(context.Context, plugincache.Key) ([]string, bool, error), l *Loaded, call string, files map[string]string, path string) ([]memberEntry, bool) {
	answers, ok, err := items(ctx, memberKey(l, call, files, path))
	if err != nil || !ok {
		return nil, false
	}

	var out []memberEntry

	seen := map[string]bool{}

	for _, a := range answers {
		idx, ok := s.indexOf(ctx, l, call, a)
		if !ok {
			return nil, false
		}

		for _, e := range idx[path] {
			if !seen[e.id] {
				seen[e.id] = true

				out = append(out, e)
			}
		}
	}

	return out, true
}

func (s *Set) storedSet(ctx context.Context, l *Loaded, call string, files map[string]string, units unitDigests, list inputList) (plugin.Set, bool) {
	k, ok := setKey(l, call, files, units, list)
	if !ok {
		return plugin.Set{}, false
	}

	data, err := s.stored(ctx, k)
	if err != nil {
		return plugin.Set{}, false
	}

	var rec storedSetRecord
	if json.Unmarshal(data, &rec) == nil && rec.InputsID != "" {
		rec.Set.Inputs = slices.Clone(list.inputs)
		rec.Set.InputUnits = slices.Clone(list.units)

		return rec.Set, true
	}

	var set plugin.Set

	return set, json.Unmarshal(data, &set) == nil
}

// stored is the answer planning reads for k; a cold pass reads the shared level past the local
// directory, to compare with what other machines would read.
func (s *Set) stored(ctx context.Context, k plugincache.Key) ([]byte, error) {
	if !s.o.Cold || s.o.Answers.Remote == nil {
		return s.o.Answers.Get(ctx, k)
	}

	data, ok, err := s.o.Answers.Stored(ctx, k)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, plugincache.ErrMiss
	}

	return data, nil
}

func (s *Set) storedItems(ctx context.Context, k plugincache.Key) ([]string, bool, error) {
	if !s.o.Cold || s.o.Answers.Remote == nil {
		return s.o.Answers.Items(ctx, k)
	}

	return s.o.Answers.StoredItems(ctx, k)
}

func (s *Set) putNothing(ctx context.Context, l *Loaded, call string, files map[string]string, path string, before []string) error {
	k, _ := setKey(l, call, files, unitDigests{}, inputList{})
	if err := s.o.Answers.Put(k, []byte(`{"group":"","inputs":[]}`)); err != nil {
		return err
	}

	return s.putMember(ctx, l, call, files, path, plugincache.Edit{Remove: before})
}

func hasRest(a answer) bool {
	return len(a.Rest.Units)+len(a.Rest.Links)+len(a.Rest.CannotTell)+len(a.Rest.Complete)+len(a.DigestFrom) > 0
}

type asked struct {
	answer

	cacheable bool
}

func (s *Set) ask(ctx context.Context, l *Loaded, call, root string, paths []string, claims []plugin.Claim) (asked, bool, error) {
	work := s.work()
	req := plugin.TreeRequest{Request: l.P.NewRequest(work), Tree: root, Paths: paths, Claimed: claims}

	var raw struct {
		plugin.Answer

		Units      []plugin.Unit       `json:"units"`
		Links      []plugin.Link       `json:"links"`
		Sets       []plugin.Set        `json:"sets"`
		CannotTell []plugin.CannotTell `json:"cannot_tell"`
		DigestFrom []plugin.DigestFrom `json:"digest_from"`
		Complete   []string            `json:"complete"`
	}

	restore := progress.Stage(ctx, "plugin %s: %s of %d files", l.Conf.Name, call, len(paths))
	err := l.P.Call(ctx, call, req, &raw)

	restore()

	if errors.Is(err, plugin.ErrCantTell) {
		s.cantTell(l, call, err)

		return asked{}, false, nil
	}

	if err == nil {
		raw.Units, err = plugin.ReadLines(work, named(raw.Files, "units"), raw.Units)
	}

	if err == nil {
		raw.Links, err = plugin.ReadLines(work, named(raw.Files, "links"), raw.Links)
	}

	if err == nil {
		raw.Sets, err = plugin.ReadLines(work, named(raw.Files, "sets"), raw.Sets)
	}

	if err == nil {
		raw.DigestFrom, err = plugin.ReadLines(work, named(raw.Files, "digest_from"), raw.DigestFrom)
	}

	if err != nil {
		return asked{}, false, err
	}

	if err := checkLinks(l, call, raw.Links, raw.Sets); err != nil {
		return asked{}, false, err
	}

	if err := checkInputUnits(l, call, raw.Sets); err != nil {
		return asked{}, false, err
	}

	out := asked{Sets: raw.Sets, DigestFrom: raw.DigestFrom, cacheable: isCacheable(raw.Answer)}
	if !out.cacheable {
		s.notCacheable(l.Conf.Name, call)
	}

	// only an sbom's units can say a file holds nothing else a test observes: a correlator's word
	// is not taken
	if l.Desc.Kind != plugin.KindSBOM {
		raw.Complete = nil

		for i := range out.Sets {
			out.Sets[i].Complete = nil
		}
	}

	if l.Desc.Inputs[call] == plugin.PerFile {
		perFile, left := splitByFile(raw.Units, raw.Links, raw.CannotTell, raw.Complete)
		out.Sets = append(out.Sets, perFile...)
		out.Rest = left
	} else {
		out.Rest = plugin.Set{Group: "", Units: raw.Units, Links: raw.Links, CannotTell: raw.CannotTell, Complete: sortedCopy(raw.Complete)}
	}

	return out, true, nil
}

// checkLinks fails a malformed link before it is cached or used: a misspelt kind would be a plain
// link, followed further than the plugin meant.
func checkLinks(l *Loaded, call string, links []plugin.Link, sets []plugin.Set) error {
	for _, set := range sets {
		links = slices.Concat(links, set.Links)
	}

	for _, ln := range links {
		if p := ln.Problem(); p != "" {
			return fmt.Errorf("%w: plugin %s (%s): %s", ErrPlugins, l.Conf.Name, call, p)
		}
	}

	return nil
}

// checkInputUnits sorts each set's input units and fails an sbom's set that names any: an sbom's
// digests are what input units are keyed on.
func checkInputUnits(l *Loaded, call string, sets []plugin.Set) error {
	for i := range sets {
		if len(sets[i].InputUnits) == 0 {
			continue
		}

		if l.Desc.Kind == plugin.KindSBOM {
			return fmt.Errorf("%w: plugin %s (%s): the set of %s names input units, which only a correlator's set may", ErrPlugins, l.Conf.Name, call, sets[i].Group)
		}

		sets[i].InputUnits = slices.Compact(sortedCopy(sets[i].InputUnits))
	}

	return nil
}

func splitByFile(units []plugin.Unit, links []plugin.Link, cannot []plugin.CannotTell, complete []string) ([]plugin.Set, plugin.Set) {
	sets := map[string]*plugin.Set{}
	fileOf := map[string]string{}
	left := plugin.Set{}

	get := func(f string) *plugin.Set {
		if sets[f] == nil {
			sets[f] = &plugin.Set{Group: f, Inputs: []string{f}}
		}

		return sets[f]
	}

	for _, u := range units {
		if u.File == "" {
			left.Units = append(left.Units, u)

			continue
		}

		fileOf[u.ID] = u.File
		get(u.File).Units = append(get(u.File).Units, u)
	}

	for _, ln := range links {
		f := fileOf[ln.From]
		if f == "" && strings.HasPrefix(ln.From, "file:") {
			f = strings.TrimPrefix(ln.From, "file:")
		}

		if f == "" {
			left.Links = append(left.Links, ln)

			continue
		}

		get(f).Links = append(get(f).Links, ln)
	}

	for _, c := range cannot {
		if f := fileOf[c.Unit]; f != "" {
			get(f).CannotTell = append(get(f).CannotTell, c)
		} else {
			left.CannotTell = append(left.CannotTell, c)
		}
	}

	for _, f := range sortedCopy(complete) {
		if f != "" && !slices.Contains(get(f).Complete, f) {
			get(f).Complete = append(get(f).Complete, f)
		}
	}

	out := make([]plugin.Set, 0, len(sets))
	for _, f := range sortedKeys(sets) {
		out = append(out, *sets[f])
	}

	return out, left
}

func (s *Set) putSet(l *Loaded, call string, files map[string]string, units unitDigests, set plugin.Set) error {
	k, ok := setKey(l, call, files, units, inputList{set.Inputs, set.InputUnits})
	if !ok {
		return nil
	}

	stored := set
	stored.Inputs, stored.InputUnits = nil, nil

	data, err := json.Marshal(storedSetRecord{InputsID: listID(set.Inputs, set.InputUnits), Set: stored})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlugins, err)
	}

	return s.o.Answers.Put(k, data)
}

// setKey is a set's key: its inputs' blobs (none for a deleted one) and its input units' digests
// in this tree; false when a digest is unknown, so the set is neither served nor kept.
func setKey(l *Loaded, call string, files map[string]string, units unitDigests, list inputList) (plugincache.Key, bool) {
	content := make([]string, 0, len(list.inputs)+len(list.units))
	for _, in := range list.inputs {
		blob, ok := files[in]
		if !ok {
			blob = absent
		}

		content = append(content, in+"@"+blob)
	}

	for _, u := range sortedCopy(list.units) {
		d, ok := units.of(u)
		if !ok {
			return plugincache.Key{}, false
		}

		// a NUL keeps a unit apart from any path
		content = append(content, "\x00"+u+"@"+d)
	}

	return l.key(call+"-set", content...), true
}

func (s *Set) cantTell(l *Loaded, call string, err error) {
	why := ""
	if msg := plugin.CantTellMessage(err); msg != "" {
		why = ": " + msg
	}

	if isToolchain(err) {
		s.warnf("plugin %s could not tell: toolchain (%s)%s; %s", l.Conf.Name, call, why, untoldMeans(l))

		return
	}

	s.warnf("plugin %s could not tell (%s)%s; %s", l.Conf.Name, call, why, untoldMeans(l))
}

func untoldMeans(l *Loaded) string {
	if l.Desc.Kind == plugin.KindSBOM {
		return "every recorded slice that reached its namespaces or owns one of its files depends on each file it was asked about"
	}

	return "every recorded slice depends on everything"
}

func isCacheable(a plugin.Answer) bool { return a.Cacheable == nil || *a.Cacheable }

func claimPaths(claims []plugin.Claim) []string {
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.Path)
	}

	return out
}

func named(files []string, name string) []string {
	var out []string

	for _, f := range files {
		if strings.HasSuffix(f, "/"+name+".jsonl") || f == name+".jsonl" {
			out = append(out, f)
		}
	}

	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

func slicesClone[T any](s []T) []T { return append([]T(nil), s...) }
