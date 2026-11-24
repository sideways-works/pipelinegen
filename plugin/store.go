package plugin

import (
	"context"
	"os"
	"time"

	"github.com/sideways-works/pipelinegen/internal/secret"
	pk "github.com/sideways-works/plugkit/plugin"
)

// KindStore is a plugin that keeps pipelinegen's records instead of the repository's refs: plans,
// run and slice states, rounds, recordings and lists, the plugins' answers. See
// docs/writing-plugins/store.md.
const KindStore = "store"

// A store's calls. Names are a record's path, such as refs/pipelinegen/plans/<run>/<change>/<c12>;
// a version is an opaque token for exactly one content of one record, never reused, which a read
// finds without the record's name.
const (
	CallStoreList  = "store.list"
	CallStoreGet   = "store.get"
	CallStoreRead  = "store.read"
	CallStoreWrite = "store.write"
)

// The variables a store's credentials come in, read by the store plugin itself. A runner gives a
// step that only reads records a key that only reads, and a step that writes them one that writes.
const (
	EnvStoreAccessKeyID     = "PIPELINEGEN_STORE_ACCESS_KEY_ID"
	EnvStoreSecretAccessKey = "PIPELINEGEN_STORE_SECRET_ACCESS_KEY" //nolint:gosec // a variable's name, not a credential
	EnvStoreSessionToken    = "PIPELINEGEN_STORE_SESSION_TOKEN"     //nolint:gosec // a variable's name, not a credential
)

// storeEnv are the variables a store plugin keeps besides its toolchain's: its credentials and how
// it reaches the network.
var storeEnv = []string{
	EnvStoreAccessKeyID, EnvStoreSecretAccessKey, EnvStoreSessionToken,
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR",
}

// StoreRecord is a record's newest version and when it was written, by the writer's clock.
type StoreRecord struct {
	Name    string    `json:"name"`
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
}

type StoreListRequest struct {
	Request

	// Prefixes are of names; a record under any of them is listed.
	Prefixes []string `json:"prefixes"`
}

type StoreListAnswer struct {
	Answer

	Records []StoreRecord `json:"records"`
}

type StoreGetRequest struct {
	Request

	// Names, in order: the answer is the first of them that exists.
	Names []string `json:"names"`
}

type StoreGetAnswer struct {
	Answer

	Record *StoreRecord `json:"record,omitempty"`
}

type StoreReadRequest struct {
	Request

	Versions []string `json:"versions"`
	// Paths, when set, are the only files to read of each version.
	Paths []string `json:"paths,omitempty"`
}

type StoreReadAnswer struct {
	Answer

	// Files are each version's files by path; a version the store doesn't have is left out.
	Files map[string]map[string][]byte `json:"files"`
}

// StoreUpdate is one record's write: Files as its new content, or Delete. Expect is the version the
// record must have for the write to apply, empty for one that must not exist; Any applies it
// whatever the record holds.
type StoreUpdate struct {
	Name   string            `json:"name"`
	Expect string            `json:"expect,omitempty"`
	Any    bool              `json:"any,omitempty"`
	Delete bool              `json:"delete,omitempty"`
	Files  map[string][]byte `json:"files,omitempty"`
}

type StoreWriteRequest struct {
	Request

	Message string        `json:"message"`
	Updates []StoreUpdate `json:"updates"`
	// Time is the writer's clock, the records' time.
	Time time.Time `json:"time"`
}

// StoreWriteAnswer gives each written record's new version. Stale names the records whose
// expectation failed: the store applies the updates in order and stops at the first stale one, so
// those before it stand.
type StoreWriteAnswer struct {
	Answer

	Versions map[string]string `json:"versions"`
	Stale    []string          `json:"stale,omitempty"`
}

// CallStore calls a store plugin: its credentials and network settings reach it, and nothing else of
// the caller's environment but its toolchain's.
func (p Plugin) CallStore(ctx context.Context, call string, req, answer any) error {
	env := secret.PluginEnv(p.cacheEnv())

	for _, k := range storeEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}

	run := pk.Plugin{Name: p.Name, Path: p.Path, Dir: p.Dir, Env: env, Timeout: p.Timeout, Log: p.Log}

	return run.Call(ctx, call, req, answer)
}
