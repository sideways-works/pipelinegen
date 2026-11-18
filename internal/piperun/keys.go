package piperun

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

var errList = errors.New("item list")

// Stopped is the outcome piperun confirms for an item of a build a person stopped.
const Stopped = "stopped"

// Listed is what a session's list says of one item: the outcome piperun confirmed, and the reasons of
// each time the item was requeued.
type Listed struct {
	Outcome string
	Reasons []string
}

// ReadList reads one of the lists piperun keeps for a session's bookkeeping, $PIPERUN_CONFIRMED
// ({"key", "outcome"}) or $PIPERUN_REQUEUED ({"key", "reason"}, a line each time): JSON lines, each
// complete only with its newline, so a last line still being written is left for the next read.
// Fields it doesn't know are ignored; a missing file lists nothing.
func ReadList(path string) (map[string]Listed, error) {
	out := map[string]Listed{}

	if path == "" {
		return out, nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // the list piperun hands the step
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}

	if err != nil {
		return nil, fmt.Errorf("%w: %w", errList, err)
	}

	for _, line := range completeLines(data) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var l struct {
			Key     string `json:"key"`
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
		}

		if err := json.Unmarshal(line, &l); err != nil || l.Key == "" {
			return nil, fmt.Errorf("%w %s: %q is no {\"key\": ...} line", errList, path, line)
		}

		e := out[l.Key]
		if l.Outcome != "" {
			e.Outcome = l.Outcome
		}

		if l.Reason != "" {
			e.Reasons = append(e.Reasons, l.Reason)
		}

		out[l.Key] = e
	}

	return out, nil
}
