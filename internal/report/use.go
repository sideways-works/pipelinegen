package report

import (
	"fmt"
	"strings"
)

// Use is what a batch's cgroup measured at a line of its journal: the reading at a slice's start or a
// part's end, or, as a slice's Used, what the slice used across its parts.
type Use struct {
	// CPU is seconds of CPU: the cgroup's since it began in a reading, the slice's own in Used.
	CPU float64 `json:"cpu"`
	// Stall is the seconds some task of the batch waited for a core, as CPU is counted.
	Stall float64 `json:"stall,omitempty"`
	// Peak is the most memory the batch held while the slice ran, in MB.
	Peak int64 `json:"peak_mb"`
	// OOM counts processes the OOM killer killed: since the cgroup began in a reading, while the slice
	// ran in Used.
	OOM int64 `json:"oom,omitempty"`
	// Room is what the batch could have held, in MB: what it held and what was free, the least of its
	// parts' starts in Used.
	Room int64 `json:"room_mb,omitempty"`
	// Pressure is the machine's PSI "some avg10" for memory, -1 unknown.
	Pressure float64 `json:"psi"`
	// Wall is the seconds the slice's CPU was read over, in Used: its parts' time from start to end. A
	// driver's own measure of a slice's time can be far shorter, a bundle's compile left out, so the
	// cores it kept busy are CPU over Wall.
	Wall float64 `json:"wall,omitempty"`
}

// Meter reads the batch's cgroup; start marks a slice's start, from which a meter that samples takes
// its peak. Nil where nothing is measured.
type Meter func(start bool) *Use

// OOMTest names the failure of a slice the OOM killer struck: a crash, whatever its tests said.
const OOMTest = "(out of memory)"

// PressureKilled is the PSI "some avg10" above which a slice whose failure names a kill signal counts
// as one memory pressure killed.
const PressureKilled = 10

type useSum struct {
	From *Use `json:"from,omitempty"`
	Used Use  `json:"used"`
}

func (r *Reporter) meter(start bool) *Use {
	if r.Meter == nil {
		return nil
	}

	return r.Meter(start)
}

// closeUse adds a part, from its start's reading to now's, to what the slice used before.
func closeUse(sum *useSum, now *Use) *Use {
	if sum == nil || now == nil {
		return nil
	}

	used := sum.Used
	used.Pressure = now.Pressure

	if from := sum.From; from != nil {
		used.CPU += max(now.CPU-from.CPU, 0)
		used.Stall += max(now.Stall-from.Stall, 0)
		used.OOM += max(now.OOM-from.OOM, 0)

		if used.Room == 0 || from.Room > 0 && from.Room < used.Room {
			used.Room = from.Room
		}
	}

	if sum.From != nil {
		used.Peak = max(used.Peak, now.Peak)
	}

	return &used
}

func (s *sliceSum) addUse(event string, u *Use) {
	if u == nil {
		return
	}

	if s.Use == nil {
		s.Use = &useSum{}
	}

	if event == eventStart {
		if s.Use.From == nil {
			s.Use.From = u
		}

		return
	}

	if used := closeUse(s.Use, u); used != nil {
		s.Use.Used, s.Use.From = *used, nil
	}
}

// usedIn puts what the slice used in its state; one the OOM killer struck, or that names a kill signal
// under memory pressure, crashed, and its peak counts as at least its room, so the prediction rises.
func usedIn(sf *StateFile, used *Use) {
	if used == nil {
		return
	}

	sf.CPUSeconds, sf.CPUStall, sf.CPUWall, sf.PeakMB = used.CPU, used.Stall, used.Wall, used.Peak

	var why string

	switch {
	case used.OOM > 0:
		why = fmt.Sprintf("the OOM killer killed %d processes of the batch while the slice ran", used.OOM)
	case sf.State != Passed && used.Pressure >= PressureKilled && killed(sf.Failures):
		why = fmt.Sprintf("a process was killed while memory was under pressure (%.0f%% of the last 10 seconds waiting for it)", used.Pressure)
	default:
		return
	}

	sf.PeakMB = max(sf.PeakMB, used.Room)
	sf.State = Errored
	sf.Failures = append([]Failure{{Test: OOMTest, Tail: fmt.Sprintf("crashed: %s; the batch had room for %d MB", why, used.Room)}}, sf.Failures...)
}

func killed(failures []Failure) bool {
	for _, f := range failures {
		t := strings.ToLower(f.Tail)
		if strings.Contains(t, "sigkill") || strings.Contains(t, "signal: killed") || strings.Contains(t, "exit code 137") {
			return true
		}
	}

	return false
}
