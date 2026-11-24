package gitstore_test

import (
	"path/filepath"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
)

func closing(t *testing.T, s *gitstore.Store) *gitstore.Store {
	t.Helper()

	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	return s
}

func TestTheRefsKeepAStoresPromises(t *testing.T) {
	storetest.Conform(t, func(t *testing.T) func(*testing.T) *gitstore.Store {
		t.Helper()

		remote := storetest.Bare(t)

		return func(t *testing.T) *gitstore.Store {
			t.Helper()

			return closing(t, &gitstore.Store{Remote: remote, Dir: filepath.Join(t.TempDir(), "scratch"), Retries: 10})
		}
	})
}

func TestABranchKeepsAStoresPromises(t *testing.T) {
	storetest.Conform(t, func(t *testing.T) func(*testing.T) *gitstore.Store {
		t.Helper()

		remote := storetest.Bare(t)

		return func(t *testing.T) *gitstore.Store {
			t.Helper()

			return closing(t, &gitstore.Store{
				Remote: remote, Dir: filepath.Join(t.TempDir(), "scratch"), Retries: 10,
				Branch: &branchstore.Store{Remote: remote, Branch: "pipelinegen", Dir: filepath.Join(t.TempDir(), "branch"), Retries: 10},
			})
		}
	})
}

func TestRecordsKeepAStoresPromises(t *testing.T) {
	storetest.Conform(t, func(*testing.T) func(*testing.T) *gitstore.Store {
		mem := storetest.NewMemRecords()

		return func(t *testing.T) *gitstore.Store {
			t.Helper()

			return closing(t, &gitstore.Store{Records: mem})
		}
	})
}
