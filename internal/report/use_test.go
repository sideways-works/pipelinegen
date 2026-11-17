package report

import (
	"strings"
	"testing"
)

func TestASlicesUseAddsItsPartsAndKeepsItsPeak(t *testing.T) {
	var ss sliceSum

	ss.addUse(eventStart, &Use{CPU: 10, Peak: 100, Room: 9000})
	ss.addUse(eventPartEnd, &Use{CPU: 13, Peak: 2500})
	ss.addUse(eventStart, &Use{CPU: 20, Peak: 100, Room: 7000})

	used := closeUse(ss.Use, &Use{CPU: 21, Peak: 1800, Pressure: 3})
	if used.CPU != 4 || used.Peak != 2500 || used.Room != 7000 || used.Pressure != 3 {
		t.Fatalf("%+v", used)
	}

	// a reading after the slice's part closed belongs to another slice
	ss.addUse(eventPartEnd, &Use{CPU: 21, Peak: 1800})

	if used := closeUse(ss.Use, &Use{CPU: 40, Peak: 9999}); used.CPU != 4 || used.Peak != 2500 {
		t.Fatalf("after its parts: %+v", used)
	}
}

func TestAKillUnderPressureCrashesASliceAndAnOrdinaryFailureDoesnt(t *testing.T) {
	killed := Failure{Test: "checkout", Tail: "worker process exited unexpectedly (signal: SIGKILL)"}

	sf := StateFile{State: Failed, Failures: []Failure{killed}}
	usedIn(&sf, &Use{CPU: 3, Peak: 2000, Room: 6000, Pressure: 35})

	if sf.State != Errored || sf.PeakMB != 6000 || sf.Failures[0].Test != OOMTest || !strings.Contains(sf.Failures[0].Tail, "under pressure (35%") {
		t.Fatalf("killed under pressure: %+v", sf)
	}

	for _, tc := range []struct {
		name string
		f    Failure
		psi  float64
	}{
		{"killed without pressure", killed, 2},
		{"an assertion under pressure", Failure{Test: "checkout", Tail: "expected 3, got 4"}, 35},
	} {
		sf := StateFile{State: Failed, Failures: []Failure{tc.f}}
		usedIn(&sf, &Use{CPU: 3, Peak: 2000, Room: 6000, Pressure: tc.psi})

		if sf.State != Failed || sf.PeakMB != 2000 || sf.CPUSeconds != 3 || len(sf.Failures) != 1 {
			t.Errorf("%s: %+v", tc.name, sf)
		}
	}
}
