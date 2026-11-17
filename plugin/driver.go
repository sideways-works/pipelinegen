package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
)

// KindDriver is a plugin that runs a suite's slices, so a suite needs no
// task script of its own. It is the one kind that runs the tree's code, and
// the core never calls one while planning.
const KindDriver = "driver"

// The verbs of a driver.
const (
	// CallRun runs the slices of a request and streams one event per
	// slice as each ends.
	CallRun = "run"
	// CallSetup starts what a batch's slices need (servers, a database)
	// and keeps it running until its stdin is closed.
	CallSetup = "setup"
	// CallPrepare builds once per run what every batch reuses, into the
	// prepared directory.
	CallPrepare = "prepare"
)

// DriverVerbs are a driver's verbs, in the order a suite's steps meet them.
var DriverVerbs = []string{CallPrepare, CallSetup, CallRun}

// The marks a driver makes at a bundle's slice boundaries, as the first
// argument to its Marks command.
const (
	MarkStart = "start"
	MarkStop  = "stop"
)

// EnvCommit is the commit a run runs, in every driver's environment: a test
// that depends on the commit reads it, and a test cache that keys on the
// variables a test read reruns it for every commit.
const EnvCommit = "PIPELINEGEN_COMMIT"

// EnvMarks names the marks directory in the environment of a recorded
// stack bundle's tests, for a hook inside them.
const EnvMarks = "PIPELINEGEN_MARKS"

// The events of a run's stream.
const (
	EventStart = "start"
	EventEnd   = "end"
)

// ResultsExit is a driver's Results when it can't read per-test events and
// judges a slice by its command's exit code alone.
const ResultsExit = "exit"

// The types a driver's settings take, as its describe names them.
const (
	SettingString   = "string"
	SettingBool     = "bool"
	SettingInt      = "int"
	SettingDuration = "duration"
	SettingList     = "list"
	SettingMap      = "map"
)

// DriverDescription is what a driver's describe adds; its fields sit at
// the top level of the answer.
type DriverDescription struct {
	Verbs []string `json:"verbs,omitempty"`
	// Routes are the slices the driver can take: file slices by path
	// pattern, target slices by rule kind. A suite's step may take no
	// slice outside them.
	Routes *Routes `json:"routes,omitempty"`
	// Apart says a bundle still leaves one recording per slice in a
	// recording run; without it a recording run calls the driver once per
	// slice.
	Apart bool `json:"apart,omitempty"`
	// RecordingForms name the forms the driver's recordings take, as the
	// test tool writes them (go-coverprofile, lcov). A correlator that
	// observes a suite says in ObservesForms which forms it reads.
	RecordingForms []string          `json:"recording_forms,omitempty"`
	Settings       map[string]string `json:"settings,omitempty"`
	Results        string            `json:"results,omitempty"`
	// Validates says the driver answers CallValidate, checking steps'
	// settings at every depth before anything runs; without it a driver is
	// checked against Settings alone.
	Validates bool `json:"validates,omitempty"`
}

// Routes are what a driver can take.
type Routes struct {
	Files []string `json:"files,omitempty"`
	Kinds []string `json:"kinds,omitempty"`
}

// DriverRequest is the request of every driver verb. Slices are for run;
// Prepared is the directory a prepare fills and later steps read.
type DriverRequest struct {
	Request

	Tree      string            `json:"tree"`
	Workspace string            `json:"workspace,omitempty"`
	Tools     map[string]string `json:"tools,omitempty"`
	Suite     string            `json:"suite"`
	Batch     bool              `json:"batch"`
	Record    bool              `json:"record"`
	// Stack says the suite's tests run against a stack its set-ups
	// started: the stack's code is no input of the test tool's cache, so
	// no slice of it is ever replayed from that cache.
	Stack    bool              `json:"stack,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Deadline string            `json:"deadline,omitempty"`
	Prepared string            `json:"prepared,omitempty"`
	// Marks, in a recording run of a stack suite, ask the driver to run
	// the slices one at a time and to mark each slice's start and stop
	// with Marks.Command, so the core collects the stack's coverage
	// between them.
	Marks  *Marks        `json:"marks,omitempty"`
	Slices []DriverSlice `json:"slices,omitempty"`
}

// Marks is how a driver marks a bundle's slice boundaries: Command, with
// MarkStart or MarkStop and the slice's name appended, returns once the
// core has collected; Dir is where it and a hook doing the exchange
// itself leave their requests.
type Marks struct {
	Command []string `json:"command"`
	Dir     string   `json:"dir"`
}

// DriverSlice is one slice a run request hands a driver: what to run
// (Label and Target for a target, Sources and Tests for a test file) and
// where its extras, failure files and recording go.
type DriverSlice struct {
	Name    string   `json:"name"`
	Label   string   `json:"label,omitempty"`
	Target  string   `json:"target,omitempty"`
	Kind    string   `json:"kind,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Tests   []string `json:"tests,omitempty"`
	Extras  string   `json:"extras,omitempty"`
	// Files is the directory a failed slice's files go into, those its end
	// names in Files; empty, the core keeps none.
	Files     string `json:"files,omitempty"`
	Recording string `json:"recording,omitempty"`
}

// DriverEvent is one line of a run's stream. An end carries the slice's
// result, as a batch's result file holds it.
type DriverEvent struct {
	Slice string `json:"slice"`
	Event string `json:"event"`

	Passed   bool      `json:"passed,omitempty"`
	Tests    *int      `json:"tests,omitempty"`
	Failures []Failure `json:"failures,omitempty"`
	Seconds  float64   `json:"seconds,omitempty"`
	Recorded bool      `json:"recorded,omitempty"`
	Replayed bool      `json:"replayed,omitempty"`
	// Files are what a failed slice left to show why (a screenshot, a
	// trace), by their paths in the slice's Files directory.
	Files []File `json:"files,omitempty"`
}

// File is one failure file: its path, relative and inside the directory
// it is named from, and a short label.
type File struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
}

// Failure is one failing test and the end of its output.
type Failure struct {
	Test string `json:"test"`
	Tail string `json:"tail"`
}

// SetupAnswer is the line a set-up writes once what it started is ready.
type SetupAnswer struct {
	Env map[string]string `json:"env"`
}

// DriverHandlers are a driver's verbs, for ServeDriver; a nil one is a
// verb the driver doesn't answer.
type DriverHandlers struct {
	Describe func() (Description, error)
	Run      func(ctx context.Context, req DriverRequest, emit func(DriverEvent) error) error
	Setup    func(ctx context.Context, req DriverRequest, ready func(env map[string]string) error, stop <-chan struct{}) error
	Prepare  func(ctx context.Context, req DriverRequest) error
	// Validate checks a step's settings for a verb as that verb would
	// read them, without the machine: no tool looked up, no file read.
	// A SettingError names the place; any other error is the whole
	// step's. With it, describe says Validates.
	Validate func(verb string, settings map[string]any) error
}

// ErrNoVerb is a verb a driver doesn't answer.
var ErrNoVerb = errors.New("no such verb")

// ServeDriver is a driver's main: it runs the verb named by args[0] and
// returns the exit code. It reads one JSON value from stdin, never to its
// end: a set-up's stdin stays open, and its closing is the stop.
func ServeDriver(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, h DriverHandlers) int {
	if len(args) == 0 {
		say(stderr, "a driver needs the name of a verb")

		return 1
	}

	if args[0] == CallDescribe {
		if h.Describe == nil {
			say(stderr, "no describe")

			return 1
		}

		d, err := h.Describe()
		if err == nil {
			d.Validates = h.Validate != nil
			err = json.NewEncoder(stdout).Encode(d)
		}

		return exitFor(stderr, err)
	}

	if args[0] == CallValidate && h.Validate != nil {
		return exitFor(stderr, serveValidate(stdin, stdout, h.Validate))
	}

	known := map[string]bool{CallRun: h.Run != nil, CallSetup: h.Setup != nil, CallPrepare: h.Prepare != nil}
	if !known[args[0]] {
		say(stderr, fmt.Sprintf("%v: %q", ErrNoVerb, args[0]))

		return 1
	}

	in := bufio.NewReader(stdin)

	var req DriverRequest
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		say(stderr, "request: "+err.Error())

		return 1
	}

	switch args[0] {
	case CallRun:
		return exitFor(stderr, h.Run(ctx, req, lineWriter[DriverEvent](stdout)))
	case CallPrepare:
		return exitFor(stderr, h.Prepare(ctx, req))
	default:
		stop := make(chan struct{})

		// the rest of stdin is nothing but its end: the stop
		go func() {
			_, _ = io.Copy(io.Discard, in) //nolint:errcheck // any end of stdin is the stop

			close(stop)
		}()

		ready := func(env map[string]string) error {
			if env == nil {
				env = map[string]string{}
			}

			return lineWriter[SetupAnswer](stdout)(SetupAnswer{Env: env})
		}

		return exitFor(stderr, h.Setup(ctx, req, ready, stop))
	}
}

func serveValidate(stdin io.Reader, stdout io.Writer, validate func(string, map[string]any) error) error {
	var req ValidateRequest
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		return fmt.Errorf("request: %w", err)
	}

	answer := ValidateAnswer{Steps: make([]ValidateResult, 0, len(req.Steps))}

	for _, st := range req.Steps {
		if !slices.Contains(DriverVerbs, st.Verb) {
			answer.Steps = append(answer.Steps, ValidateResult{Problems: []SettingError{{Problem: fmt.Sprintf("%q is no verb of a step", st.Verb)}}})

			continue
		}

		answer.Steps = append(answer.Steps, ValidateResult{Problems: SettingProblems(validate(st.Verb, st.Settings))})
	}

	if err := json.NewEncoder(stdout).Encode(answer); err != nil {
		return fmt.Errorf("answer: %w", err)
	}

	return nil
}

// lineWriter writes one JSON value per line, each written whole and at
// once, so a reader sees what ended even if the driver dies next.
func lineWriter[T any](w io.Writer) func(T) error {
	var mu sync.Mutex

	return func(v T) error {
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("event: %w", err)
		}

		mu.Lock()
		defer mu.Unlock()

		if _, err := w.Write(append(data, '\n')); err != nil {
			return fmt.Errorf("event: %w", err)
		}

		return nil
	}
}

func exitFor(stderr io.Writer, err error) int {
	if err != nil {
		say(stderr, err.Error())

		return 1
	}

	return 0
}

// HasVerb tells whether a driver's describe lists a verb.
func (d DriverDescription) HasVerb(verb string) bool { return slices.Contains(d.Verbs, verb) }
