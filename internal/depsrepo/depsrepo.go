// Package depsrepo writes the deps repository, .pipelinegen/deps, that
// bazel-diff hashes: a digest file per unit, and per slice a selector
// depending on its units' digest files, the groups it holds whole and its
// data labels. It knows no language: unit ids are opaque strings, encoded
// into file and target names that stay distinct for distinct ids.
package depsrepo

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/deps"
)

// Dir is where the repository is written, relative to a tree's root.
const Dir = ".pipelinegen/deps"

// Fixed names inside the repository. None of them can be an encoded name:
// encoded names never start with "~" and file names always carry a prefix.
const (
	groupsDir    = "g"
	slicesDir    = "slices"
	absentDir    = "absent"
	rootGroup    = "~root"
	allTarget    = "all"
	unitPrefix   = "u."
	sharePrefix  = "s."
	absentDigest = "absent"
)

// Writer writes the deps repository of one tree.
type Writer struct{}

var _ deps.Writer = Writer{}

// Write makes root's deps repository from the tree's units and the
// slices, replacing what was there, and returns a key of everything it
// wrote: equal for equal input, different for any difference.
func (Writer) Write(ctx context.Context, root string, tree *deps.Tree, ss []deps.Slice) (string, error) {
	key := Key(tree, ss)

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("deps: %w", err)
	}

	dir := filepath.Join(root, filepath.FromSlash(Dir))
	keyFile := dir + keySuffix

	// the same repository in place is left alone: a plan that changes
	// nothing rewrites nothing. Another writer may have replaced the
	// directory and left the key beside it, so the repository must be this
	// writer's too.
	if have, err := os.ReadFile(keyFile); err == nil && string(have) == key+"\n" { //nolint:gosec // the key this writer left
		if repo, err := os.ReadFile(filepath.Join(dir, "REPO.bazel")); err == nil && string(repo) == header { //nolint:gosec // as above
			return key, nil
		}
	}

	// the key goes first, so a write cut short leaves a key that no longer
	// matches and the next write starts over
	if err := os.Remove(keyFile); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("deps: %w", err)
	}

	if err := writeDir(dir, Files(tree, ss)); err != nil {
		return "", err
	}

	if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("deps: %w", err)
	}

	return key, nil
}

// keySuffix names the file beside the repository that holds its key; it
// sits outside the repository, so Bazel never sees it.
const keySuffix = ".key"

// ReadKey reads the key of the repository Write last left at root, so a
// planner can tell that it is still the one it hashed.
func ReadKey(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(Dir)) + keySuffix)
	if err != nil {
		return "", fmt.Errorf("deps: %w", err)
	}

	return strings.TrimSuffix(string(data), "\n"), nil
}

// Remove deletes the repository at root and its key, for a plan that
// writes none: bazel-diff hashes a repository it finds, so one an earlier
// plan left would be hashed as if it were the tree's.
func Remove(root string) error {
	dir := filepath.Join(root, filepath.FromSlash(Dir))

	// the key first, so a removal cut short never leaves a key that
	// vouches for what remains
	if err := os.Remove(dir + keySuffix); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deps: %w", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("deps: %w", err)
	}

	return nil
}

// format names the layout Files writes; a new layout gets a new name, so
// a repository written by an older writer is never taken for a new one.
const format = "depsrepo 1"

// Key is a short hash of everything Files writes from: the units' ids,
// groups and digests, and each slice's selector, units, groups, data and
// whether it depends on everything. Files is deterministic, so equal keys
// mean equal repositories, and the key costs a fraction of writing them.
func Key(tree *deps.Tree, ss []deps.Slice) string {
	h := sha256.New()
	w := bufio.NewWriterSize(h, 1<<16)

	field := func(tag byte, v string) {
		w.WriteByte(tag)                    //nolint:errcheck // a hash takes every write
		w.WriteString(strconv.Itoa(len(v))) //nolint:errcheck // as above
		w.WriteByte(' ')                    //nolint:errcheck // as above
		w.WriteString(v)                    //nolint:errcheck // as above
	}

	field('f', format)

	for _, id := range slices.Sorted(keys(tree.Units)) {
		u := tree.Units[id]
		field('u', id)
		field('g', u.Group)
		field('d', u.Digest)
	}

	for _, sl := range sortedSlices(ss) {
		field('s', Selector(sl))
		field('e', strconv.FormatBool(sl.Everything))

		for _, part := range []struct {
			tag  byte
			list []string
		}{{'i', sl.Units}, {'h', sl.Groups}, {'l', sl.Data}} {
			sorted := slices.Clone(part.list)
			slices.Sort(sorted)

			for _, v := range slices.Compact(sorted) {
				field(part.tag, v)
			}
		}
	}

	w.Flush() //nolint:errcheck // as above

	return hex.EncodeToString(h.Sum(nil))[:16]
}

func keys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// writeDir writes files into a fresh directory next to dir and swaps it
// in, so a failed write never leaves half a repository behind.
func writeDir(dir string, files map[string][]byte) error {
	tmp := dir + ".new"
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("deps: %w", err)
	}

	made := map[string]bool{}

	for _, p := range slices.Sorted(keys(files)) {
		full := filepath.Join(tmp, filepath.FromSlash(p))

		if d := filepath.Dir(full); !made[d] {
			if err := os.MkdirAll(d, 0o750); err != nil {
				return fmt.Errorf("deps: %w", err)
			}

			made[d] = true
		}

		if err := os.WriteFile(full, files[p], 0o600); err != nil {
			return fmt.Errorf("deps: %w", err)
		}
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("deps: %w", err)
	}

	if err := os.Rename(tmp, dir); err != nil {
		return fmt.Errorf("deps: %w", err)
	}

	return nil
}

// Files is the repository's content: path to bytes.
func Files(tree *deps.Tree, ss []deps.Slice) map[string][]byte {
	r := newRepo(tree)

	for _, s := range sortedSlices(ss) {
		r.slice(s)
	}

	return r.files()
}

func sortedSlices(ss []deps.Slice) []deps.Slice {
	out := slices.Clone(ss)
	sort.SliceStable(out, func(i, j int) bool {
		return Selector(out[i]) < Selector(out[j])
	})

	return out
}

// Selector is a slice's target name in the slices package:
// @pipelinegen_deps//slices:<selector>.
func Selector(s deps.Slice) string {
	if s.Width == "" || s.Width == "executed" {
		return s.Name
	}

	return s.Name + "." + s.Width
}

type group struct {
	pkg    string
	units  []string            // ids
	shares map[string][]string // selector name to its ids here
}

type repo struct {
	tree     *deps.Tree
	names    map[string]string // encoded names, by id
	groups   map[string]*group // by group name
	absent   map[string]bool   // ids slices list that the tree lacks
	selector bytes.Buffer
}

func newRepo(tree *deps.Tree) *repo {
	r := &repo{tree: tree, names: map[string]string{}, groups: map[string]*group{}, absent: map[string]bool{}}

	for _, id := range slices.Sorted(keys(tree.Units)) {
		g := r.group(tree.Units[id].Group)
		g.units = append(g.units, id)
	}

	return r
}

func (r *repo) group(name string) *group {
	if g, ok := r.groups[name]; ok {
		return g
	}

	g := &group{pkg: groupPackage(name), shares: map[string][]string{}}
	r.groups[name] = g

	return g
}

func (r *repo) name(id string) string {
	n, ok := r.names[id]
	if !ok {
		n = Encode(id)
		r.names[id] = n
	}

	return n
}

// shareMin is how many units of one group a slice must hold before they
// get a filegroup of their own there: fewer are named one by one, since a
// target per slice and group costs bazel-diff more than a few labels do.
const shareMin = 8

func groupPackage(name string) string {
	if name == "" {
		return groupsDir + "/" + rootGroup
	}

	return groupsDir + "/" + Encode(name)
}

func (r *repo) slice(s deps.Slice) {
	sel := Selector(s)

	var srcs []string

	if s.Everything {
		srcs = append(srcs, ":"+allTarget)
	}

	byGroup := map[string][]string{}

	for _, id := range s.Units {
		u, ok := r.tree.Units[id]
		if !ok {
			r.absent[id] = true
			srcs = append(srcs, "//"+absentDir+":"+unitPrefix+r.name(id))

			continue
		}

		byGroup[u.Group] = append(byGroup[u.Group], id)
	}

	for name, ids := range byGroup {
		g := r.group(name)

		if len(ids) < shareMin {
			for _, id := range ids {
				srcs = append(srcs, "//"+g.pkg+":"+unitPrefix+r.name(id))
			}

			continue
		}

		share := sharePrefix + r.name(sel)
		g.shares[share] = ids
		srcs = append(srcs, "//"+g.pkg+":"+share)
	}

	for _, name := range s.Groups {
		srcs = append(srcs, "//"+r.group(name).pkg+":"+allTarget)
	}

	srcs = append(srcs, mainRepo(s.Data)...)

	slices.Sort(srcs)
	filegroup(&r.selector, sel, slices.Compact(srcs))
}

// mainRepo writes labels of the repository under test so this repository
// can name them: //pkg:t as @@//pkg:t.
func mainRepo(labels []string) []string {
	out := make([]string, 0, len(labels))

	for _, l := range labels {
		if strings.HasPrefix(l, "//") {
			l = "@@" + l
		}

		out = append(out, l)
	}

	return out
}

const header = "# Written by pipelinegen: the digests of one tree's units and each slice's dependencies on them.\n\n"

func (r *repo) files() map[string][]byte {
	out := map[string][]byte{"REPO.bazel": []byte(header)}

	everything := make([]string, 0, len(r.groups))

	for _, name := range slices.Sorted(keys(r.groups)) {
		g := r.groups[name]
		everything = append(everything, "//"+g.pkg+":"+allTarget)

		var b bytes.Buffer

		b.WriteString(header)
		b.WriteString("package(default_visibility = [\"//visibility:public\"])\n\n")

		all := make([]string, 0, len(g.units))

		for _, id := range g.units {
			f := unitPrefix + r.name(id)
			all = append(all, f)
			out[g.pkg+"/"+f] = []byte(r.tree.Units[id].Digest + "\n")
		}

		// selectors name a few units of a group one by one
		b.WriteString("exports_files(")
		writeList(&b, all, "")
		b.WriteString(")\n\n")
		filegroup(&b, allTarget, all)

		for _, share := range slices.Sorted(keys(g.shares)) {
			ids := g.shares[share]

			srcs := make([]string, 0, len(ids))
			for _, id := range ids {
				srcs = append(srcs, unitPrefix+r.name(id))
			}

			slices.Sort(srcs)
			filegroup(&b, share, slices.Compact(srcs))
		}

		out[g.pkg+"/BUILD.bazel"] = b.Bytes()
	}

	r.absentFiles(out)

	var s bytes.Buffer

	s.WriteString(header)
	s.WriteString("package(default_visibility = [\"//visibility:public\"])\n\n")
	filegroup(&s, allTarget, everything)
	s.Write(r.selector.Bytes())
	out[slicesDir+"/BUILD.bazel"] = s.Bytes()

	return out
}

// absentFiles writes a file for each unit slices list that the tree
// lacks, so their selectors load and change when the unit appears.
func (r *repo) absentFiles(out map[string][]byte) {
	if len(r.absent) == 0 {
		return
	}

	var b bytes.Buffer

	b.WriteString(header)
	b.WriteString("package(default_visibility = [\"//visibility:public\"])\n\n")

	names := make([]string, 0, len(r.absent))

	for id := range r.absent {
		f := unitPrefix + r.name(id)
		names = append(names, f)
		out[absentDir+"/"+f] = []byte(absentDigest + "\n")
	}

	slices.Sort(names)
	b.WriteString("exports_files(")
	writeList(&b, names, "")
	b.WriteString(")\n")
	out[absentDir+"/BUILD.bazel"] = b.Bytes()
}

func filegroup(b *bytes.Buffer, name string, srcs []string) {
	b.WriteString("filegroup(\n    name = ")
	b.Write(strconv.AppendQuote(nil, name))
	b.WriteString(",\n    srcs = ")
	writeList(b, srcs, "    ")
	b.WriteString(",\n)\n\n")
}

func writeList(b *bytes.Buffer, items []string, indent string) {
	b.WriteString("[\n")

	buf := make([]byte, 0, 128)

	for _, s := range items {
		b.WriteString(indent)
		b.WriteString("    ")
		b.Write(strconv.AppendQuote(buf[:0], s))
		b.WriteString(",\n")
	}

	b.WriteString(indent)
	b.WriteByte(']')
}
