package connector

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/fetch"
	"github.com/sideways-works/pipelinegen/plugin"
)

// ErrChecksum is a connector's binary whose checksum isn't the pinned one.
var ErrChecksum = fetch.ErrChecksum

// ErrPin is a pin no binary can be checked against.
var ErrPin = fetch.ErrPin

// Program is the name of a pinned connector's program, which names its
// release files.
func Program(pin config.ConnectorPin) string { return plugin.Program(pin.Repository) }

// Fetch returns the path of the pinned connector's binary: dir's copy
// (the plugins directory a pipeline's bootstrap fills) when it matches the
// pin, cacheDir's when an earlier fetch put it there, and otherwise one
// downloaded into cacheDir and checked against the pin. A binary that
// doesn't match its pin never runs.
func Fetch(ctx context.Context, client *http.Client, dir, cacheDir string, pin config.ConnectorPin) (string, error) {
	f := fetch.Fetcher{Client: client, Cache: cacheDir, Under: "connectors"}
	p := fetch.Pin{Name: pin.Name, Repository: pin.Repository, Version: pin.Version, SHA256: pin.SHA256, Sums: pin.Sums, URL: pin.URL}

	if dir != "" {
		want, err := f.Accepts(ctx, p, config.Platform())
		if err != nil {
			return "", hint(pin, err)
		}

		if path := filepath.Join(dir, pin.Name); slices.Contains(want, fetch.FileSum(path)) {
			return path, nil
		}
	}

	got, err := f.Fetch(ctx, p, config.Platform())
	if err != nil {
		return "", hint(pin, err)
	}

	return got.Path, nil
}

func hint(pin config.ConnectorPin, err error) error {
	return fmt.Errorf("connector %s: %w", pin.Name, err)
}
