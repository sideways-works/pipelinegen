package config

import (
	"strings"
	"testing"
)

func TestASuiteWaitsForMemoryByItsOwnSettingOrTheTopLevels(t *testing.T) {
	for _, tc := range []struct {
		name, text   string
		e2e, backend bool
	}{
		{"off by default", example, false, false},
		{"the top level's", example + "memory: {wait: true}\n", true, true},
		{"a suite's own over it", strings.Replace(example, "    run_slice: ci/tasks/backend-slice.yml\n",
			"    run_slice: ci/tasks/backend-slice.yml\n    memory: {wait: false}\n", 1) + "memory: {wait: true}\n", true, false},
		{"a suite's alone", strings.Replace(example, "    run_slice: ci/tasks/backend-slice.yml\n",
			"    run_slice: ci/tasks/backend-slice.yml\n    memory: {wait: true}\n", 1), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Parse([]byte(tc.text))
			if err != nil {
				t.Fatal(err)
			}

			if c.WaitsForMemory("e2e") != tc.e2e || c.WaitsForMemory("backend") != tc.backend {
				t.Fatalf("e2e %v, backend %v", c.WaitsForMemory("e2e"), c.WaitsForMemory("backend"))
			}
		})
	}
}

func TestASuiteAsksItsOwnBrokerOrTheTopLevels(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(example, "    run_slice: ci/tasks/backend-slice.yml\n",
		"    run_slice: ci/tasks/backend-slice.yml\n    memory: {broker: \"10.0.0.2:7480\"}\n", 1) + "memory: {broker: \"gateway:7480\"}\n"))
	if err != nil {
		t.Fatal(err)
	}

	if c.MemoryBroker("backend") != "10.0.0.2:7480" || c.MemoryBroker("e2e") != "gateway:7480" {
		t.Fatalf("backend %q, e2e %q", c.MemoryBroker("backend"), c.MemoryBroker("e2e"))
	}
}

func TestTheStepsMemoryHasDefaultsTheTopLevelReplaces(t *testing.T) {
	c, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}

	if c.StepMemoryMB(StepPlanning) != 4096 || c.StepMemoryMB(StepBookkeeping) != 1024 {
		t.Errorf("defaults: %d %d", c.StepMemoryMB(StepPlanning), c.StepMemoryMB(StepBookkeeping))
	}

	c, err = Parse([]byte(example + "memory: {wait: true, steps: {planning: 6000}}\n"))
	if err != nil {
		t.Fatal(err)
	}

	if c.StepMemoryMB(StepPlanning) != 6000 || c.StepMemoryMB(StepBookkeeping) != 1024 || !c.WaitsForMemory("e2e") {
		t.Errorf("set: %d %d", c.StepMemoryMB(StepPlanning), c.StepMemoryMB(StepBookkeeping))
	}

	if _, err := Parse([]byte(example + "memory: {steps: {bookkeeping: -1}}\n")); err == nil {
		t.Error("a negative size")
	}

	suite := strings.Replace(example, "    run_slice: ci/tasks/backend-slice.yml\n", "    run_slice: ci/tasks/backend-slice.yml\n    memory: {steps: {planning: 1}}\n", 1)
	if _, err := Parse([]byte(suite)); err == nil {
		t.Error("a suite's own memory.steps: the steps are the pipeline's, not a suite's")
	}
}

func TestASuiteWaitsForCPUByItsOwnSettingsOrTheTopLevels(t *testing.T) {
	backend := func(own string) string {
		return strings.Replace(example, "    run_slice: ci/tasks/backend-slice.yml\n", "    run_slice: ci/tasks/backend-slice.yml\n    cpu: "+own+"\n", 1)
	}

	for _, tc := range []struct {
		name, text              string
		e2e, backend            bool
		e2ePressure, backendPSI float64
		e2eCores, backendCores  float64
	}{
		{"off by default, at the defaults", example, false, false, 40, 40, 1, 1},
		{"the top level's", example + "cpu: {wait: true, pressure: 60, cores: 2}\n", true, true, 60, 60, 2, 2},
		{"a suite's own over it, key by key", backend("{wait: false, cores: 3.5}") + "cpu: {wait: true, pressure: 60}\n", true, false, 60, 60, 1, 3.5},
		{"a suite's alone", backend("{wait: true, pressure: 25}"), false, true, 40, 25, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Parse([]byte(tc.text))
			if err != nil {
				t.Fatal(err)
			}

			if c.WaitsForCPU("e2e") != tc.e2e || c.WaitsForCPU("backend") != tc.backend {
				t.Errorf("waits: e2e %v, backend %v", c.WaitsForCPU("e2e"), c.WaitsForCPU("backend"))
			}

			if c.CPUPressure("e2e") != tc.e2ePressure || c.CPUPressure("backend") != tc.backendPSI {
				t.Errorf("pressure: e2e %v, backend %v", c.CPUPressure("e2e"), c.CPUPressure("backend"))
			}

			if c.CPUCores("e2e") != tc.e2eCores || c.CPUCores("backend") != tc.backendCores {
				t.Errorf("cores: e2e %v, backend %v", c.CPUCores("e2e"), c.CPUCores("backend"))
			}
		})
	}
}

func TestCPUPressureIsAPercentageAndCoresArePositive(t *testing.T) {
	for _, tc := range []struct {
		text, want string
	}{
		{example + "cpu: {pressure: 0}\n", "cpu.pressure: a percentage above 0 and at most 100, not 0"},
		{example + "cpu: {pressure: 120}\n", "cpu.pressure: a percentage above 0 and at most 100, not 120"},
		{example + "cpu: {cores: 0}\n", "cpu.cores: more than 0 cores, not 0"},
		{strings.Replace(example, "    run_slice: ci/tasks/backend-slice.yml\n", "    run_slice: ci/tasks/backend-slice.yml\n    cpu: {cores: -1}\n", 1),
			"suites.backend.cpu.cores: more than 0 cores, not -1"},
	} {
		if _, err := Parse([]byte(tc.text)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v", tc.want, err)
		}
	}

	if _, err := Parse([]byte(example + "cpu: {wait: true, pressure: 100, cores: 0.5}\n")); err != nil {
		t.Errorf("the bounds themselves: %v", err)
	}
}
