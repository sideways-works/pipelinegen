package memory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()

	for p, body := range files {
		p = filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const meminfo = "MemTotal:       16384000 kB\nMemFree:          512000 kB\nMemAvailable:    8192000 kB\n"

func TestTheMachineAloneWithoutACgroupLimit(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"proc/meminfo":                    meminfo,
		"proc/self/cgroup":                "0::/system.slice/worker.service\n",
		"sys/fs/cgroup/memory.stat":       "inactive_file 0\n",
		"sys/fs/cgroup/system.slice/x.md": "",
	})

	r, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}

	if r.Available != 8000 || r.Total != 16000 || r.Limit != 0 || r.Free() != 8000 {
		t.Fatalf("%+v free %d", r, r.Free())
	}

	if got := r.String(); got != "8000 MB free: 8000 MB available of 16000 MB, no cgroup limit" {
		t.Fatalf("%q", got)
	}
}

func TestTheTightestCgroupLimitWinsAndCachesDontCount(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"proc/meminfo":     meminfo,
		"proc/self/cgroup": "0::/garden/task\n",
		// the worker's limit: 6000 MB, 4000 MB in use of which 1000 MB is reclaimable cache
		"sys/fs/cgroup/garden/memory.max":     "6291456000\n",
		"sys/fs/cgroup/garden/memory.current": "4194304000\n",
		"sys/fs/cgroup/garden/memory.stat":    "anon 1\ninactive_file 1048576000\n",
		// the task's own: no limit
		"sys/fs/cgroup/garden/task/memory.max":     "max\n",
		"sys/fs/cgroup/garden/task/memory.current": "1048576000\n",
	})

	r, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}

	if r.Limit != 6000 || r.Used != 3000 || r.Cgroup != "/garden" || r.Free() != 3000 {
		t.Fatalf("%+v free %d", r, r.Free())
	}

	if got := r.String(); got != "3000 MB free: 8000 MB available of 16000 MB, the cgroup /garden limited to 6000 MB, using 3000 MB" {
		t.Fatalf("%q", got)
	}
}

func TestAContainersOwnCgroupAtTheMount(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"proc/meminfo":                 meminfo,
		"proc/self/cgroup":             "0::/\n",
		"sys/fs/cgroup/memory.max":     "20971520000\n",
		"sys/fs/cgroup/memory.current": "1048576000\n",
	})

	r, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}

	// a limit above what the machine has leaves the machine's figure
	if r.Limit != 20000 || r.Used != 1000 || r.Free() != 8000 {
		t.Fatalf("%+v free %d", r, r.Free())
	}
}

func TestCgroupV1(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"proc/meminfo":     meminfo,
		"proc/self/cgroup": "4:memory:/docker/abc\n",
		"sys/fs/cgroup/memory/memory.limit_in_bytes": "2097152000\n",
		"sys/fs/cgroup/memory/memory.usage_in_bytes": "1048576000\n",
		"sys/fs/cgroup/memory/memory.stat":           "cache 9\ntotal_inactive_file 524288000\n",
	})

	r, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}

	if r.Limit != 2000 || r.Used != 500 || r.Free() != 1500 {
		t.Fatalf("%+v free %d", r, r.Free())
	}

	write(t, root, map[string]string{"sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712\n"})

	if r, err = Read(root); err != nil || r.Limit != 0 || r.Free() != 8000 {
		t.Fatalf("no limit in v1: %+v %v", r, err)
	}
}

func TestNoMeminfoIsNoReading(t *testing.T) {
	if _, err := Read(t.TempDir()); !errors.Is(err, ErrNoReading) {
		t.Fatalf("%v", err)
	}
}

func TestMeasureACgroupsUse(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"proc/meminfo":                 meminfo,
		"proc/pressure/memory":         "some avg10=12.50 avg60=3.00 avg300=1.00 total=99\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		"proc/self/cgroup":             "0::/\n",
		"sys/fs/cgroup/cpu.stat":       "usage_usec 2500000\nuser_usec 2000000\n",
		"sys/fs/cgroup/memory.current": "3145728000\n",
		"sys/fs/cgroup/memory.peak":    "5242880000\n",
		"sys/fs/cgroup/memory.stat":    "inactive_file 1048576000\n",
		"sys/fs/cgroup/memory.events":  "low 0\nhigh 0\nmax 2\noom 1\noom_kill 1\n",
	})

	u, err := Measure(root)
	if err != nil {
		t.Fatal(err)
	}

	if u.CPU != 2.5 || u.Set != 2000 || u.Peak != 4000 || u.OOM != 1 || u.Free != 8000 || u.Pressure != 12.5 {
		t.Fatalf("%+v", u)
	}

	write(t, root, map[string]string{"proc/pressure/memory": ""})

	if p := Pressure(root); p != -1 {
		t.Fatalf("no pressure: %v", p)
	}
}

func TestPredictTakesTheNinetiethPercentileAndATenthMore(t *testing.T) {
	for _, tc := range []struct {
		peaks []int64
		want  int64
	}{
		{nil, 0},
		{[]int64{1000}, 1100},
		{[]int64{100, 200, 300, 400, 500, 600, 700, 800, 900, 5000}, 990},
		{[]int64{5000, 100, 200, 300, 400, 500, 600, 700, 800, 900, 1000}, 1100},
		{[]int64{3000, 1000, 2000}, 3300},
	} {
		if got := Predict(tc.peaks); got != tc.want {
			t.Errorf("%v: %d, want %d", tc.peaks, got, tc.want)
		}
	}
}

func TestAContainerNamingTheHostsPathIsMeasuredAtTheMount(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"proc/meminfo":                 meminfo,
		"proc/self/cgroup":             "0::/garden/0f3c\n",
		"sys/fs/cgroup/cpu.stat":       "usage_usec 1000000\n",
		"sys/fs/cgroup/memory.current": "1048576000\n",
	})

	u, err := Measure(root)
	if err != nil || u.CPU != 1 || u.Set != 1000 {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestAReaderOutsideACgroupReadsItsLimitByPath(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"proc/meminfo":     meminfo,
		"proc/self/cgroup": "0::/system.slice/broker.service\n",
		"sys/fs/cgroup/system.slice/docker-1.scope/memory.max":     "5242880000\n",
		"sys/fs/cgroup/system.slice/docker-1.scope/memory.current": "1048576000\n",
	})

	r, err := ReadAt(root, "/system.slice/docker-1.scope")
	if err != nil {
		t.Fatal(err)
	}

	if r.Limit != 5000 || r.Used != 1000 || r.Free() != 4000 || r.Cgroup != "/system.slice/docker-1.scope" {
		t.Fatalf("%+v free %d", r, r.Free())
	}

	// the reader's own cgroup has no limit: Read sees the machine alone
	if own, err := Read(root); err != nil || own.Limit != 0 {
		t.Fatalf("own: %+v %v", own, err)
	}
}
