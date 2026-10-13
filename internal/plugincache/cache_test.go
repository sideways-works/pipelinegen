package plugincache

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
)

func bare(t *testing.T) string {
	t.Helper()

	return storetest.Bare(t)
}

func store(t *testing.T, remote string) *gitstore.Store {
	t.Helper()

	return storetest.Open(t, remote)
}

func key(blob string) Key {
	return Key{Plugin: "go", Version: "v1", SHA256: "aaaaaaaaaaaaaaaa", Config: map[string]any{"m": 1}, Call: "units", Content: []string{blob}}
}

func hash(t *testing.T, k Key) string {
	t.Helper()

	h, err := k.Hash()
	if err != nil {
		t.Fatal(err)
	}

	return h
}

func TestKeyChangesWithEveryPart(t *testing.T) {
	base := hash(t, key("b1"))

	for _, k := range []Key{key("b2"), {Plugin: "go", Version: "v2", SHA256: "aaaaaaaaaaaaaaaa", Config: map[string]any{"m": 1}, Call: "units", Content: []string{"b1"}},
		{Plugin: "go", Version: "v1", SHA256: "aaaaaaaaaaaaaaaa", Config: map[string]any{"m": 2}, Call: "units", Content: []string{"b1"}}} {
		if hash(t, k) == base {
			t.Errorf("%+v hashes like the base", k)
		}
	}

	if hash(t, Key{Content: []string{"x", "y"}}) != hash(t, Key{Content: []string{"y", "x"}}) {
		t.Error("a set's order changes its key")
	}
}

func TestLocalThenRefsThenAnotherMachine(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	writer := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote)}, Push: true}

	if _, err := writer.Get(ctx, key("b1")); !errors.Is(err, ErrMiss) {
		t.Fatalf("empty cache: %v", err)
	}

	if err := writer.Put(key("b1"), []byte("units of b1")); err != nil {
		t.Fatal(err)
	}

	if got, err := writer.Get(ctx, key("b1")); err != nil || string(got) != "units of b1" {
		t.Fatalf("local: %q %v", got, err)
	}

	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	reader := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote)}}
	if got, err := reader.Get(ctx, key("b1")); err != nil || string(got) != "units of b1" {
		t.Fatalf("from the ref: %q %v", got, err)
	}

	if err := reader.Put(key("b9"), []byte("x")); err != nil {
		t.Fatal(err)
	}

	if err := reader.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	other := &Cache{Remote: &Refs{Store: store(t, remote)}}
	if _, err := other.Get(ctx, key("b9")); !errors.Is(err, ErrMiss) {
		t.Fatalf("a read-only cache pushed: %v", err)
	}
}

func TestTwoPlansBothLand(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	a := &Cache{Remote: &Refs{Store: store(t, remote)}, Push: true}
	b := &Cache{Remote: &Refs{Store: store(t, remote)}, Push: true}

	// both read the (empty) ref before either writes
	if _, err := a.Get(ctx, key("seed")); !errors.Is(err, ErrMiss) {
		t.Fatal(err)
	}

	if _, err := b.Get(ctx, key("seed")); !errors.Is(err, ErrMiss) {
		t.Fatal(err)
	}

	if err := a.Put(key("from-a"), []byte("a")); err != nil {
		t.Fatal(err)
	}

	if err := b.Put(key("from-b"), []byte("b")); err != nil {
		t.Fatal(err)
	}

	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	if err := b.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	check := &Cache{Remote: &Refs{Store: store(t, remote)}}
	for _, k := range []string{"from-a", "from-b"} {
		if _, err := check.Get(ctx, key(k)); err != nil {
			t.Errorf("%s lost: %v", k, err)
		}
	}
}

func items(t *testing.T, c *Cache, k Key) ([]string, bool) {
	t.Helper()

	got, ok, err := c.Items(t.Context(), k)
	if err != nil {
		t.Fatal(err)
	}

	return got, ok
}

func TestAnEditedRecordExistsEvenEmpty(t *testing.T) {
	remote := bare(t)
	c := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote)}, Push: true}

	if _, ok := items(t, c, key("m")); ok {
		t.Fatal("a record before any edit")
	}

	if err := c.Edit(t.Context(), key("m"), Edit{}); err != nil {
		t.Fatal(err)
	}

	if err := c.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	other := &Cache{Remote: &Refs{Store: store(t, remote)}}
	if got, ok := items(t, other, key("m")); !ok || len(got) != 0 {
		t.Fatalf("an empty record reads as %v, found %v", got, ok)
	}
}

func TestEditsOfTwoPlansBothStay(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	seed := &Cache{Remote: &Refs{Store: store(t, remote)}, Push: true}

	// both read the record before either writes; one replaces what it
	// read with its answer, the other only adds
	for _, firstA := range []bool{true, false} {
		a := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote)}, Push: true}
		b := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote)}, Push: true}

		k := key(fmt.Sprintf("m-%v", firstA))

		for _, c := range []*Cache{seed, a, b} {
			if c == seed {
				if err := c.Edit(ctx, k, Edit{Add: []string{"old"}}); err != nil {
					t.Fatal(err)
				}

				if err := c.Flush(ctx); err != nil {
					t.Fatal(err)
				}

				continue
			}

			if got, _ := items(t, c, k); !slices.Equal(got, []string{"old"}) {
				t.Fatalf("seeded record reads %v", got)
			}
		}

		if err := a.Edit(ctx, k, Edit{Add: []string{"from-a"}, Remove: []string{"old"}}); err != nil {
			t.Fatal(err)
		}

		if err := b.Edit(ctx, k, Edit{Add: []string{"from-b"}}); err != nil {
			t.Fatal(err)
		}

		order := []*Cache{a, b}
		if !firstA {
			order = []*Cache{b, a}
		}

		for _, c := range order {
			if err := c.Flush(ctx); err != nil {
				t.Fatal(err)
			}
		}

		check := &Cache{Remote: &Refs{Store: store(t, remote)}}
		if got, _ := items(t, check, k); !slices.Equal(got, []string{"from-a", "from-b"}) {
			t.Errorf("a first %v: the record reads %v, want both plans' answers and not the replaced one", firstA, got)
		}
	}
}

func TestEditsInOnePlanAddUp(t *testing.T) {
	e := Edit{Add: []string{"x"}, Remove: []string{"y"}}.then(Edit{Add: []string{"y"}, Remove: []string{"x"}})
	if got := e.apply([]string{"x", "y", "z"}); !slices.Equal(got, []string{"y", "z"}) {
		t.Fatalf("got %v", got)
	}
}

func TestADifferingAnswerIsAConflict(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	first := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote)}, Push: true}
	if err := first.Put(key("b1"), []byte("first")); err != nil {
		t.Fatal(err)
	}

	if err := first.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	again := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote)}, Push: true}

	for k, data := range map[string]string{"b1": "first", "b2": "other key"} {
		if err := again.Put(key(k), []byte(data)); err != nil {
			t.Fatal(err)
		}
	}

	if err := again.Flush(ctx); err != nil {
		t.Fatalf("same bytes: %v", err)
	}

	if err := again.Put(key("b1"), []byte("second")); err != nil {
		t.Fatal(err)
	}

	err := again.Flush(ctx)

	var ce *ConflictError
	if !errors.As(err, &ce) || !errors.Is(err, ErrConflict) || len(ce.Conflicts) != 1 ||
		string(ce.Conflicts[0].Stored) != "first" || string(ce.Conflicts[0].Got) != "second" || ce.Conflicts[0].Build.Plugin != "go" {
		t.Fatalf("got %v", err)
	}

	check := &Cache{Remote: &Refs{Store: store(t, remote)}}
	if got, ok, err := check.Stored(ctx, key("b1")); err != nil || !ok || string(got) != "first" {
		t.Fatalf("the stored answer was overwritten: %q %v %v", got, ok, err)
	}

	if got, _, err := check.Stored(ctx, key("b2")); err != nil || string(got) != "other key" {
		t.Fatalf("the rest of the write: %q", got)
	}

	if err := again.Replace(ctx, []Answer{{Key: key("b1"), Data: []byte("second")}}); err != nil {
		t.Fatal(err)
	}

	check = &Cache{Remote: &Refs{Store: store(t, remote)}}
	if got, _, err := check.Stored(ctx, key("b1")); err != nil || string(got) != "second" {
		t.Fatalf("after Replace: %q", got)
	}
}

func TestStoredItemsReadsTheSharedLevel(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	c := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote)}, Push: true}
	if err := c.Edit(ctx, key("m"), Edit{Add: []string{"a1"}}); err != nil {
		t.Fatal(err)
	}

	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	if err := c.Edit(ctx, key("m"), Edit{Add: []string{"a2"}}); err != nil {
		t.Fatal(err)
	}

	if items, _, err := c.Items(ctx, key("m")); err != nil || !slices.Equal(items, []string{"a1", "a2"}) {
		t.Fatalf("local: %v %v", items, err)
	}

	if items, ok, err := c.StoredItems(ctx, key("m")); err != nil || !ok || !slices.Equal(items, []string{"a1"}) {
		t.Fatalf("shared: %v %v %v", items, ok, err)
	}

	local := &Cache{Dir: c.Dir}
	if items, ok, err := local.StoredItems(ctx, key("m")); err != nil || ok || items != nil {
		t.Fatalf("no shared level: %v %v %v", items, ok, err)
	}
}

func TestALocalCopyOfAReplacedAnswerIsNotServed(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)

	old := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote)}, Push: true}
	for k, data := range map[string]string{"b1": "stale", "b2": "kept"} {
		if err := old.Put(key(k), []byte(data)); err != nil {
			t.Fatal(err)
		}
	}

	if err := old.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	rebuilt := &Cache{Dir: t.TempDir(), Remote: &Refs{Store: store(t, remote)}, Push: true}
	if err := rebuilt.Replace(ctx, []Answer{{Key: key("b1"), Data: []byte("fresh")}}); err != nil {
		t.Fatal(err)
	}

	again := &Cache{Dir: old.Dir, Remote: &Refs{Store: store(t, remote)}}
	if got, err := again.Get(ctx, key("b1")); err != nil || string(got) != "fresh" {
		t.Fatalf("replaced key: %q %v", got, err)
	}

	local := &Cache{Dir: old.Dir}
	if got, err := local.Get(ctx, key("b1")); err != nil || string(got) != "fresh" {
		t.Fatalf("local copy after the read: %q %v", got, err)
	}

	if got, err := again.Get(ctx, key("b2")); err != nil || string(got) != "kept" {
		t.Fatalf("a key nobody replaced: %q %v", got, err)
	}

	if err := rebuilt.Replace(ctx, []Answer{{Key: key("b2"), Data: []byte("kept again")}}); err != nil {
		t.Fatal(err)
	}

	got, err := (&Refs{Store: store(t, remote)}).Replaced(ctx, key("b1").build())
	if err != nil || got[hashOf(t, key("b1"))] != Digest([]byte("fresh")) || got[hashOf(t, key("b2"))] != Digest([]byte("kept again")) {
		t.Fatalf("record %v %v", got, err)
	}
}

func hashOf(t *testing.T, k Key) string {
	t.Helper()

	h, err := k.Hash()
	if err != nil {
		t.Fatal(err)
	}

	return h
}

func TestASaveThatChangesNothingWritesNothing(t *testing.T) {
	ctx := t.Context()
	remote := bare(t)
	b := key("b1").build()

	first := &Refs{Store: store(t, remote)}
	if _, err := first.Save(ctx, b, map[string][]byte{"aa11": []byte("one")}, map[string]Edit{"bb22": {Add: []string{"x", "y"}}}); err != nil {
		t.Fatal(err)
	}

	tip := func() string {
		t.Helper()

		out, err := exec.CommandContext(ctx, "git", "-C", strings.TrimPrefix(remote, "file://"), "for-each-ref", "--format=%(refname) %(objectname)").CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}

		return string(out)
	}

	before := tip()

	again := &Refs{Store: store(t, remote)}
	if _, err := again.Save(ctx, b, map[string][]byte{"aa11": []byte("one")}, map[string]Edit{"bb22": {Add: []string{"y"}, Remove: []string{"z"}}}); err != nil {
		t.Fatal(err)
	}

	if got := tip(); got != before {
		t.Fatalf("a save that changed nothing wrote:\nbefore\n%s\nafter\n%s", before, got)
	}

	if err := again.Replace(ctx, b, map[string][]byte{"aa11": []byte("one")}); err != nil {
		t.Fatal(err)
	}

	noted := tip()
	if noted == before {
		t.Fatal("a Replace noted nothing")
	}

	if err := again.Replace(ctx, b, map[string][]byte{"aa11": []byte("one")}); err != nil {
		t.Fatal(err)
	}

	if _, err := again.Save(ctx, b, nil, map[string]Edit{"bb22": {Add: []string{"x"}}}); err != nil {
		t.Fatal(err)
	}

	if got := tip(); got != noted {
		t.Fatalf("writes that changed nothing wrote:\nbefore\n%s\nafter\n%s", noted, got)
	}

	if _, err := again.Save(ctx, b, nil, map[string]Edit{"bb22": {Add: []string{"w"}}}); err != nil {
		t.Fatal(err)
	}

	if got := tip(); got == noted {
		t.Fatal("a real change wrote nothing")
	}
}

type sizes struct {
	answers map[string][]byte
	saves   []int
}

func (r *sizes) Answer(_ context.Context, _ Build, hash string) ([]byte, bool, error) {
	data, ok := r.answers[hash]

	return data, ok, nil
}

func (r *sizes) Items(context.Context, Build, string) ([]string, bool, error) {
	return nil, false, nil
}

func (r *sizes) Save(_ context.Context, _ Build, answers map[string][]byte, _ map[string]Edit) ([]Conflict, error) {
	n := 0

	for hash, data := range answers {
		r.answers[hash] = data
		n += len(data)
	}

	r.saves = append(r.saves, n)

	return nil, nil
}

func (r *sizes) Replace(context.Context, Build, map[string][]byte) error { return nil }

func (r *sizes) Replaced(context.Context, Build) (map[string]string, error) {
	return map[string]string{}, nil
}

func TestFreshAnswersPastTheBoundWaitOnDisk(t *testing.T) {
	remote := &sizes{answers: map[string][]byte{}}
	c := &Cache{Dir: t.TempDir(), Remote: remote, Push: true, HeldBytes: 1000}

	want := map[string]string{}

	for i := range 50 {
		k := key(fmt.Sprintf("blob-%d", i))
		data := fmt.Sprintf("%-100d", i)

		if err := c.Put(k, []byte(data)); err != nil {
			t.Fatal(err)
		}

		if held := c.Held(); held > 1000 {
			t.Fatalf("after %d answers the cache holds %d bytes", i+1, held)
		}

		want[hashOf(t, k)] = data
	}

	if err := c.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(remote.answers) != len(want) {
		t.Fatalf("%d of %d answers landed", len(remote.answers), len(want))
	}

	for hash, data := range want {
		if string(remote.answers[hash]) != data {
			t.Fatalf("%s: %q", hash, remote.answers[hash])
		}
	}

	if len(remote.saves) < 5 || slices.Max(remote.saves) > 1000 {
		t.Fatalf("writes of %v bytes", remote.saves)
	}

	if c.Held() != 0 {
		t.Fatalf("after Flush the cache holds %d bytes", c.Held())
	}
}
