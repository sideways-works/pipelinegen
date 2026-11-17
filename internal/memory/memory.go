// Package memory reads how much memory a task may still use: the machine's
// available memory and, when the task's cgroup or one above it has a limit,
// what that limit leaves.
package memory

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// MB is the unit of every figure here: a mebibyte.
const MB = 1 << 20

// ErrNoReading is a machine whose memory can't be read: no /proc/meminfo.
var ErrNoReading = errors.New("memory: no reading")

// Reading is what a task may still use, in MB.
type Reading struct {
	// Available is MemAvailable: free memory plus the caches the kernel would reclaim.
	Available int64
	Total     int64
	// Limit is the tightest limit among the task's cgroup and those above it, 0 for none; Used is that
	// cgroup's working set (its usage less the file cache it would reclaim first), Cgroup its path.
	Limit, Used int64
	Cgroup      string
}

// Free is what the task may still use: the machine's available memory, or less when a cgroup's limit
// leaves less.
func (r Reading) Free() int64 {
	if r.Limit > 0 {
		return min(r.Available, max(r.Limit-r.Used, 0))
	}

	return r.Available
}

func (r Reading) String() string {
	s := fmt.Sprintf("%d MB free: %d MB available of %d MB", r.Free(), r.Available, r.Total)
	if r.Limit > 0 {
		return s + fmt.Sprintf(", the cgroup %s limited to %d MB, using %d MB", r.Cgroup, r.Limit, r.Used)
	}

	return s + ", no cgroup limit"
}

// Read reads the machine under root ("/" but in tests): root/proc/meminfo, and the cgroup of the
// reading process, v2 or v1, under root/sys/fs/cgroup.
func Read(root string) (Reading, error) {
	info, err := fields(filepath.Join(root, "proc", "meminfo"), ":")
	if err != nil {
		return Reading{}, fmt.Errorf("%w: %w", ErrNoReading, err)
	}

	avail, ok := info["MemAvailable"]
	if !ok {
		return Reading{}, fmt.Errorf("%w: /proc/meminfo has no MemAvailable", ErrNoReading)
	}

	r := Reading{Available: kb(avail), Total: kb(info["MemTotal"])}

	cg := filepath.Join(root, "sys", "fs", "cgroup")
	if p, ok := unifiedPath(root); ok {
		r.Limit, r.Used, r.Cgroup = tightest(cg, p)
	} else {
		r.Limit, r.Used, r.Cgroup = legacy(filepath.Join(cg, "memory"))
	}

	return r, nil
}

// unifiedPath is the process's cgroup v2 path ("0::/a/b"), "/" inside a container's own namespace.
func unifiedPath(root string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(root, "proc", "self", "cgroup")) //nolint:gosec // the machine's own file
	if err != nil {
		return "", false
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			return path.Clean("/" + p), true
		}
	}

	return "", false
}

// tightest walks from the cgroup up to the hierarchy's root and keeps the limit that leaves least.
func tightest(mount, p string) (limit, used int64, at string) {
	left := int64(-1)

	for {
		dir := filepath.Join(mount, filepath.FromSlash(p))

		if l, ok := number(filepath.Join(dir, "memory.max")); ok {
			cur, _ := number(filepath.Join(dir, "memory.current"))
			u := workingSet(cur, dir, "inactive_file")

			if left < 0 || l-u < left {
				left, limit, used, at = l-u, l/MB, u/MB, p
			}
		}

		if p == "/" {
			return limit, used, at
		}

		p = path.Dir(p)
	}
}

// legacy reads a cgroup v1 memory controller at its mount: inside a container, the container's own.
func legacy(dir string) (limit, used int64, at string) {
	l, ok := number(filepath.Join(dir, "memory.limit_in_bytes"))
	// v1 says "no limit" with a number near the largest int64
	if !ok || l >= 1<<60 {
		return 0, 0, ""
	}

	cur, _ := number(filepath.Join(dir, "memory.usage_in_bytes"))

	return l / MB, workingSet(cur, dir, "total_inactive_file") / MB, "/ (v1)"
}

// workingSet is a cgroup's usage less its inactive file cache, which the kernel reclaims before it kills.
func workingSet(usage int64, dir, key string) int64 {
	stat, err := fields(filepath.Join(dir, "memory.stat"), " ")
	if err != nil {
		return usage
	}

	n, err := strconv.ParseInt(stat[key], 10, 64)
	if err != nil {
		return usage
	}

	return max(usage-n, 0)
}

// number reads a file holding one number; "max" and a missing file are no number.
func number(p string) (int64, bool) {
	data, err := os.ReadFile(p) //nolint:gosec // the machine's own file
	if err != nil {
		return 0, false
	}

	n, err := strconv.ParseInt(string(bytes.TrimSpace(data)), 10, 64)

	return n, err == nil
}

func fields(p, sep string) (map[string]string, error) {
	f, err := os.Open(p) //nolint:gosec // the machine's own file
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller says what it read
	}
	defer f.Close() //nolint:errcheck // read only

	out := map[string]string{}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), sep)
		if ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	return out, sc.Err() //nolint:wrapcheck // as above
}

// kb reads "16384 kB" as MB.
func kb(v string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(v, "kB")), 10, 64)

	return n / 1024
}
