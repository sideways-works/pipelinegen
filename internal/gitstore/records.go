package gitstore

import (
	"context"
	"maps"
	"sync"
)

// Records is a store kept outside git, a store plugin's: records by name, each version an opaque
// token whose files never change, which a read finds without the record's name.
type Records interface {
	// List is every record under any of the prefixes, by name.
	List(ctx context.Context, prefixes []string) (map[string]Ref, error)
	// First is the first of the names that exists.
	First(ctx context.Context, names []string) (Ref, bool, error)
	// Read is each version's files, those at paths only when paths is set; a version the store
	// lacks is left out.
	Read(ctx context.Context, versions, paths []string) (map[string]map[string][]byte, error)
	// Write applies the updates in order, stopping at the first whose record no longer holds what
	// it expects (ErrStale): those before it stand. It returns each written record's new version.
	Write(ctx context.Context, message string, updates []Update) (map[string]string, error)
	Close() error
}

// recorded keeps the versions a store of Records has read one at a time: a version's files never
// change.
type recorded struct {
	mu    sync.Mutex
	files map[string]map[string][]byte
}

func (s *Store) onRecords() bool { return s.Records != nil }

// versions are the files of each version, read once per store.
func (s *Store) versions(ctx context.Context, versions []string) (map[string]map[string][]byte, error) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()

	if s.r.files == nil {
		s.r.files = map[string]map[string][]byte{}
	}

	var missing []string

	asked := map[string]bool{}

	for _, v := range versions {
		if _, ok := s.r.files[v]; !ok && !asked[v] {
			asked[v] = true
			missing = append(missing, v)
		}
	}

	if len(missing) > 0 {
		got, err := s.Records.Read(ctx, missing, nil)
		if err != nil {
			return nil, err
		}

		maps.Copy(s.r.files, got)
	}

	out := make(map[string]map[string][]byte, len(versions))

	for _, v := range versions {
		if f, ok := s.r.files[v]; ok {
			out[v] = f
		}
	}

	return out, nil
}

// recordFiles are one version's files that keep says to; a version the store lacks has none.
func (s *Store) recordFiles(ctx context.Context, version string, keep func(path string) bool) (map[string][]byte, error) {
	got, err := s.versions(ctx, []string{version})
	if err != nil {
		return nil, err
	}

	out := map[string][]byte{}

	for p, data := range got[version] {
		if keep(p) {
			out[p] = data
		}
	}

	return out, nil
}

// recordsFileEach reads a file of many versions, such as every live recording a plan reads: they
// go uncached, since a plan may read gigabytes of them once.
func (s *Store) recordsFileEach(ctx context.Context, versions []string, path string) (map[string][]byte, error) {
	got, err := s.Records.Read(ctx, versions, []string{path})
	if err != nil {
		return nil, err
	}

	out := make(map[string][]byte, len(versions))

	for v, files := range got {
		if data, ok := files[path]; ok {
			out[v] = data
		}
	}

	return out, nil
}
