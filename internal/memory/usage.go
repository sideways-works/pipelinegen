package memory

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Usage is what the reading process's own cgroup has used, at one moment: in a CI task, the task's
// container.
type Usage struct {
	// CPU is the seconds of CPU the cgroup used since it began.
	CPU float64
	// Set is its working set now, in MB: its usage less the file cache the kernel reclaims first.
	Set int64
	// Peak is the most it used since it began, less the file cache it holds now: an approximation, as
	// the cache at the peak isn't known. 0 where the kernel keeps no peak.
	Peak int64
	// OOM counts its processes the OOM killer killed since it began.
	OOM int64
	// Free is what the machine, or a cgroup's limit above, leaves it now (Reading.Free).
	Free int64
	// Pressure is the machine's memory pressure: the share of the last 10 seconds some task waited for
	// memory (PSI "some avg10"), in percent; -1 where the kernel doesn't say.
	Pressure float64
}

// Measure reads the cgroup of the reading process under root, v2 or v1. A machine whose memory can't
// be read has no Usage.
func Measure(root string) (Usage, error) {
	r, err := Read(root)
	if err != nil {
		return Usage{}, err
	}

	u := Usage{Free: r.Free(), Pressure: Pressure(root)}
	cg := filepath.Join(root, "sys", "fs", "cgroup")

	if p, ok := unifiedPath(root); ok {
		dir := filepath.Join(cg, filepath.FromSlash(p))
		cpu := fieldsOf(filepath.Join(dir, "cpu.stat"), " ")
		u.CPU = float64(atoi(cpu["usage_usec"])) / 1e6
		cur, _ := number(filepath.Join(dir, "memory.current"))
		u.Set = workingSet(cur, dir, "inactive_file") / MB

		if peak, ok := number(filepath.Join(dir, "memory.peak")); ok {
			u.Peak = workingSet(peak, dir, "inactive_file") / MB
		}

		events := fieldsOf(filepath.Join(dir, "memory.events"), " ")
		u.OOM = atoi(events["oom_kill"])

		return u, nil
	}

	mem := filepath.Join(cg, "memory")
	ns, _ := number(filepath.Join(cg, "cpuacct", "cpuacct.usage"))
	u.CPU = float64(ns) / 1e9
	cur, _ := number(filepath.Join(mem, "memory.usage_in_bytes"))
	u.Set = workingSet(cur, mem, "total_inactive_file") / MB

	if peak, ok := number(filepath.Join(mem, "memory.max_usage_in_bytes")); ok {
		u.Peak = workingSet(peak, mem, "total_inactive_file") / MB
	}

	oom := fieldsOf(filepath.Join(mem, "memory.oom_control"), " ")
	u.OOM = atoi(oom["oom_kill"])

	return u, nil
}

// Pressure is the machine's PSI "some avg10" for memory, -1 where the kernel doesn't say.
func Pressure(root string) float64 {
	psi, err := fields(filepath.Join(root, "proc", "pressure", "memory"), " ")
	if err != nil {
		return -1
	}

	for f := range strings.FieldsSeq(psi["some"]) {
		if v, ok := strings.CutPrefix(f, "avg10="); ok {
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				return n
			}
		}
	}

	return -1
}

// atoi reads a number, 0 for what isn't one: a file the kernel doesn't keep counts nothing.
func atoi(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}

	return n
}

// fieldsOf is a file's fields, none for a file the kernel doesn't keep.
func fieldsOf(p, sep string) map[string]string {
	f, err := fields(p, sep)
	if err != nil {
		return map[string]string{}
	}

	return f
}

// Headroom is the share, in percent, a prediction adds to the peaks it is made from.
const Headroom = 10

// Predict is the peak to expect from recent ones: their 90th percentile by nearest rank, and Headroom
// more; 0 without any.
func Predict(peaks []int64) int64 {
	if len(peaks) == 0 {
		return 0
	}

	s := slices.Clone(peaks)
	slices.Sort(s)

	p90 := s[(len(s)*9+9)/10-1]

	return p90 + (p90*Headroom+99)/100
}
