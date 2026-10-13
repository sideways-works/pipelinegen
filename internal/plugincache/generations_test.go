package plugincache

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

type week struct{ now time.Time }

func (w *week) clock() time.Time { return w.now }

func newWeek() *week { return &week{now: time.Date(2026, 1, 7, 9, 0, 0, 0, time.UTC)} }

func memberOf(path string) Key {
	return Key{Plugin: "go", Version: "v1", SHA256: "aaaaaaaaaaaaaaaa", Call: "member", Content: []string{path}}
}

func planTree(t *testing.T, c *Cache, files int) int {
	t.Helper()

	ctx := t.Context()
	asked := 0

	for i := range files {
		k := key(fmt.Sprintf("blob%03d", i))

		_, err := c.Get(ctx, k)

		switch {
		case errors.Is(err, ErrMiss):
			asked++

			if err := c.Put(k, fmt.Appendf(nil, "units of file %d", i)); err != nil {
				t.Fatal(err)
			}
		case err != nil:
			t.Fatal(err)
		}

		m := memberOf(fmt.Sprintf("dir/f%03d.go", i))

		items, ok, err := c.Items(ctx, m)
		if err != nil {
			t.Fatal(err)
		}

		if !ok || !slices.Contains(items, "set1") {
			asked++

			if err := c.Edit(ctx, m, Edit{Add: []string{"set1"}}); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	return asked
}

func cacheRefs(t *testing.T, remote string) []string {
	t.Helper()

	refs, err := store(t, remote).List(t.Context(), RefPrefix)
	if err != nil {
		t.Fatal(err)
	}

	return slices.Sorted(maps.Keys(refs))
}

func TestAnUnchangedTreeIsNeverAskedAgainAcrossGenerations(t *testing.T) {
	const files = 40

	for _, warm := range []bool{false, true} {
		t.Run(fmt.Sprintf("warm=%v", warm), func(t *testing.T) {
			ctx := t.Context()
			remote := bare(t)
			clock := newWeek()
			dir := t.TempDir()

			plan := func() int {
				local := dir
				if !warm {
					local = t.TempDir()
				}

				c := &Cache{Dir: local, Remote: &Refs{Store: store(t, remote), Clock: clock.clock}, Push: true}

				return planTree(t, c, files)
			}

			if n := plan(); n != 2*files {
				t.Fatalf("the first plan computed %d answers, want %d", n, 2*files)
			}

			for boundary := range 3 {
				clock.now = clock.now.AddDate(0, 0, 7)

				if n := plan(); n != 0 {
					t.Fatalf("after boundary %d a plan computed %d answers", boundary+1, n)
				}

				if _, err := Drop(ctx, store(t, remote), clock.now); err != nil {
					t.Fatal(err)
				}
			}

			b := key("x").build()
			want := []string{GenRef(b, Generation(clock.now.AddDate(0, 0, -7))), GenRef(b, Generation(clock.now))}

			if got := cacheRefs(t, remote); !slices.Equal(got, want) {
				t.Errorf("refs kept %v, want %v", got, want)
			}

			c := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote), Clock: clock.clock}}
			if n := planTree(t, c, files); n != 0 {
				t.Errorf("a cold machine after three boundaries computed %d answers", n)
			}
		})
	}
}

func TestUnusedAnswersAndBuildsAreDropped(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)
	clock := newWeek()

	put := func(k Key) {
		t.Helper()

		c := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote), Clock: clock.clock}, Push: true}
		if err := c.Put(k, []byte("answer of "+k.Content[0])); err != nil {
			t.Fatal(err)
		}

		if err := c.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}

	old := key("old version of a file")
	retired := Key{Plugin: "go", Version: "v0", SHA256: "bbbbbbbbbbbbbbbb", Call: "units", Content: []string{"b"}}

	put(old)
	put(retired)

	for range 3 {
		clock.now = clock.now.AddDate(0, 0, 7)

		put(key(fmt.Sprintf("new %v", clock.now)))

		if _, err := Drop(ctx, store(t, remote), clock.now); err != nil {
			t.Fatal(err)
		}
	}

	cold := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote), Clock: clock.clock}}

	for _, k := range []Key{old, retired} {
		if _, err := cold.Get(ctx, k); !errors.Is(err, ErrMiss) {
			t.Errorf("%s %s: %v; want it dropped", k.Version, k.Content[0], err)
		}
	}

	for _, ref := range cacheRefs(t, remote) {
		if strings.Contains(ref, "/v0-") {
			t.Errorf("the retired build's ref %s stays", ref)
		}
	}
}

func TestLastWeekIsReadThrough(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)
	clock := newWeek()
	b := key("b1").build()
	m := hash(t, memberOf("a.go"))
	h := hash(t, key("b1"))

	first := &Refs{Store: store(t, remote), Clock: clock.clock}
	if _, err := first.Save(ctx, b, map[string][]byte{h: []byte("one")}, map[string]Edit{m: {Add: []string{"s1", "s2"}}}); err != nil {
		t.Fatal(err)
	}

	clock.now = clock.now.AddDate(0, 0, 7)

	next := &Refs{Store: store(t, remote), Clock: clock.clock}

	got, err := next.Save(ctx, b, map[string][]byte{h: []byte("two")}, map[string]Edit{m: {Add: []string{"s3"}, Remove: []string{"s1"}}})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || string(got[0].Stored) != "one" {
		t.Errorf("conflicts %+v; want last week's answer", got)
	}

	clock.now = clock.now.AddDate(0, 0, 7)

	if _, err := Drop(ctx, store(t, remote), clock.now); err != nil {
		t.Fatal(err)
	}

	r := &Refs{Store: store(t, remote), Clock: clock.clock}

	items, ok, err := r.Items(ctx, b, m)
	if err != nil || !ok || !slices.Equal(items, []string{"s2", "s3"}) {
		t.Errorf("items %v %v %v; want [s2 s3]", items, ok, err)
	}

	if data, ok, err := r.Answer(ctx, b, h); err != nil || !ok || string(data) != "one" {
		t.Errorf("the answer other machines read: %q %v %v", data, ok, err)
	}
}

func TestALocalHitTheSharedLevelLostGoesBack(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)
	clock := newWeek()
	dir := t.TempDir()

	c := &Cache{Dir: dir, Remote: &Refs{Store: store(t, remote), Clock: clock.clock}, Push: true}
	if err := c.Put(key("b1"), []byte("units")); err != nil {
		t.Fatal(err)
	}

	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	clock.now = clock.now.AddDate(0, 0, 21)
	if n, err := Drop(ctx, store(t, remote), clock.now); err != nil || n != 1 {
		t.Fatalf("dropped %d: %v", n, err)
	}

	back := &Cache{Dir: dir, Remote: &Refs{Store: store(t, remote), Clock: clock.clock}, Push: true}
	if got, err := back.Get(ctx, key("b1")); err != nil || string(got) != "units" {
		t.Fatalf("local: %q %v", got, err)
	}

	if err := back.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	cold := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote), Clock: clock.clock}}
	if got, err := cold.Get(ctx, key("b1")); err != nil || string(got) != "units" {
		t.Errorf("a cold machine reads %q %v", got, err)
	}
}

func TestDropRemovesTheLayoutWithoutGenerations(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)
	s := store(t, remote)

	if _, err := s.Write(ctx, "old", gitstore.Update{Ref: Ref(key("x").build()), Files: map[string][]byte{"aa/bb": []byte("x")}}); err != nil {
		t.Fatal(err)
	}

	if n, err := Drop(ctx, store(t, remote), time.Now()); err != nil || n != 1 {
		t.Errorf("dropped %d: %v", n, err)
	}

	if got := cacheRefs(t, remote); len(got) != 0 {
		t.Errorf("left %v", got)
	}
}

func TestGenerationsAreISOWeeksInOrder(t *testing.T) {
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "2026-W01"},
		{time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "2026-W53"},
		{time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC), "2027-W01"},
	} {
		got := Generation(tc.at)
		if _, gen, ok := cutGeneration("go/v1-abc." + got); got != tc.want || !ok || gen != got {
			t.Errorf("%v: %s, want %s", tc.at, got, tc.want)
		}
	}

	if Generation(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)) >= Generation(time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC)) {
		t.Error("generations don't sort across a year")
	}

	if _, _, ok := cutGeneration("go/v1.2.3-abc"); ok {
		t.Error("a version's dots are no generation")
	}
}
