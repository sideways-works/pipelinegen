package forgeplugin

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/plugkit/fetch"
	"github.com/sideways-works/plugkit/pin"
)

var ErrChecksum = fetch.ErrChecksum

// ErrPin is a pin no binary can be checked against.
var ErrPin = fetch.ErrPin

// Program is the name of a pinned forge's program, which names its
// release files.
func Program(c config.ForgePin) string { return c.Program() }

// Fetch returns the pinned forge's binary: dir's copy (filled by a pipeline's bootstrap) when it matches,
// else cacheDir's, else one downloaded there and checked; a binary that doesn't match its pin never runs.
func Fetch(ctx context.Context, client *http.Client, dir, cacheDir string, c config.ForgePin) (string, error) {
	f := fetch.Fetcher{Client: client, Cache: cacheDir, Under: "forges"}
	p := pin.Pin{
		Name: c.Name, Repository: c.Repository, Path: c.Path, Version: c.Version, Program: Program(c),
		SHA256: pin.Checksums(c.SHA256), Sums: c.Sums, URL: c.URL,
	}

	if dir != "" {
		want, err := f.Accepts(ctx, p, config.Platform())
		if err != nil {
			return "", hint(c, err)
		}

		if path := filepath.Join(dir, c.Name); slices.Contains(want, fetch.FileSum(path)) {
			return path, nil
		}
	}

	got, err := f.Fetch(ctx, p, config.Platform())
	if err != nil {
		return "", hint(c, err)
	}

	return got.Path, nil
}

func hint(c config.ForgePin, err error) error {
	return fmt.Errorf("forge %s: %w", c.Name, err)
}
