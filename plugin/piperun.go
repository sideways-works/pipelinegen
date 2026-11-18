package plugin

import (
	"encoding/json"
	"fmt"
)

// PiperunPrepare is the suite's prepare. With PiperunSlices, PiperunWhole and PiperunEnd it names
// the templates a run's plan instantiates on piperun, one set per suite: the piperun runner names its
// templates with them and `plan --from --emit` its instances, so the two always agree. Each instance
// has its template's name, as a build plans once.
func PiperunPrepare(suite string) string { return suite + "-prepare" }

// PiperunSlices is the pool of the suite's selected slices, each item a slice's name.
func PiperunSlices(suite string) string { return suite + "-slices" }

// PiperunWhole is a whole-suite check, by the name the suite's whole_suite gives it.
func PiperunWhole(suite, check string) string { return suite + "-whole-" + check }

// PiperunEnd reports the suite's end, whatever its slices and checks did.
func PiperunEnd(suite string) string { return suite + "-end" }

// The most piperun takes in one emission inside a job's end; above them it refuses the emission.
const (
	EmitMaxBytes     = 48 << 10
	EmitMaxInstances = 500
	EmitMaxPoolItems = 2000
)

// EmitFile is the emission's name in the emitting step's emit directory ($PIPERUN_EMIT).
const EmitFile = "emit.json"

// Emission is piperun's emission document: the jobs and pools a step adds to its build.
type Emission struct {
	Jobs  []EmitJob  `json:"jobs"`
	Pools []EmitPool `json:"pools,omitempty"`
}

// EmitJob is one instance of a job template.
type EmitJob struct {
	Template string         `json:"template"`
	Name     string         `json:"name"`
	Params   map[string]any `json:"params"`
	After    []EmitAfter    `json:"after,omitempty"`
}

// EmitPool is a pool of items that sessions of its template take in chunks.
type EmitPool struct {
	Template string      `json:"template"`
	Name     string      `json:"name"`
	Items    []string    `json:"items"`
	After    []EmitAfter `json:"after,omitempty"`
}

// EmitAfter is one node an instance waits for: one that must pass, or with Ended one that need only
// end, whatever its outcome.
type EmitAfter struct {
	Job   string
	Ended bool
}

func (a EmitAfter) MarshalJSON() ([]byte, error) {
	var (
		data []byte
		err  error
	)

	if a.Ended {
		data, err = json.Marshal(struct {
			Job   string `json:"job"`
			Ended bool   `json:"ended"`
		}{a.Job, true})
	} else {
		data, err = json.Marshal(a.Job)
	}

	if err != nil {
		return nil, fmt.Errorf("after: %w", err)
	}

	return data, nil
}

func (a *EmitAfter) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &a.Job); err == nil {
		a.Ended = false

		return nil
	}

	var o struct {
		Job   string `json:"job"`
		Ended bool   `json:"ended"`
	}

	if err := json.Unmarshal(data, &o); err != nil {
		return fmt.Errorf("after: %w", err)
	}

	a.Job, a.Ended = o.Job, o.Ended

	return nil
}
