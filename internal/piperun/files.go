package piperun

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
)

// What piperun keeps of a session's failure files: so much per item, and per session.
const (
	MaxItemFiles    = 2 << 20
	MaxSessionFiles = 50 << 20
)

// Files hands piperun the failure files of a session's items, within its caps.
type Files struct {
	// Feed is the session's feed directory; the files go under its files/.
	Feed string
	used int64
}

// Kept copies an item's failure files, each a path from the result directory, to files/<key>/<name>
// beside the feed, and returns their paths from files/. Over a cap the largest go first; a path that
// leaves the result directory or isn't a regular file is skipped. It says what it left out in left.
func (f *Files) Kept(result, prefix, key string, list []string) (kept, left []string, err error) {
	if len(list) == 0 {
		return nil, nil, nil
	}

	root, err := os.OpenRoot(result)
	if err != nil {
		return nil, list, fmt.Errorf("failure files: %w", err)
	}

	defer func() { _ = root.Close() }() //nolint:errcheck // only read

	type file struct {
		from, to string
		size     int64
	}

	var files []file

	for _, p := range list {
		name, ok := cutPrefix(p, prefix)
		info, serr := root.Lstat(filepath.FromSlash(p))

		if !ok || !filepath.IsLocal(filepath.FromSlash(p)) || serr != nil || !info.Mode().IsRegular() {
			left = append(left, p)

			continue
		}

		files = append(files, file{from: p, to: path.Join(key, name), size: info.Size()})
	}

	room := min(int64(MaxItemFiles), MaxSessionFiles-f.used)

	var total int64
	for _, fl := range files {
		total += fl.size
	}

	for total > room && len(files) > 0 {
		i := slices.IndexFunc(files, func(fl file) bool {
			return fl.size == slices.MaxFunc(files, func(a, b file) int { return cmp.Compare(a.size, b.size) }).size
		})
		total -= files[i].size
		left = append(left, files[i].from)
		files = slices.Delete(files, i, i+1)
	}

	for _, fl := range files {
		if err := copyIn(root, fl.from, filepath.Join(f.Feed, FilesDir, filepath.FromSlash(fl.to))); err != nil {
			return kept, append(left, fl.from), err
		}

		f.used += fl.size
		kept = append(kept, fl.to)
	}

	return kept, left, nil
}

// A failure file's name under the item: its path from the slice's own files directory.
func cutPrefix(p, prefix string) (string, bool) {
	rel, err := filepath.Rel(filepath.FromSlash(prefix), filepath.FromSlash(p))
	if err != nil || !filepath.IsLocal(rel) {
		return "", false
	}

	return filepath.ToSlash(rel), true
}

var errFile = errors.New("failure file")

func copyIn(root *os.Root, from, to string) error {
	in, err := root.Open(filepath.FromSlash(from))
	if err != nil {
		return fmt.Errorf("%w: %w", errFile, err)
	}

	defer func() { _ = in.Close() }() //nolint:errcheck // only read

	if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
		return fmt.Errorf("%w: %w", errFile, err)
	}

	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // under the session's own feed
	if err != nil {
		return fmt.Errorf("%w: %w", errFile, err)
	}

	_, cerr := io.Copy(out, in)

	if err := errors.Join(cerr, out.Close()); err != nil {
		return fmt.Errorf("%w %s: %w", errFile, from, err)
	}

	return nil
}
