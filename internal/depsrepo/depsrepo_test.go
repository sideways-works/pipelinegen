package depsrepo

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/deps"
)

func TestEncodeKeepsIdsApart(t *testing.T) {
	ids := []string{
		"go:a#func:Create", "go:a#func:create", "go:a/func:Create", "go:a#func Create",
		"go:a#method:(*T).Save", "go:a#method:(T).Save", "go:a%23func%3ACreate",
		"x^y", "X", "^x", ".hidden", "%2E", "a.b", "a%2Eb", "~root", "", "ü", "%C3%BC",
	}

	seen := map[string]string{}

	for _, id := range ids {
		e := Encode(id)
		if prev, ok := seen[e]; ok {
			t.Fatalf("%q and %q both encode to %q", prev, id, e)
		}

		seen[e] = id

		if strings.ContainsAny(e, "/:~ ") || strings.HasPrefix(e, ".") {
			t.Errorf("%q encodes to %q, not a safe path element", id, e)
		}
	}
}

func TestEncodeIsInjectiveOnRandomIds(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // test data
	alphabet := "aA:#/() .%^~_-zZ09ü"
	seen := map[string]string{}

	for range 200000 {
		n := 1 + r.IntN(8)

		var b strings.Builder
		for range n {
			b.WriteRune([]rune(alphabet)[r.IntN(len([]rune(alphabet)))])
		}

		id := b.String()

		e := Encode(id)
		if prev, ok := seen[e]; ok && prev != id {
			t.Fatalf("%q and %q both encode to %q", prev, id, e)
		}

		seen[e] = id
	}
}

func TestLongNamesStayShortAndApart(t *testing.T) {
	a := "go:" + strings.Repeat("very/long/path/", 30) + "#func:A"
	b := "go:" + strings.Repeat("very/long/path/", 30) + "#func:B"

	ea, eb := Encode(a), Encode(b)
	if ea == eb || len(ea) > keptPrefix+1+hashHex || !strings.Contains(ea, "~") {
		t.Fatalf("%q\n%q", ea, eb)
	}
}

func TestKeyIsStableAndChangesWithAnyDifference(t *testing.T) {
	k := Key(baseTree(), baseSlices())

	if again := Key(baseTree(), baseSlices()); again != k {
		t.Fatalf("same input, keys %s and %s", k, again)
	}

	reordered := baseSlices()
	reordered[0], reordered[1] = reordered[1], reordered[0]

	if got := Key(baseTree(), reordered); got != k {
		t.Fatal("the slices' order changed the key")
	}

	shuffled := baseSlices()
	slices.Reverse(shuffled[len(shuffled)-1].Units)

	if got := Key(baseTree(), shuffled); got != k {
		t.Fatal("the order of a slice's units changed the key")
	}

	variants := map[string]func() (*deps.Tree, []deps.Slice){
		"a digest": func() (*deps.Tree, []deps.Slice) { return withDigest(baseTree(), create, "x"), baseSlices() },
		"a unit's group": func() (*deps.Tree, []deps.Slice) {
			tr := baseTree()
			u := tr.Units[create]
			u.Group = "elsewhere"
			tr.Units[create] = u

			return tr, baseSlices()
		},
		"a slice's units": func() (*deps.Tree, []deps.Slice) {
			ss := baseSlices()
			ss[0].Units = ss[0].Units[:1]

			return baseTree(), ss
		},
		"a slice's data": func() (*deps.Tree, []deps.Slice) {
			ss := baseSlices()
			ss[0].Data = []string{"//:data.txt"}

			return baseTree(), ss
		},
		"everything": func() (*deps.Tree, []deps.Slice) {
			ss := baseSlices()
			ss[0].Everything = true

			return baseTree(), ss
		},
	}
	for name, v := range variants {
		if got := Key(v()); got == k {
			t.Errorf("%s changed, the key didn't", name)
		}
	}
}

func TestWriteReusesWhatIsInPlace(t *testing.T) {
	root := t.TempDir()

	k1, err := (Writer{}).Write(t.Context(), root, baseTree(), baseSlices())
	if err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(root, filepath.FromSlash(Dir), slicesDir, "BUILD.bazel")
	old := time.Unix(1, 0)

	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}

	k2, err := (Writer{}).Write(t.Context(), root, baseTree(), baseSlices())
	if err != nil || k2 != k1 {
		t.Fatalf("keys %s %s, %v", k1, k2, err)
	}

	if info, err := os.Stat(marker); err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("the same repository was written again: %v", err)
	}

	k3, err := (Writer{}).Write(t.Context(), root, withDigest(baseTree(), create, "x"), baseSlices())
	if err != nil || k3 == k1 {
		t.Fatalf("a change kept the key: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(Dir), groupPackage("internal/notes"), unitPrefix+Encode(create)))
	if err != nil || string(got) != "x\n" {
		t.Fatalf("digest file %q %v", got, err)
	}

	if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(Dir))); err != nil {
		t.Fatal(err)
	}

	if _, err := (Writer{}).Write(t.Context(), root, withDigest(baseTree(), create, "x"), baseSlices()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("not written again: %v", err)
	}
}

func TestWriteReplacesAnotherWritersRepository(t *testing.T) {
	root := t.TempDir()

	if _, err := (Writer{}).Write(t.Context(), root, baseTree(), baseSlices()); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(root, filepath.FromSlash(Dir))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, slicesDir), 0o750); err != nil {
		t.Fatal(err)
	}

	for p, body := range map[string]string{"REPO.bazel": "# another writer's\n", slicesDir + "/BUILD.bazel": "# not ours\n"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := (Writer{}).Write(t.Context(), root, baseTree(), baseSlices()); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, slicesDir, "BUILD.bazel"))
	if err != nil || !strings.HasPrefix(string(got), header) {
		t.Fatalf("the other writer's repository stayed: %q %v", got, err)
	}
}

func TestSelectorsAndDataLabels(t *testing.T) {
	files := Files(baseTree(), baseSlices())
	build := string(files[slicesDir+"/BUILD.bazel"])

	if !strings.Contains(build, `"@@//:data.txt",`) || !strings.Contains(build, `name = "internal/money/money_test.go.dependents",`) {
		t.Fatalf("slices package:\n%s", build)
	}

	if !strings.Contains(build, `"//absent:u.`+Encode("go:internal/notes#func:Gone")+`",`) {
		t.Fatalf("a listed unit the tree lacks has no absent file:\n%s", build)
	}

	if got := string(files[absentDir+"/"+unitPrefix+Encode("go:internal/notes#func:Gone")]); got != "absent\n" {
		t.Fatalf("absent file %q", got)
	}
}
