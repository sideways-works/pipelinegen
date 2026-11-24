package pluginstore_test

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/pluginstore"
	"github.com/sideways-works/pipelinegen/plugin"
)

// TestMain lets the test binary act as a store plugin keeping its records in a directory, which its
// config names, when started with a store's call: each call is a process of its own, as the core
// runs a store plugin.
func TestMain(m *testing.M) {
	if len(os.Args) < 2 || (!strings.HasPrefix(os.Args[1], "store.") && os.Args[1] != plugin.CallDescribe) {
		os.Exit(m.Run())
	}

	os.Exit(plugin.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, map[string]plugin.Handler{
		plugin.CallDescribe: func([]byte) (any, error) {
			return plugin.Description{Protocol: plugin.Protocol, Kind: plugin.KindStore, Name: "dirstore"}, nil
		},
		plugin.CallStoreList:  dirList,
		plugin.CallStoreGet:   dirGet,
		plugin.CallStoreRead:  dirRead,
		plugin.CallStoreWrite: dirWrite,
	}))
}

var errDir = errors.New("dirstore")

func wrap(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%w: %w", errDir, err)
}

// A dirstore keeps a version per file under v/ and each record's head under h/, its name escaped;
// a write holds an exclusive lock on the directory.

func dirOf(req plugin.Request) (string, error) {
	dir, ok := req.Config["dir"].(string)
	if !ok || dir == "" {
		return "", fmt.Errorf("%w: no dir", errDir)
	}

	return dir, nil
}

func headPath(dir, name string) string { return filepath.Join(dir, "h", url.PathEscape(name)) }

func readHead(dir, name string) (plugin.StoreRecord, bool, error) {
	data, err := os.ReadFile(headPath(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return plugin.StoreRecord{}, false, nil
	}

	if err != nil {
		return plugin.StoreRecord{}, false, wrap(err)
	}

	var rec plugin.StoreRecord

	return rec, true, wrap(json.Unmarshal(data, &rec))
}

func dirList(in []byte) (any, error) {
	var req plugin.StoreListRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, wrap(err)
	}

	dir, err := dirOf(req.Request)
	if err != nil {
		return nil, wrap(err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "h"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, wrap(err)
	}

	ans := plugin.StoreListAnswer{Records: []plugin.StoreRecord{}}

	for _, e := range entries {
		name, err := url.PathUnescape(e.Name())
		if err != nil {
			return nil, wrap(err)
		}

		for _, p := range req.Prefixes {
			if strings.HasPrefix(name, p) {
				rec, ok, err := readHead(dir, name)
				if err != nil {
					return nil, wrap(err)
				}

				if ok {
					ans.Records = append(ans.Records, rec)
				}

				break
			}
		}
	}

	return ans, nil
}

func dirGet(in []byte) (any, error) {
	var req plugin.StoreGetRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, wrap(err)
	}

	dir, err := dirOf(req.Request)
	if err != nil {
		return nil, wrap(err)
	}

	for _, n := range req.Names {
		rec, ok, err := readHead(dir, n)
		if err != nil {
			return nil, wrap(err)
		}

		if ok {
			return plugin.StoreGetAnswer{Record: &rec}, nil
		}
	}

	return plugin.StoreGetAnswer{}, nil
}

func dirRead(in []byte) (any, error) {
	var req plugin.StoreReadRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, wrap(err)
	}

	dir, err := dirOf(req.Request)
	if err != nil {
		return nil, wrap(err)
	}

	ans := plugin.StoreReadAnswer{Files: map[string]map[string][]byte{}}

	for _, v := range req.Versions {
		data, err := os.ReadFile(filepath.Join(dir, "v", filepath.Base(v)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			return nil, wrap(err)
		}

		var files map[string][]byte
		if err := json.Unmarshal(data, &files); err != nil {
			return nil, wrap(err)
		}

		if req.Paths != nil {
			some := map[string][]byte{}

			for _, p := range req.Paths {
				if d, ok := files[p]; ok {
					some[p] = d
				}
			}

			files = some
		}

		ans.Files[v] = files
	}

	return ans, nil
}

func dirWrite(in []byte) (any, error) {
	var req plugin.StoreWriteRequest
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, wrap(err)
	}

	dir, err := dirOf(req.Request)
	if err != nil {
		return nil, wrap(err)
	}

	for _, sub := range []string{"h", "v"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
			return nil, wrap(err)
		}
	}

	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, wrap(err)
	}
	defer lock.Close() //nolint:errcheck // closing the lock releases it

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, wrap(err)
	}

	ans := plugin.StoreWriteAnswer{Versions: map[string]string{}}

	for _, u := range req.Updates {
		cur, exists, err := readHead(dir, u.Name)
		if err != nil {
			return nil, wrap(err)
		}

		if !u.Any && ((u.Expect == "" && exists) || (u.Expect != "" && cur.Version != u.Expect)) {
			ans.Stale = []string{u.Name}

			return ans, nil
		}

		if u.Delete {
			if err := os.Remove(headPath(dir, u.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, wrap(err)
			}

			continue
		}

		v, err := version()
		if err != nil {
			return nil, wrap(err)
		}

		data, err := json.Marshal(u.Files)
		if err != nil {
			return nil, wrap(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "v", v), data, 0o600); err != nil {
			return nil, wrap(err)
		}

		head, err := json.Marshal(plugin.StoreRecord{Name: u.Name, Version: v, Time: req.Time})
		if err != nil {
			return nil, wrap(err)
		}

		if err := os.WriteFile(headPath(dir, u.Name), head, 0o600); err != nil {
			return nil, wrap(err)
		}

		ans.Versions[u.Name] = v
	}

	return ans, nil
}

func version() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", wrap(err)
	}

	return hex.EncodeToString(b), nil
}

func TestAStorePluginKeepsAStoresPromises(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	storetest.Conform(t, func(t *testing.T) func(*testing.T) *gitstore.Store {
		t.Helper()

		dir := t.TempDir()

		return func(t *testing.T) *gitstore.Store {
			t.Helper()

			r := &pluginstore.Records{
				Plugin: plugin.Plugin{Name: "dirstore", Path: self, Config: map[string]any{"dir": dir}, Timeout: time.Minute},
			}

			s := &gitstore.Store{Records: r}

			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})

			return s
		}
	})
}

// TestAnExternalStorePluginKeepsAStoresPromises holds a store plugin built elsewhere to the same
// promises: PIPELINEGEN_TEST_STORE_PLUGIN is its program and PIPELINEGEN_TEST_STORE_CONFIG its
// config, whose prefix each backend extends with one of its own; its credentials come as the core
// hands them, in PIPELINEGEN_STORE_*.
func TestAnExternalStorePluginKeepsAStoresPromises(t *testing.T) {
	path, raw := os.Getenv("PIPELINEGEN_TEST_STORE_PLUGIN"), os.Getenv("PIPELINEGEN_TEST_STORE_CONFIG")
	if path == "" {
		t.Skip("PIPELINEGEN_TEST_STORE_PLUGIN names no store plugin")
	}

	var base map[string]any
	if err := json.Unmarshal([]byte(raw), &base); err != nil {
		t.Fatalf("PIPELINEGEN_TEST_STORE_CONFIG: %v", err)
	}

	storetest.Conform(t, func(t *testing.T) func(*testing.T) *gitstore.Store {
		t.Helper()

		v, err := version()
		if err != nil {
			t.Fatal(err)
		}

		config := maps.Clone(base)

		prefix, _ := base["prefix"].(string) //nolint:errcheck // none is the bucket's root
		config["prefix"] = prefix + v[:12] + "/"

		return func(t *testing.T) *gitstore.Store {
			t.Helper()

			s := &gitstore.Store{Records: &pluginstore.Records{
				Plugin: plugin.Plugin{Name: "external", Path: path, Config: config, Cache: t.TempDir(), Timeout: time.Minute},
			}}

			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})

			return s
		}
	})
}
