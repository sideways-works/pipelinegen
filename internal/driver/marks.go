package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// A mark is exchanged through the marks directory: the marker writes <id>.json by rename and waits for
// <id>.done, or <id>.failed holding why; ids sort in the order the marks were made.
const (
	markRequest = ".json"
	markDone    = ".done"
	markFailed  = ".failed"
)

// ErrMark is a mark the batch didn't take.
var ErrMark = errors.New("mark")

type Mark struct {
	Mark  string `json:"mark"`
	Slice string `json:"slice"`
}

// MarkAndWait makes a mark and returns once the running batch has taken
// it, or failed it, or timeout passed.
func MarkAndWait(ctx context.Context, dir string, m Mark, timeout time.Duration) error {
	if m.Mark != plugin.MarkStart && m.Mark != plugin.MarkStop {
		return fmt.Errorf("%w: %q: a mark is %s or %s", ErrMark, m.Mark, plugin.MarkStart, plugin.MarkStop)
	}

	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMark, err)
	}

	id := fmt.Sprintf("%020d-%d", time.Now().UnixNano(), os.Getpid())
	tmp := filepath.Join(dir, "."+id+".tmp")

	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("%w: %w", ErrMark, err)
	}

	if err := os.Rename(tmp, filepath.Join(dir, id+markRequest)); err != nil {
		return fmt.Errorf("%w: %w", ErrMark, err)
	}

	deadline := time.Now().Add(timeout)

	for {
		if _, err := os.Stat(filepath.Join(dir, id+markDone)); err == nil {
			return nil
		}

		if why, err := os.ReadFile(filepath.Join(dir, id+markFailed)); err == nil { //nolint:gosec // the batch's own answer
			return fmt.Errorf("%w %s %s: %s", ErrMark, m.Mark, m.Slice, why)
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%w %s %s: the batch did not answer within %s; is `pipelinegen slice run` running with this marks directory?", ErrMark, m.Mark, m.Slice, timeout)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrMark, ctx.Err())
		case <-time.After(markPoll):
		}
	}
}

const markPoll = 20 * time.Millisecond

// serveMarks takes marks from dir until ctx ends, one at a time and in
// order, answering each with take's verdict.
func serveMarks(ctx context.Context, dir string, take func(Mark) error) {
	for {
		ids, err := pendingMarks(dir)
		if err == nil {
			for _, id := range ids {
				answerMark(dir, id, take)
			}
		}

		select {
		case <-ctx.Done():
			// marks made while the bundle ended are answered too, so no
			// hook waits out its timeout
			if ids, err := pendingMarks(dir); err == nil {
				for _, id := range ids {
					answerMark(dir, id, take)
				}
			}

			return
		case <-time.After(markPoll):
		}
	}
}

func pendingMarks(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("marks: %w", err)
	}

	answered := map[string]bool{}

	var ids []string

	for _, e := range ents {
		name := e.Name()

		switch {
		case strings.HasSuffix(name, markDone):
			answered[strings.TrimSuffix(name, markDone)] = true
		case strings.HasSuffix(name, markFailed):
			answered[strings.TrimSuffix(name, markFailed)] = true
		case strings.HasSuffix(name, markRequest) && !strings.HasPrefix(name, "."):
			ids = append(ids, strings.TrimSuffix(name, markRequest))
		}
	}

	ids = slices.DeleteFunc(ids, func(id string) bool { return answered[id] })
	slices.Sort(ids)

	return ids, nil
}

func answerMark(dir, id string, take func(Mark) error) {
	var m Mark

	data, err := os.ReadFile(filepath.Join(dir, id+markRequest)) //nolint:gosec // the batch's own marks directory
	if err == nil {
		err = json.Unmarshal(data, &m)
	}

	if err == nil {
		err = take(m)
	}

	name, body := id+markDone, []byte{}
	if err != nil {
		name, body = id+markFailed, []byte(err.Error())
	}

	tmp := filepath.Join(dir, "."+name+".tmp")
	if os.WriteFile(tmp, body, 0o600) == nil { //nolint:gosec // the batch's own marks directory; id names a request found there
		_ = os.Rename(tmp, filepath.Join(dir, name)) //nolint:errcheck // the one who marks times out and says so
	}
}
