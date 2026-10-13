package plugins

import (
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/plugin"
)

var cacheScales = map[bool]struct {
	files, sets, inputs int
	onDisk              int64
	allocAtMost         uint64
}{
	false: {400, 200, 70, 256 << 10, 64 << 20},
	true:  {4000, 2000, 700, 10 << 20, 512 << 20},
}

func TestManySetsSharingInputsCostLittle(t *testing.T) {
	scale := cacheScales[os.Getenv("PIPELINEGEN_DEPS_SCALE") != ""]
	files, sets, inputs := scale.files, scale.sets, scale.inputs

	r := rand.New(rand.NewPCG(7, 8)) //nolint:gosec // test data
	blobs := map[string]string{}
	paths := make([]string, 0, files)

	for i := range files {
		p := fmt.Sprintf("backend/internal/package%03d/file_with_a_long_name_%04d.go", i/20, i)
		paths = append(paths, p)
		blobs[p] = fmt.Sprintf("%040x", i)
	}

	var answer []plugin.Set

	claims := make([]plugin.Claim, 0, sets)

	for i := range sets {
		in := []string{paths[i]}
		for range inputs - 1 {
			in = append(in, paths[r.IntN(files)])
		}

		answer = append(answer, plugin.Set{Group: paths[i], Inputs: in, Links: []plugin.Link{{From: "go:" + paths[i], To: "go:x", Kind: plugin.LinkUses}}})
		claims = append(claims, plugin.Claim{Path: paths[i], Role: "go"})
	}

	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	store := &gitstore.Store{Remote: "file://" + remote, Dir: t.TempDir(), Retries: 3}
	dir := t.TempDir()
	l := &Loaded{Conf: config.Plugin{Name: "uses", Kind: config.KindCorrelator}, P: plugin.Plugin{Name: "uses"}}

	cold := &Set{o: Options{Answers: &plugincache.Cache{Dir: dir, Remote: &plugincache.Refs{Store: store}, Push: true}}}

	peak := peakHeap(t, func() {
		if err := cold.keep(t.Context(), l, plugin.CallLinks, blobs, unitDigests{}, answer, claimPaths(claims)); err != nil {
			t.Fatal(err)
		}
	})

	t.Logf("storing %d sets: the heap peaked at %d bytes", sets, peak)

	if peak > scale.allocAtMost {
		t.Fatalf("storing %d sets took the heap to %d bytes, more than %d", sets, peak, scale.allocAtMost)
	}

	if err := cold.o.Answers.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	local := dirSize(t, dir)
	pushed := refSize(t, remote)

	t.Logf("local %d bytes, pushed ref %d bytes", local, pushed)

	if local > scale.onDisk || pushed > scale.onDisk {
		t.Fatalf("the cache takes %d bytes here and %d in the ref, more than %d", local, pushed, scale.onDisk)
	}

	start := time.Now()
	warm := &Set{o: Options{Answers: &plugincache.Cache{Dir: dir}}}

	got, missing, _ := warm.cachedSets(t.Context(), l, plugin.CallLinks, blobs, unitDigests{}, claims)
	took := time.Since(start)

	t.Logf("warm: %d sets in %s", len(got.Sets), took)

	if len(got.Sets) != sets || len(missing) != 0 {
		t.Fatalf("warm: %d sets, %d missing", len(got.Sets), len(missing))
	}

	if took > 10*time.Second {
		t.Fatalf("a warm read of %d sets took %s", sets, took)
	}
}

func peakHeap(t *testing.T, f func()) uint64 {
	t.Helper()

	runtime.GC()

	var (
		peak uint64
		done = make(chan struct{})
		quit = make(chan struct{})
	)

	go func() {
		defer close(done)

		var m runtime.MemStats

		for {
			runtime.ReadMemStats(&m)
			peak = max(peak, m.HeapAlloc)

			select {
			case <-quit:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()

	f()
	close(quit)
	<-done

	return peak
}

func dirSize(t *testing.T, dir string) int64 {
	t.Helper()

	var n int64

	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("size of %s: %w", d.Name(), err)
		}

		n += info.Size()

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return n
}

func refSize(t *testing.T, remote string) int64 {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", "--git-dir", remote, "for-each-ref", "--format=%(refname)", plugincache.RefPrefix).Output()
	if err != nil {
		t.Fatal(err)
	}

	var n int64

	for ref := range strings.FieldsSeq(string(out)) {
		tree, err := exec.CommandContext(t.Context(), "git", "--git-dir", remote, "ls-tree", "-r", "-l", ref).Output()
		if err != nil {
			t.Fatal(err)
		}

		for line := range strings.SplitSeq(strings.TrimSpace(string(tree)), "\n") {
			if f := strings.Fields(line); len(f) >= 4 {
				size, _ := strconv.ParseInt(f[3], 10, 64) //nolint:errcheck // a size git didn't print counts nothing
				n += size
			}
		}
	}

	if n == 0 {
		t.Fatal("nothing was pushed")
	}

	return n
}
