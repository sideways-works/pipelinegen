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
