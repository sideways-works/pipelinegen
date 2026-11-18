package piperun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

var errList = errors.New("item list")

// ReadKeys reads a list of items piperun keeps for a session's bookkeeping ($PIPERUN_CONFIRMED,
// $PIPERUN_REQUEUED): one item key a line, or a JSON object with its "key"; blank lines are skipped.
// A list still growing (complete false) may end in a line being written, which is left out; a missing
// file lists nothing.
func ReadKeys(path string, complete bool) (map[string]bool, error) {
	out := map[string]bool{}

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

	lines := strings.Split(string(data), "\n")
	if !complete {
		lines = lines[:len(lines)-1]
	}

	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}

		if strings.HasPrefix(l, "{") {
			var o struct {
				Key string `json:"key"`
			}

			if err := json.Unmarshal([]byte(l), &o); err != nil || o.Key == "" {
				return nil, fmt.Errorf("%w %s: %q is neither a key nor {\"key\": ...}", errList, path, l)
			}

			l = o.Key
		}

		out[l] = true
	}

	return out, nil
}
