package plugins

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/progress"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Tree asks every plugin about the tree at root (its HEAD the commit planned) and assembles at
// the narrowing level the units with resolved digests, the links in force, and the untold.
func (s *Set) Tree(ctx context.Context, root, commit string, level int) (*deps.Tree, error) {
	root = RealPath(root)

	files, err := s.filledBlobs(ctx, root)
	if err != nil {
		return nil, err
	}

	// paths a Mac holds as one file are no plugin's: none is asked about them, so a change to one
	// is a change to a file no plugin claims
	asked := s.apartOnEveryFilesystem(files)

	g := &gather{
		units: map[string]plugin.Unit{}, digestFrom: map[string][]string{}, digestBy: map[string]string{}, by: map[string]string{},
		untold: map[string][]string{}, complete: map[string]string{}, linkBy: map[[2]string]string{},
	}
	t := &deps.Tree{
		Commit: commit, Units: map[string]deps.Unit{}, CannotTell: map[string]string{}, CannotTellBy: map[string]string{}, FileUnits: map[string][]string{},
		Narrowings: s.InForce(level), Providers: map[string]string{}, LinkBy: g.linkBy,
	}

	defer progress.Stage(ctx, "the plugins on %s", commit[:min(len(commit), 12)])()

	if err := s.gatherAll(ctx, root, asked, g); err != nil {
		return nil, err
	}

	s.correlatorDigests(g, level)
	resolveDigests(g, t)

	maps.Copy(t.Providers, g.by)

	untoldFiles(t, g.untold, files)

	s.pins(root, t)

	// typed links stay apart from plain ones: the forward closure follows every plain link, and
	// must not follow a typed one as if it were
	plain, typed := splitTyped(g.links)

	if t.Links, t.RuledOut, err = s.inForce(plain, level); err != nil {
		return nil, err
	}

	if t.Typed, t.TypedRuledOut, err = s.inForce(typed, level); err != nil {
		return nil, err
	}

	// in one order whether the sets came from the cache or the plugin (the walk's reasons follow
	// it), each link once
	t.Typed = mergeLinks(t.Typed)

	for n, links := range t.TypedRuledOut {
		t.TypedRuledOut[n] = mergeLinks(links)
	}

	for _, ln := range t.Links {
		if dangling(t, ln.To) {
			cannotTell(t, ln.From, g.linkBy[[2]string{ln.From, ln.To}], "links to "+ln.To+", which no plugin describes")
		}
	}

	// a typed link to nothing is a naming convention that didn't hold, as for a plain one: its
	// source binds wide, and the plan says which
	for _, ln := range t.Typed {
		if dangling(t, ln.To) {
			cannotTell(t, ln.From, g.typedBy[[2]string{ln.From, ln.To}], "links to "+ln.To+", which no plugin describes")
			s.warnf("plugin %s: a %s link from %s to %s, which no plugin describes: %s binds wide", g.typedBy[[2]string{ln.From, ln.To}], ln.Kind, ln.From, ln.To, ln.From)
		}
	}

	for _, c := range g.cannot {
		cannotTell(t, c.Unit, c.by, c.Reason)
	}

	slices.Sort(g.inputUnits)
	t.InputUnits = slices.Compact(g.inputUnits)

	s.byUnitsInForce(t, g, level)
	t.Complete, t.UnitsOf = g.complete, g.unitsOf
	t.CorrelatorEnds, t.CorrelatorFiles = nearEnds(t, g.correlatorLinks), sortedSets(g.correlatorFiles)

	return t, nil
}

func (s *Set) byUnitsInForce(t *deps.Tree, g *gather, level int) {
	t.ByUnits, t.Anchors = map[string]string{}, map[string]string{}

	for _, c := range g.byUnits {
		if !s.on(c.ByUnits, level) {
			continue
		}

		t.FileUnits[c.Path] = g.unitsOf[c.Path]
		t.ByUnits[c.Path] = c.ByUnits

		if c.Anchor != "" {
			t.Anchors[c.Path] = c.Anchor
		}
	}
}

// gatherAll asks every plugin about the tree, the sboms first: a correlator's set may be keyed
// on the digests of units they list.
func (s *Set) gatherAll(ctx context.Context, root string, files map[string]string, g *gather) error {
	for _, sbom := range []bool{true, false} {
		if !sbom {
			g.inputs = s.unitDigests(g, files)
		}

		for _, l := range s.Plugins {
			if (l.Desc.Kind == plugin.KindSBOM) != sbom {
				continue
			}

			if err := s.gatherPlugin(ctx, l, root, files, g); err != nil {
				return err
			}

			g.asked++
			progress.Count(ctx, g.asked, len(s.Plugins), "plugins")
		}
	}

	return nil
}

type saidCannot struct {
	plugin.CannotTell

	by string
}

type saidDigest struct {
	plugin.DigestFrom

	by string
}

func cannotTell(t *deps.Tree, id, by, why string) {
	t.CannotTell[id] = why
	t.CannotTellBy[id] = by
}

const absent = "absent"

type gather struct {
	asked             int
	units             map[string]plugin.Unit
	unitsOf           map[string][]string
	links             []plugin.Link
	typedBy           map[[2]string]string
	linkBy            map[[2]string]string
	correlatorLinks   map[string][][2]string
	correlatorFiles   map[string]map[string]bool
	cannot            []saidCannot
	inputUnits        []string
	digestFrom        map[string][]string
	digestBy          map[string]string
	correlatorDigests []saidDigest
	by                map[string]string
	byUnits           []plugin.Claim
	complete          map[string]string
	untold            map[string][]string
	inputs            unitDigests
}

// unitDigests are what a correlator's set with input units is keyed on: the sboms' units, before
// any correlator's digest_from, which comes from the very answers being keyed.
type unitDigests struct {
	digest map[string]string
	files  map[string]string
	// covered are the namespaces whose sboms told about every file asked about: only there is a
	// unit's absence known.
	covered map[string]bool
}

// of is the digest a set naming id as an input unit is keyed on; false when nothing can be
// said, and the set is then neither served nor kept.
func (u unitDigests) of(id string) (string, bool) {
	if p, ok := strings.CutPrefix(id, "file:"); ok {
		if blob, ok := u.files[p]; ok {
			return blob, true
		}

		return absent, true
	}

	if d, ok := u.digest[id]; ok {
		return d, d != ""
	}

	if ns, _, _ := strings.Cut(id, ":"); u.covered[ns] {
		return absent, true
	}

	return "", false
}

func (s *Set) unitDigests(g *gather, files map[string]string) unitDigests {
	scratch := &deps.Tree{Units: map[string]deps.Unit{}, CannotTell: map[string]string{}, CannotTellBy: map[string]string{}}
	resolveDigests(g, scratch)

	u := unitDigests{digest: make(map[string]string, len(scratch.Units)), files: files, covered: map[string]bool{}}
	for id, unit := range scratch.Units {
		u.digest[id] = unit.Digest
	}

	spoiled := map[string]bool{}

	for _, l := range s.Plugins {
		if l.Desc.Kind != plugin.KindSBOM {
			continue
		}

		for _, ns := range l.Desc.Namespaces {
			if len(g.untold[l.Conf.Name]) > 0 {
				spoiled[ns] = true
			} else {
				u.covered[ns] = true
			}
		}
	}

	for ns := range spoiled {
		delete(u.covered, ns)
	}

	return u
}

func (s *Set) gatherPlugin(ctx context.Context, l *Loaded, root string, files map[string]string, g *gather) error {
	defer progress.Stage(ctx, "plugin %s", l.Conf.Name)()

	claims, untold, err := s.claims(ctx, l, root, files)
	if err != nil {
		return err
	}

	g.noteClaims(l, claims)
	s.warnNoClaims(l, claims)

	call := plugin.CallUnits

	switch {
	case l.Desc.Kind == plugin.KindSBOM:
	case l.can(plugin.CallLinks):
		call = plugin.CallLinks
	default:
		return nil // a bind correlator binds later
	}

	ans, untoldSets, err := s.sets(ctx, l, call, root, files, g.inputs, claims)
	if err != nil {
		return err
	}

	if untold = append(untold, untoldSets...); len(untold) > 0 {
		g.untold[l.Conf.Name] = untold
	}

	if g.unitsOf == nil {
		g.unitsOf = map[string][]string{}
	}

	for _, set := range append(ans.Sets, ans.Rest) {
		for _, u := range set.Units {
			g.units[u.ID] = u
			g.by[u.ID] = l.Conf.Name

			if u.File != "" {
				g.unitsOf[u.File] = append(g.unitsOf[u.File], u.ID)
			}

			if len(u.DigestFrom) > 0 {
				g.digestFrom[u.ID] = u.DigestFrom
				g.digestBy[u.ID] = l.Conf.Name
			}
		}

		g.links = append(g.links, set.Links...)
		g.noteTyped(l.Conf.Name, set.Links)
		g.noteBy(l, set.Links)
		g.noteSaid(l.Conf.Name, set.CannotTell, set.DigestFrom)
		g.inputUnits = append(g.inputUnits, set.InputUnits...)
		g.noteComplete(l, set.Complete)
	}

	g.noteSaid(l.Conf.Name, nil, ans.DigestFrom)

	for _, c := range claims {
		if c.ByUnits != "" {
			g.byUnits = append(g.byUnits, c)
		}
	}

	return nil
}

func (g *gather) noteSaid(name string, cannot []plugin.CannotTell, digests []plugin.DigestFrom) {
	for _, c := range cannot {
		g.cannot = append(g.cannot, saidCannot{c, name})
	}

	for _, df := range digests {
		g.correlatorDigests = append(g.correlatorDigests, saidDigest{df, name})
	}
}

func (g *gather) noteComplete(l *Loaded, files []string) {
	if l.Desc.Kind != plugin.KindSBOM {
		return
	}

	for _, f := range files {
		g.complete[f] = l.Conf.Name
	}
}

func (g *gather) noteBy(l *Loaded, links []plugin.Link) {
	name := l.Conf.Name

	if l.Desc.Kind == plugin.KindCorrelator {
		g.noteEnds(name, links)
	}

	if g.linkBy == nil {
		g.linkBy = map[[2]string]string{}
	}

	for _, ln := range links {
		if _, ok := g.linkBy[[2]string{ln.From, ln.To}]; !ok {
			g.linkBy[[2]string{ln.From, ln.To}] = name
		}
	}
}

func (g *gather) noteEnds(name string, links []plugin.Link) {
	if g.correlatorLinks == nil {
		g.correlatorLinks = map[string][][2]string{}
	}

	for _, ln := range links {
		g.correlatorLinks[name] = append(g.correlatorLinks[name], [2]string{ln.From, ln.To})
	}
}

func nearEnds(t *deps.Tree, links map[string][][2]string) map[string][]string {
	out := make(map[string][]string, len(links))

	for name, ends := range links {
		set := map[string]bool{}

		for _, e := range ends {
			if _, ok := t.Units[e[0]]; ok {
				set[e[0]] = true
			} else if _, ok := t.Units[e[1]]; ok {
				set[e[1]] = true
			}
		}

		out[name] = slices.Sorted(maps.Keys(set))
	}

	return out
}

func (s *Set) warnNoClaims(l *Loaded, claims []plugin.Claim) {
	if len(claims) == 0 && (l.can(plugin.CallDetect) || len(l.Desc.Candidates) > 0 || len(l.Conf.Include) > 0) {
		s.warnf("%s claims nothing in the tree: `pipelinegen plugins claims` shows what each plugin claims; check its include and exclude", l.Conf.Name)
	}
}

func (g *gather) noteClaims(l *Loaded, claims []plugin.Claim) {
	if l.Desc.Kind != plugin.KindCorrelator {
		return
	}

	name := l.Conf.Name

	if g.correlatorFiles == nil {
		g.correlatorFiles = map[string]map[string]bool{}
	}

	if g.correlatorFiles[name] == nil {
		g.correlatorFiles[name] = map[string]bool{}
	}

	for _, c := range claims {
		g.correlatorFiles[name][c.Path] = true
	}
}

func sortedSets(m map[string]map[string]bool) map[string][]string {
	out := make(map[string][]string, len(m))

	for name, set := range m {
		out[name] = slices.Sorted(maps.Keys(set))
	}

	return out
}

func (g *gather) noteTyped(name string, links []plugin.Link) {
	for _, ln := range links {
		if !ln.Typed() {
			continue
		}

		if g.typedBy == nil {
			g.typedBy = map[[2]string]string{}
		}

		g.typedBy[[2]string{ln.From, ln.To}] = name
	}
}

// untoldFiles gives each file a plugin couldn't tell about a unit file:<path> digesting its blob,
// so a change to it is seen at all.
func untoldFiles(t *deps.Tree, untold map[string][]string, files map[string]string) {
	t.Untold = map[string][]string{}

	for name, paths := range untold {
		paths = sortedCopy(paths)
		paths = slices.Compact(paths)
		t.Untold[name] = paths

		for _, p := range paths {
			id := deps.FileID(p)
			if _, ok := t.Units[id]; ok {
				continue
			}

			t.Units[id] = deps.Unit{ID: id, Group: p, Digest: files[p]}
			t.Providers[id] = name
		}
	}
}

func PinID(plugin string) string { return "plugin:" + plugin }

// pins gives each plugin a unit digesting its entry in the tree's config, so a changed entry
// selects every slice it binds; read as history, so a base with a retired key still names them.
func (s *Set) pins(root string, t *deps.Tree) {
	t.Pins = map[string]string{}

	own := map[string]config.Plugin{}

	if data, err := os.ReadFile(filepath.Join(root, config.FileName)); err == nil { //nolint:gosec // the tree's own config
		if cfg, err := config.ParseHistory(data); err == nil {
			for _, p := range cfg.Plugins {
				own[p.Name] = p
			}
		}
	}

	for _, l := range s.Plugins {
		id := PinID(l.Conf.Name)
		t.Pins[l.Conf.Name] = id

		digest := absent
		if p, ok := own[l.Conf.Name]; ok {
			digest = pinDigest(p)
		}

		t.Units[id] = deps.Unit{ID: id, Digest: digest}
	}
}

func pinDigest(p config.Plugin) string {
	h := sha256.New()

	if p.Release != nil {
		h.Write([]byte("release " + p.Release.Version + " " + p.Release.Pin() + "\n"))
	}

	h.Write([]byte("commit " + p.Commit + "\nkind " + p.Kind + "\n"))

	// a map marshals with its keys sorted, so equal blocks digest alike
	block, err := json.Marshal(p.Config)
	if err != nil {
		block = []byte(fmt.Sprint(p.Config))
	}

	h.Write(block)

	return hex.EncodeToString(h.Sum(nil))
}

// RealPath is a tree's root with every symlink resolved, as handed to plugins, so a plugin sees
// the same paths as the tool that wrote them.
func RealPath(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}

	return root
}

func (s *Set) on(narrowing string, level int) bool {
	off, ok := s.offAt[narrowing]

	return ok && level < off
}

// On tells whether a narrowing is in force at a level: one no plugin declares never is, so its
// wide rule applies.
func (s *Set) On(narrowing string, level int) bool { return s.on(narrowing, level) }

// OffAt is the level that switches a narrowing off, 0 for one nobody declares.
func (s *Set) OffAt(narrowing string) int { return s.offAt[narrowing] }

func (s *Set) InForce(level int) []string {
	out := []string{}

	for _, n := range sortedKeys(s.offAt) {
		if s.on(n, level) {
			out = append(out, n)
		}
	}

	return out
}

func (s *Set) inForce(links []plugin.Link, level int) ([]plugin.Link, map[string][]plugin.Link, error) {
	out := make([]plugin.Link, 0, len(links))
	ruled := map[string][]plugin.Link{}

	for _, ln := range links {
		if ln.Narrowing == "" {
			out = append(out, ln)

			continue
		}

		if _, ok := s.offAt[ln.Narrowing]; !ok {
			return nil, nil, fmt.Errorf("%w: a link from %s carries the narrowing %q, which no configured plugin declares", ErrPlugins, ln.From, ln.Narrowing)
		}

		on := s.on(ln.Narrowing, level)

		switch {
		case (ln.While == plugin.WhileOn) == on:
			out = append(out, ln)
		case on:
			ruled[ln.Narrowing] = append(ruled[ln.Narrowing], ln)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}

		return out[i].To < out[j].To
	})

	return out, ruled, nil
}

// mergeLinks sorts links and merges those alike in ends, kind and tags, so equal answers give
// equal lists whether the sets came from the cache or the plugin.
func mergeLinks(links []plugin.Link) []plugin.Link {
	same := func(a, b plugin.Link) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To), cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.Narrowing, b.Narrowing), cmp.Compare(a.While, b.While))
	}

	slices.SortFunc(links, same)

	out := links[:0]

	for _, ln := range links {
		if n := len(out); n > 0 && same(out[n-1], ln) == 0 {
			out[n-1].Ref = out[n-1].Ref || ln.Ref
			out[n-1].IfLinked = out[n-1].IfLinked && ln.IfLinked

			continue
		}

		out = append(out, ln)
	}

	return out
}

func dangling(t *deps.Tree, id string) bool {
	_, ok := t.Units[id]

	return !ok && !strings.HasPrefix(id, "file:") && !plugin.CoreOwned(id)
}

func splitTyped(links []plugin.Link) (plain, typed []plugin.Link) { //nolint:nonamedreturns // two lists of one type
	for _, ln := range links {
		if ln.Typed() {
			typed = append(typed, ln)
		} else {
			plain = append(plain, ln)
		}
	}

	return plain, typed
}

func resolveDigests(g *gather, t *deps.Tree) {
	done := map[string]string{}
	busy := map[string]bool{}

	var resolve func(id string) (string, bool)

	resolve = func(id string) (string, bool) {
		if d, ok := done[id]; ok {
			return d, d != ""
		}

		u, ok := g.units[id]
		if !ok || busy[id] {
			return "", false
		}

		from := g.digestFrom[id]
		if len(from) == 0 {
			done[id] = u.Digest

			return u.Digest, u.Digest != ""
		}

		busy[id] = true
		defer delete(busy, id)

		h := sha256.New()

		for _, src := range sortedCopy(from) {
			d, ok := resolve(src)
			if !ok {
				done[id] = ""
				cannotTell(t, id, g.digestBy[id], "takes its digest from "+src+", which is missing")

				return "", false
			}

			h.Write([]byte(src + "=" + d + "\n"))
		}

		done[id] = hex.EncodeToString(h.Sum(nil))

		return done[id], true
	}

	for id, u := range g.units {
		d, _ := resolve(id)

		group := u.Group
		if group == "" {
			group = u.File
		}

		t.Units[id] = deps.Unit{ID: id, Group: group, Digest: d, File: u.File, Entry: u.Entry, NotRoot: u.NotRoot, Label: u.Label}
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)

	return out
}

func (s *Set) apartOnEveryFilesystem(files map[string]string) map[string]string {
	groups := collidingPaths(slices.Collect(maps.Keys(files)))
	if len(groups) == 0 {
		return files
	}

	asked := maps.Clone(files)

	for _, group := range groups {
		s.warnf("%s are one file on a filesystem that folds case or normalises Unicode (a Mac's): no plugin is asked about them, "+
			"so a change to one is planned as a change to a file no plugin claims; rename all but one", strings.Join(group, " and "))

		for _, p := range group {
			delete(asked, p)
		}
	}

	return asked
}

func (s *Set) correlatorDigests(g *gather, level int) {
	for _, df := range g.correlatorDigests {
		if df.Narrowing == "" || s.on(df.Narrowing, level) {
			g.digestFrom[df.Unit] = df.From
			g.digestBy[df.Unit] = df.by
		}
	}
}
