package plugins

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/plugincache"
)

type onceRemote struct {
	mu      sync.Mutex
	answers map[string][]byte
	items   map[string][]string
	clashes []string
}

func (r *onceRemote) Answer(_ context.Context, b plugincache.Build, hash string) ([]byte, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, ok := r.answers[b.Plugin+"/"+hash]

	return data, ok, nil
}

func (r *onceRemote) Items(_ context.Context, b plugincache.Build, hash string) ([]string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	items, ok := r.items[b.Plugin+"/"+hash]

	return items, ok, nil
}

func (r *onceRemote) Save(_ context.Context, b plugincache.Build, answers map[string][]byte, edits map[string]plugincache.Edit) ([]plugincache.Conflict, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var conflicts []plugincache.Conflict

	for hash, data := range answers {
		k := b.Plugin + "/" + hash
		if have, ok := r.answers[k]; ok && string(have) != string(data) {
			r.clashes = append(r.clashes, fmt.Sprintf("%s: stored %s, got %s", k, have, data))
			conflicts = append(conflicts, plugincache.Conflict{Build: b, Hash: hash, Stored: have, Got: data})

			continue
		}

		r.answers[k] = data
	}

	for hash, e := range edits {
		k := b.Plugin + "/" + hash
		out := slices.DeleteFunc(slices.Clone(r.items[k]), func(it string) bool { return slices.Contains(e.Remove, it) })
		out = append(out, e.Add...)
		slices.Sort(out)
		r.items[k] = slices.Compact(out)
	}

	return conflicts, nil
}

func (r *onceRemote) Replaced(context.Context, plugincache.Build) (map[string]string, error) {
	return map[string]string{}, nil
}

func (r *onceRemote) Replace(_ context.Context, b plugincache.Build, answers map[string][]byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for hash, data := range answers {
		r.answers[b.Plugin+"/"+hash] = data
	}

	return nil
}

func TestEveryWriteOnceKeyNamesOneValue(t *testing.T) {
	for seed := range uint64(4) {
		rng := rand.New(rand.NewPCG(seed, 1)) //nolint:gosec // a test's sequence of changes

		r := &onceRemote{answers: map[string][]byte{}, items: map[string][]string{}}
		s := newSetup(t, "overlap")
		s.r.Write("texts/doc.txt", "shared\n")
		s.r.Commit("shared")

		withFake(t, s, "overlap", "FAKE_LOG='"+t.TempDir()+"/calls'", "FAKE_SHARED=texts/doc.txt")

		for step := range 5 {
			if step > 0 {
				f := []string{"texts/a.txt", "texts/b.txt", "texts/doc.txt"}[rng.IntN(3)]
				s.r.Write(f, fmt.Sprintf("%s %d\n", f, rng.IntN(1000)))
				s.r.Commit("change")
			}

			s.cache = &plugincache.Cache{Dir: t.TempDir(), Remote: r, Push: true}

			set := s.set(t)
			if _, err := set.Tree(t.Context(), s.r.Dir, "c", 3); err != nil {
				t.Fatal(err)
			}

			if err := set.Flush(t.Context()); err != nil && !errors.Is(err, plugincache.ErrConflict) {
				t.Fatal(err)
			}
		}

		for _, c := range r.clashes {
			t.Errorf("seed %d: %s", seed, c)
		}
	}
}

func TestAnOlderAnswerRecordStillReads(t *testing.T) {
	s := &Set{o: Options{Answers: &plugincache.Cache{Dir: t.TempDir()}}}
	l, call := memberPlugin, "links"

	paths := []string{"a.go", "b.go"}
	table := inputsID(paths)
	id := listID([]string{"b.go"}, nil)

	for _, rec := range []struct {
		k plugincache.Key
		v any
	}{
		{l.key(call+"-paths", table), paths},
		{l.key(call+"-inputs", id), listRecord{Paths: table, Gaps: []int{1}}},
		{l.key(call+"-answer", "old"), answerRecord{Lists: []string{id}}},
	} {
		if err := s.putJSON(rec.k, rec.v); err != nil {
			t.Fatal(err)
		}
	}

	idx, ok := s.indexOf(t.Context(), l, call, "old")
	if !ok || len(idx["b.go"]) != 1 || idx["b.go"][0].id != id {
		t.Errorf("older answer: %v %v", idx, ok)
	}
}
