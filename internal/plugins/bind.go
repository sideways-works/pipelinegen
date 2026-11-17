package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/progress"
	"github.com/sideways-works/pipelinegen/plugin"
)

// The widths a slice is bound in.
const (
	Executed   = "executed"
	Dependents = "dependents"
)

// BindSlice is a slice as binding needs it.
type BindSlice struct {
	Suite   string
	Name    string
	Sources []string
	Tests   []string
	// List is the unit ids the slice was recorded touching; without a
	// recording (HasList false) it binds everything.
	List    []string
	HasList bool
	// Depth limits the dependents width's steps against the links; 0 is
	// no limit.
	Depth int
	Data  []string
	Units []string
	// Declared are the labels among Data the test file names in its own
	// pipelinegen:data lines, which no narrowing takes away.
	Declared []string
	// Tops are the data labels as the slice names them, and Groups the
	// filegroups among them opened: Data then holds what they stand for.
	Tops   []string
	Groups *bazel.Groups
	Setup  bool
	// Graph marks a slice of a suite bound by the Bazel graph: it never
	// records, so it depends on its own files' units and its data.
	Graph bool
}

const setupSlice = "_setup"

// InGroup tells a data label found inside one of the slice's filegroups
// from one the slice names itself.
func (sl BindSlice) InGroup(label string) bool {
	return sl.Groups != nil && !slices.Contains(sl.Tops, label)
}

func (sl BindSlice) answerName() string {
	if sl.Setup {
		return setupSlice
	}

	return sl.Name
}

const whyData = "data"

const whyOwn = "own"

// maxRounds bounds the rounds between bind correlators and links: two
// suffice in practice.
const maxRounds = 4

// Bind gives each slice its dependencies in both widths.
func (s *Set) Bind(ctx context.Context, root string, t *deps.Tree, slices []BindSlice) ([]deps.Slice, error) {
	root = RealPath(root)
	g := newLinkGraph(OwnFiles(root, t, slices))
	out := make([]deps.Slice, 0, 2*len(slices))

	bound, leaves := make([]map[string]*deps.Slice, len(slices)), NewLeaves(s.config(), t, slices)

	for i, sl := range slices {
		bound[i] = map[string]*deps.Slice{}

		for _, width := range []string{Executed, Dependents} {
			d := &deps.Slice{
				Suite: sl.Suite, Name: sl.Name, Width: width, Why: map[string]deps.Why{}, Everything: !sl.HasList && !sl.Graph,
				Dropped: map[string][]string{}, DroppedGroups: map[string][]string{},
			}
			d.Data = data(leaves, sl)

			switch {
			case sl.HasList:
				g.close(d, roots(t, sl), width == Dependents, sl.Depth)
			case sl.Graph:
				// its own files' units only: what they reach is the owner's
				// graph, in its data
				for id, why := range roots(t, BindSlice{Sources: sl.Sources}) {
					d.Why[id] = why
					g.add(d, id)
				}

				named := map[string]deps.Why{}
				for _, id := range sl.Units {
					named[id] = deps.Why{Kind: whyData}
				}

				g.close(d, named, width == Dependents, sl.Depth)
			}

			bound[i][width] = d
		}
	}

	if err := s.bindRounds(ctx, root, g, slices, bound); err != nil {
		return nil, err
	}

	wide := newWideGraphs(t)
	pins := pinSources{groupBy: groupProviders(t), answers: s.answerSets(t)}

	for i, sl := range slices {
		for _, width := range []string{Executed, Dependents} {
			d := bound[i][width]
			ix := unitIndex{}

			if sl.HasList {
				wide.dropped(g, d, sl, width == Dependents)
				bindUnbound(t, d, ix)
				s.bindUntold(t, d, sl, g, ix)
			}

			s.bindPins(t, d, sl, pins, ix)

			sort.Strings(d.Units)
			sort.Strings(d.Groups)

			for n := range d.Dropped {
				d.Dropped[n] = sortedCopy(d.Dropped[n])
			}

			out = append(out, *d)
		}
	}

	return out, nil
}

const whyUntold = "cannot-tell"

// bindUntold widens a recorded slice for each plugin that couldn't tell
// about the tree: for a correlator the slice depends on everything, for an
// sbom on each file it couldn't tell about once the slice reached its
// namespaces.
func (s *Set) bindUntold(t *deps.Tree, d *deps.Slice, sl BindSlice, g *linkGraph, ix unitIndex) {
	for _, name := range sortedKeys(t.Untold) {
		l := s.loaded(name)
		if l == nil || l.Desc.Kind != plugin.KindSBOM {
			d.Everything = true

			continue
		}

		files := t.Untold[name]
		if !reaches(d, sl, l.Desc.Namespaces, g.untold[name]) {
			continue
		}

		for _, f := range files {
			id := deps.FileID(f)

			ix.add(&d.Units, id)

			if _, ok := d.Why[id]; !ok {
				d.Why[id] = deps.Why{Kind: whyUntold, By: name}
			}
		}
	}
}

func reaches(d *deps.Slice, sl BindSlice, namespaces []string, files map[string]bool) bool {
	if len(namespaces) == 0 {
		return true
	}

	for _, src := range sl.Sources {
		if files[src] {
			return true
		}
	}

	for id := range d.Why {
		for _, ns := range namespaces {
			if strings.HasPrefix(id, ns+":") {
				return true
			}
		}
	}

	return false
}

func (s *Set) loaded(name string) *Loaded {
	for _, l := range s.Plugins {
		if l.Conf.Name == name {
			return l
		}
	}

	return nil
}

const whyPin = "plugin"

func (s *Set) bindPins(t *deps.Tree, d *deps.Slice, sl BindSlice, pins pinSources, ix unitIndex) {
	if d.Everything {
		return
	}

	pin := func(name string) {
		id, ok := t.Pins[name]
		if !ok {
			return
		}

		ix.add(&d.Units, id)

		if _, ok := d.Why[id]; !ok {
			d.Why[id] = deps.Why{Kind: whyPin}
		}
	}

	for _, id := range slices.Clone(d.Units) {
		if p, ok := t.Providers[id]; ok {
			pin(p)
		}
	}

	for _, g := range d.Groups {
		for _, p := range pins.groupBy[g] {
			pin(p)
		}
	}

	for _, l := range s.Plugins {
		if l.Desc.Kind != plugin.KindCorrelator {
			continue
		}

		name := l.Conf.Name

		if sl.HasList && s.Observes(sl.Suite, name) || restsOn(bindingUnits(d, sl), pins.answers[name]) {
			pin(name)
		}
	}
}

// Observes tells whether a correlator observes a suite's slices.
func (s *Set) Observes(suite, correlator string) bool {
	return s.o.Config == nil || slices.Contains(s.o.Config.CorrelatorsFor(suite), correlator)
}

// AnswerUnits are what a binding holds when it rests on a correlator's
// answers in a tree.
func AnswerUnits(t *deps.Tree, correlator string) map[string]bool {
	out := map[string]bool{}

	for _, id := range t.CorrelatorEnds[correlator] {
		out[id] = true
	}

	for _, f := range t.CorrelatorFiles[correlator] {
		for _, id := range t.UnitsOf[f] {
			out[id] = true
		}
	}

	return out
}

type pinSources struct {
	groupBy map[string][]string
	answers map[string]map[string]bool
}

func (s *Set) answerSets(t *deps.Tree) map[string]map[string]bool {
	out := map[string]map[string]bool{}

	for _, l := range s.Plugins {
		if l.Desc.Kind == plugin.KindCorrelator {
			out[l.Conf.Name] = AnswerUnits(t, l.Conf.Name)
		}
	}

	return out
}

func bindingUnits(d *deps.Slice, sl BindSlice) []string {
	if !sl.Graph {
		return d.Units
	}

	var out []string

	for _, id := range d.Units {
		if d.Why[id].Kind != whyOwn {
			out = append(out, id)
		}
	}

	return out
}

func restsOn(units []string, answers map[string]bool) bool {
	return slices.ContainsFunc(units, func(id string) bool { return answers[id] })
}

func groupProviders(t *deps.Tree) map[string][]string {
	out := map[string][]string{}

	for id, p := range t.Providers {
		if g := t.Units[id].Group; g != "" && !slices.Contains(out[g], p) {
			out[g] = append(out[g], p)
		}
	}

	return out
}

const whyUnbound = "unbound"

// bindUnbound makes d depend on the unbound names of each group it links,
// through one unit per group and width whose digest is the list of names: a
// name that appears or goes selects the slice, a changed body doesn't.
func bindUnbound(t *deps.Tree, d *deps.Slice, ix unitIndex) {
	for _, group := range d.Linked {
		ub, ok := t.Unbound[d.Width][group]
		if !ok {
			continue
		}

		id := deps.UnboundID(d.Width, group)
		if _, ok := t.Units[id]; !ok {
			t.Units[id] = deps.Unit{ID: id, Digest: namesDigest(ub.Names)}
		}

		ix.add(&d.Units, id)

		if _, ok := d.Why[id]; !ok {
			d.Why[id] = deps.Why{Kind: whyUnbound}
		}
	}
}

func namesDigest(names []string) string {
	h := sha256.New()

	for _, n := range sortedCopy(names) {
		h.Write([]byte(n + "\n"))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// OwnFiles gives each recorded slice's own test file that no plugin lists a
// unit in (a shell or YAML test) a unit of its own, file:<path>, whose
// digest is the file's content. Planning calls it before the walk, so the
// walk and Bind see the same units; a second call adds nothing.
func OwnFiles(root string, t *deps.Tree, slices []BindSlice) *deps.Tree {
	var covered map[string]bool

	for _, sl := range slices {
		if sl.Graph {
			continue
		}

		for _, src := range sl.Sources {
			id := deps.FileID(src)
			if _, ok := t.Units[id]; ok || len(t.FileUnits[src]) > 0 {
				continue
			}

			if covered == nil {
				covered = filesWithUnits(t)
			}

			if covered[src] {
				continue
			}

			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(src))) //nolint:gosec // a test file of the tree planned
			if err != nil {
				continue // absent from this tree: the slice is new, or gone
			}

			sum := sha256.Sum256(data)
			t.Units[id] = deps.Unit{ID: id, Group: src, File: src, Digest: hex.EncodeToString(sum[:])}
		}
	}

	return t
}

func filesWithUnits(t *deps.Tree) map[string]bool {
	out := make(map[string]bool, len(t.Units))

	for _, u := range t.Units {
		out[u.Group] = true

		if u.File != "" {
			out[u.File] = true
		}
	}

	return out
}

func roots(t *deps.Tree, sl BindSlice) map[string]deps.Why {
	out := map[string]deps.Why{}

	for _, id := range sl.List {
		out[id] = deps.Why{Kind: "executed"}
	}

	for _, id := range sl.Units {
		out[id] = deps.Why{Kind: whyData}
	}

	for _, f := range sl.Sources {
		out["file:"+f] = deps.Why{Kind: whyOwn}

		for _, id := range t.FileUnits[f] {
			out[id] = deps.Why{Kind: whyOwn}
		}
	}

	for id, u := range t.Units {
		for _, f := range sl.Sources {
			if u.Group == f {
				out[id] = deps.Why{Kind: whyOwn}
			}
		}
	}

	return out
}

// data is a slice's data labels less those Leaves judges replaced: a file
// whose units the slice touched are roots already, and a BUILD file.
func data(f *Leaves, sl BindSlice) []string {
	var out []string

	for _, label := range sl.Data {
		if j := f.Judge(sl, label, sl.InGroup(label)); j == LeafUnits || j == LeafBuild {
			continue
		}

		out = append(out, label)
	}

	return out
}

// RecordedSBOMs are, per suite, the sboms some slice's list holds a unit
// of: the recordings of that suite hold that sbom's units.
func RecordedSBOMs(t *deps.Tree, bs []BindSlice) map[string]map[string]bool {
	out := map[string]map[string]bool{}

	for _, sl := range bs {
		if out[sl.Suite] == nil {
			out[sl.Suite] = map[string]bool{}
		}

		for _, id := range sl.List {
			if p := t.Providers[id]; p != "" {
				out[sl.Suite][p] = true
			}
		}
	}

	return out
}

// ProviderOf is the sbom that lists a file's units.
func ProviderOf(t *deps.Tree, units []string) string {
	for _, id := range units {
		if p := t.Providers[id]; p != "" {
			return p
		}
	}

	return ""
}

type linkGraph struct {
	t       *deps.Tree
	forward map[string][]string
	reverse map[string][]string
	// units knows what each slice's groups hold, so adding one is a lookup,
	// not a scan.
	units  unitIndex
	untold map[string]map[string]bool
	backs  map[*deps.Slice]map[string]int
}

type unitIndex map[*[]string]map[string]bool

// add appends v to the list once; a list grown by other means since is read
// again.
func (ix unitIndex) add(list *[]string, v string) {
	set := ix[list]
	if set == nil || len(set) != len(*list) {
		set = make(map[string]bool, len(*list)+1)
		for _, have := range *list {
			set[have] = true
		}

		ix[list] = set
	}

	if !set[v] {
		set[v] = true
		*list = append(*list, v)
	}
}

func newLinkGraph(t *deps.Tree) *linkGraph {
	g := &linkGraph{
		t: t, forward: map[string][]string{}, reverse: map[string][]string{},
		units: unitIndex{}, untold: map[string]map[string]bool{}, backs: map[*deps.Slice]map[string]int{},
	}

	for name, files := range t.Untold {
		g.untold[name] = make(map[string]bool, len(files))
		for _, f := range files {
			g.untold[name][f] = true
		}
	}

	for _, ln := range t.Links {
		g.forward[ln.From] = append(g.forward[ln.From], ln.To)
		g.reverse[ln.To] = append(g.reverse[ln.To], ln.From)
	}

	return g
}

func (g *linkGraph) close(d *deps.Slice, roots map[string]deps.Why, dependents bool, depth int) {
	if dependents && depth > 0 {
		g.closeWithin(d, roots, depth)

		return
	}

	type item struct {
		id   string
		back int
	}

	queue := make([]item, 0, len(roots))

	for _, id := range sortedKeys(roots) {
		if _, ok := d.Why[id]; !ok {
			d.Why[id] = roots[id]
			queue = append(queue, item{id: id})
		}
	}

	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]

		g.add(d, it.id)

		next := g.forward[it.id]
		for _, to := range next {
			if _, ok := d.Why[to]; !ok {
				d.Why[to] = deps.Why{Kind: "closure", Via: it.id}
				queue = append(queue, item{id: to, back: it.back})
			}
		}

		if !dependents || (depth > 0 && it.back >= depth) {
			continue
		}

		for _, from := range g.reverse[it.id] {
			if _, ok := d.Why[from]; !ok {
				d.Why[from] = deps.Why{Kind: "dependent", Via: it.id}
				queue = append(queue, item{id: from, back: it.back + 1})
			}
		}
	}
}

// closeWithin is the dependents closure under a depth: a 0-1 breadth-first
// search where a step back costs one, so each unit keeps the fewest steps
// back of any path. The fewest steps are kept across calls, so a root a bind
// correlator adds later starts at none.
func (g *linkGraph) closeWithin(d *deps.Slice, roots map[string]deps.Why, depth int) {
	backs := g.backs[d]
	if backs == nil {
		backs = map[string]int{}
		g.backs[d] = backs
	}

	var q stepQueue

	push := func(id string, b int, why deps.Why, ahead bool) {
		have, known := backs[id]
		_, bound := d.Why[id]

		// a unit bound by other means than a closure (a slice's own files
		// bound by the Bazel graph) is not walked from, as without a depth
		if (known && have <= b) || (bound && !known) {
			return
		}

		d.Why[id] = why
		backs[id] = b

		if !bound {
			g.add(d, id)
		}

		q.push(step{id: id, back: b}, ahead)
	}

	for _, id := range sortedKeys(roots) {
		push(id, 0, roots[id], false)
	}

	for it, ok := q.pop(); ok; it, ok = q.pop() {
		if backs[it.id] != it.back {
			continue // reached since with fewer steps back
		}

		for _, to := range g.forward[it.id] {
			push(to, it.back, deps.Why{Kind: "closure", Via: it.id}, true)
		}

		if it.back < depth {
			for _, from := range g.reverse[it.id] {
				push(from, it.back+1, deps.Why{Kind: "dependent", Via: it.id}, false)
			}
		}
	}
}

type step struct {
	id   string
	back int
}

// stepQueue is a deque: a step with no step back goes to the head, one with
// a step back to the tail.
type stepQueue struct {
	head []step // its last element is next
	tail []step
}

func (q *stepQueue) push(s step, ahead bool) {
	if ahead {
		q.head = append(q.head, s)
	} else {
		q.tail = append(q.tail, s)
	}
}

func (q *stepQueue) pop() (step, bool) {
	if n := len(q.head); n > 0 {
		s := q.head[n-1]
		q.head = q.head[:n-1]

		return s, true
	}

	if len(q.tail) == 0 {
		return step{}, false
	}

	s := q.tail[0]
	q.tail = q.tail[1:]

	return s, true
}

func within(dependents bool, depth int) bool { return dependents && depth > 0 }

func (g *linkGraph) add(d *deps.Slice, id string) {
	u, ok := g.t.Units[id]
	if !ok {
		return
	}

	// only a unit new to the reasons gets here, so no set is needed to keep
	// it once
	d.Units = append(d.Units, id)

	if _, wide := g.t.CannotTell[id]; wide && u.Group != "" {
		g.units.add(&d.Groups, u.Group)
	}
}

func appendNew(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}

	return append(s, v)
}

type bindRecord struct {
	Suite         string              `json:"suite"`
	Slice         string              `json:"slice"`
	Width         string              `json:"width"`
	WholeGroups   []string            `json:"whole_groups"`
	Linked        []string            `json:"linked"`
	Everything    bool                `json:"everything"`
	Units         []bindUnit          `json:"units"`
	Dropped       map[string][]int    `json:"dropped"`
	DroppedGroups map[string][]string `json:"dropped_groups"`
}

type unboundLine struct {
	Group    string              `json:"group"`
	Width    string              `json:"width"`
	Names    []string            `json:"names"`
	Narrowed map[string][]string `json:"narrowed"`
}

func (s *Set) bindRounds(ctx context.Context, root string, g *linkGraph, slices []BindSlice, bound []map[string]*deps.Slice) error {
	var binders []*Loaded

	for _, l := range s.Plugins {
		if l.can(plugin.CallBind) {
			binders = append(binders, l)
		}
	}

	// the unbound names of each binder's last round: they shrink as roots
	// cross between correlators
	unbound := make([][]unboundLine, len(binders))

	// each binder's last request: asked the same again in this tree, it
	// answers the same, so it isn't asked
	last := make([][]byte, len(binders))

	defer func() { g.t.Unbound = unboundOf(unbound) }()

	for range maxRounds {
		grew := false

		for i, l := range binders {
			req := bindRequest(l, root, g.t.Narrowings, slices, bound)

			same, err := json.Marshal(req)
			if err != nil {
				return fmt.Errorf("%w: %s bind: %w", ErrPlugins, l.Conf.Name, err)
			}

			if bytes.Equal(same, last[i]) {
				continue
			}

			last[i] = same

			more, lines, ok, err := s.bindOnce(ctx, l, req)
			if err != nil {
				return err
			}

			// what it would have bound is unknown: every slice it is asked
			// about depends on everything
			if !ok {
				untoldBinder(slices, bound)

				continue
			}

			unbound[i] = lines

			if merge(g, l.Conf.Name, slices, bound, more) {
				grew = true
			}
		}

		if !grew {
			return nil
		}
	}

	return nil
}

func untoldBinder(slices []BindSlice, bound []map[string]*deps.Slice) {
	for i, sl := range slices {
		if sl.Graph {
			continue
		}

		for _, d := range bound[i] {
			d.Everything = true
		}
	}
}

func unboundOf(answers [][]unboundLine) map[string]map[string]deps.Unbound {
	out := map[string]map[string]deps.Unbound{}

	for _, lines := range answers {
		for _, ln := range lines {
			if out[ln.Width] == nil {
				out[ln.Width] = map[string]deps.Unbound{}
			}

			ub := out[ln.Width][ln.Group]
			ub.Names = mergeStrings(ub.Names, ln.Names)

			for n, ids := range ln.Narrowed {
				if ub.Narrowed == nil {
					ub.Narrowed = map[string][]string{}
				}

				ub.Narrowed[n] = mergeStrings(ub.Narrowed[n], ids)
			}

			out[ln.Width][ln.Group] = ub
		}
	}

	return out
}

func merge(g *linkGraph, name string, slices []BindSlice, bound []map[string]*deps.Slice, more map[string]*deps.Slice) bool {
	grew := false

	for i, sl := range slices {
		for _, width := range []string{Executed, Dependents} {
			extra := more[sl.Suite+"/"+sl.answerName()+"/"+width]
			if extra == nil {
				continue
			}

			d := bound[i][width]
			before := len(d.Units) + len(d.Groups)

			d.Everything = d.Everything || extra.Everything
			d.Groups = mergeStrings(d.Groups, extra.Groups)
			d.Linked = mergeStrings(d.Linked, extra.Linked)

			for n, ids := range extra.Dropped {
				d.Dropped[n] = mergeStrings(d.Dropped[n], ids)
			}

			for n, gs := range extra.DroppedGroups {
				d.DroppedGroups[n] = mergeStrings(d.DroppedGroups[n], gs)
			}

			fresh := map[string]deps.Why{}

			for _, id := range extra.Units {
				why := extra.Why[id]
				why.By = name

				if have, ok := d.Why[id]; ok {
					// bound already: what else brings it along still counts
					have.Vias = mergeStrings(have.Vias, why.Vias)
					d.Why[id] = have

					// under a depth, a unit the links reached some steps
					// back is a root now, and is walked from again
					if b, known := g.backs[d][id]; known && b > 0 && within(width == Dependents, sl.Depth) {
						why.Vias = have.Vias
						fresh[id] = why
					}

					continue
				}

				fresh[id] = why
			}

			g.close(d, fresh, width == Dependents, sl.Depth)

			grew = grew || len(d.Units)+len(d.Groups) > before
		}
	}

	return grew
}

// bindRequest is what a bind correlator is asked about the slices: their
// lists, and as roots what the slice holds that the correlator didn't bind
// itself, since that is its own answer to the same request.
func bindRequest(l *Loaded, root string, narrowings []string, slices []BindSlice, bound []map[string]*deps.Slice) map[string]any {
	reqSlices := make([]map[string]any, 0, len(slices))
	rootsBy := map[string][]string{}
	setup := map[string][]string{}

	for i, sl := range slices {
		if sl.Setup {
			setup[sl.Suite] = sl.List

			continue
		}

		if sl.Graph {
			continue
		}

		reqSlices = append(reqSlices, map[string]any{
			"suite": sl.Suite, "name": sl.Name, "sources": sl.Sources, "tests": sl.Tests,
			"list": sl.List, "depth": sl.Depth,
		})

		d := bound[i][Executed]
		roots := make([]string, 0, len(d.Units))

		for _, id := range d.Units {
			if d.Why[id].By != l.Conf.Name {
				roots = append(roots, id)
			}
		}

		rootsBy[sl.Name] = roots
	}

	return map[string]any{
		"protocol": plugin.Protocol, "cache": l.P.Cache, "config": l.Conf.Config,
		"tree": root, "narrowings": narrowings, "widths": []string{Executed, Dependents},
		"slices": reqSlices, "roots": rootsBy, "setup": setup,
	}
}

func (s *Set) bindOnce(ctx context.Context, l *Loaded, req map[string]any) (map[string]*deps.Slice, []unboundLine, bool, error) {
	work := s.work()
	req["work"] = work

	var ans plugin.Answer

	restore := progress.Stage(ctx, "plugin %s: bind", l.Conf.Name)
	err := l.P.Call(ctx, plugin.CallBind, req, &ans)

	restore()

	if errors.Is(err, plugin.ErrCantTell) {
		s.cantTell(l, plugin.CallBind, err)

		return nil, nil, false, nil
	}

	if err != nil {
		return nil, nil, false, err
	}

	ids, err := plugin.ReadLines[string](work, named(ans.Files, "units"), nil)
	if err != nil {
		return nil, nil, false, err
	}

	records, err := plugin.ReadLines[bindRecord](work, named(ans.Files, "bind"), nil)
	if err != nil {
		return nil, nil, false, err
	}

	unbound, err := plugin.ReadLines[unboundLine](work, named(ans.Files, "unbound"), nil)
	if err != nil {
		return nil, nil, false, err
	}

	out := map[string]*deps.Slice{}

	at := func(i int) (string, error) {
		if i < 0 || i >= len(ids) {
			return "", fmt.Errorf("%w: %s bind: a unit index outside units.jsonl", ErrPlugins, l.Conf.Name)
		}

		return ids[i], nil
	}

	for _, r := range records {
		d, err := r.slice(at)
		if err != nil {
			return nil, nil, false, err
		}

		out[r.Suite+"/"+r.Slice+"/"+r.Width] = d
	}

	return out, unbound, true, nil
}

func (r bindRecord) slice(at func(int) (string, error)) (*deps.Slice, error) {
	d := &deps.Slice{
		Everything: r.Everything, Groups: r.WholeGroups, Linked: r.Linked, Why: map[string]deps.Why{},
		Dropped: map[string][]string{}, DroppedGroups: r.DroppedGroups,
	}

	for _, u := range r.Units {
		id, why, err := unitEntry(u, at)
		if err != nil {
			return nil, err
		}

		d.Units = append(d.Units, id)
		d.Why[id] = why
	}

	for n, idxs := range r.Dropped {
		for _, i := range idxs {
			id, err := at(i)
			if err != nil {
				return nil, err
			}

			d.Dropped[n] = append(d.Dropped[n], id)
		}
	}

	if d.DroppedGroups == nil {
		d.DroppedGroups = map[string][]string{}
	}

	return d, nil
}

func unitEntry(u bindUnit, at func(int) (string, error)) (string, deps.Why, error) {
	if !u.hasIndex {
		return "", deps.Why{}, fmt.Errorf("%w: a bind unit without its index", ErrPlugins)
	}

	id, err := at(u.index)
	if err != nil {
		return "", deps.Why{}, err
	}

	why := deps.Why{Kind: u.kind}

	if u.hasVia && u.via >= 0 {
		if why.Via, err = at(u.via); err != nil {
			return "", deps.Why{}, err
		}
	}

	for _, n := range u.vias {
		via, err := at(n)
		if err != nil {
			return "", deps.Why{}, err
		}

		why.Vias = append(why.Vias, via)
	}

	return id, why, nil
}

func mergeStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}

	if len(a)+len(b) <= smallMerge {
		for _, v := range b {
			a = appendNew(a, v)
		}

		return a
	}

	have := make(map[string]bool, len(a)+len(b))
	for _, v := range a {
		have[v] = true
	}

	for _, v := range b {
		if !have[v] {
			have[v] = true
			a = append(a, v)
		}
	}

	return a
}

// smallMerge is the size up to which scanning beats building a set.
const smallMerge = 16
