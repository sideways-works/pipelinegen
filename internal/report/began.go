package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// A suite build's start record is written once the plan selected something of the suite, before its
// first batch, so whatever asks whether a suite began sees the build from its start.
const (
	beganDir  = "_began/"
	beganFile = "began.json"
)

func (id ID) BeganRef(suite string) string { return id.Prefix() + beganDir + suite }

type Began struct {
	Suite  string    `json:"suite"`
	At     time.Time `json:"at"`
	JobURL string    `json:"job_url,omitempty"`
}

// RecordBegan writes the start record of a build of suite in the run of
// id, over the one before.
func RecordBegan(ctx context.Context, store *gitstore.Store, id ID, suite, jobURL string) error {
	if err := errors.Join(id.Check(), checkName("suite", suite)); err != nil {
		return err
	}

	data, err := json.Marshal(Began{Suite: suite, At: time.Now().UTC(), JobURL: jobURL})
	if err != nil {
		return fmt.Errorf("start record: %w", err)
	}

	_, err = store.Write(ctx, id.Prefix()+" "+suite+" began",
		gitstore.Update{Ref: id.BeganRef(suite), Any: true, Files: map[string][]byte{beganFile: append(data, '\n')}})

	return err
}
