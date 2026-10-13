// Package plugincache keeps plugins' answers by content: in a local
// directory and, behind it, a remote level shared by every machine (a ref of
// the repository per plugin build). It is only a cache: losing it costs
// time, never a wrong answer.
package plugincache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Key names one answer: the plugin, exactly which build of it, its config
// for the call, the call, and the content the answer depends on.
type Key struct {
	Plugin  string
	Version string
	SHA256  string
	Config  any
	Call    string
	Content []string
}

// Hash is the key's address.
func (k Key) Hash() (string, error) {
	cfg, err := json.Marshal(k.Config)
	if err != nil {
		return "", fmt.Errorf("cache key: %w", err)
	}

	content := append([]string(nil), k.Content...)
	sort.Strings(content)

	h := sha256.New()

	for _, part := range []string{k.Plugin, k.Version, k.SHA256, string(cfg), k.Call, strings.Join(content, "\n")} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// Build names one plugin build: the answers of a build are kept together,
// so a build no longer used is dropped whole.
type Build struct {
	Plugin, Version, SHA256 string
}

func (k Key) build() Build { return Build{Plugin: k.Plugin, Version: k.Version, SHA256: k.SHA256} }

// Edit changes an item record: Add puts items in, Remove takes items out.
// An edit is applied to the record as the store holds it when the edit is
// written, and again whenever that write is replayed, so two plans editing
// one record never lose each other's items.
type Edit struct {
	Add, Remove []string
}

// apply is the record after the edit: sorted, each item once. A record
// edited exists afterwards, empty or not.
func (e Edit) apply(items []string) []string {
	out := make([]string, 0, len(items)+len(e.Add))

	for _, it := range items {
		if !slices.Contains(e.Remove, it) {
			out = append(out, it)
		}
	}

	out = append(out, e.Add...)
	slices.Sort(out)

	return slices.Compact(out)
}

func (e Edit) then(o Edit) Edit {
	add := slices.DeleteFunc(slices.Clone(e.Add), func(it string) bool { return slices.Contains(o.Remove, it) })
	rm := slices.DeleteFunc(slices.Clone(e.Remove), func(it string) bool { return slices.Contains(o.Add, it) })

	return Edit{Add: append(add, o.Add...), Remove: append(rm, o.Remove...)}
}

// Remote is the shared level behind the local directory: where any
// machine finds the answers any other one computed.
type Remote interface {
	// Answer reads one answer by its key's hash; false when it has none.
	Answer(ctx context.Context, b Build, hash string) ([]byte, bool, error)
	// Items reads an item record; false when it has none.
	Items(ctx context.Context, b Build, hash string) ([]string, bool, error)
	// Save writes a build's new answers and applies its edits, in one
	// write. Answers are write-once: a hash always names the same bytes. An
	// answer the level holds already with the same bytes changes nothing;
	// one it holds with other bytes is never overwritten: it comes back as
	// a Conflict, and the rest of the write lands.
	Save(ctx context.Context, b Build, answers map[string][]byte, edits map[string]Edit) ([]Conflict, error)
	// Replace overwrites answers whatever the level holds for them: for
	// the run that rebuilt them cold, after it reported each conflict.
	Replace(ctx context.Context, b Build, answers map[string][]byte) error
	// Replaced are the keys Replace ever wrote for a build, each with the
	// digest (Digest) of the bytes it left: a local copy of such a key
	// with other bytes is stale.
	Replaced(ctx context.Context, b Build) (map[string]string, error)
}

// Digest names an answer's bytes in the record of replaced keys.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// Conflict is a write-once answer the remote level holds with other bytes
// than a fresh one for the same key: the answer has an input its key doesn't
// name, or the plugin isn't deterministic. A plan that meets one keeps its
// own fresh answer, overwrites nothing and warns once per key.
type Conflict struct {
	Build       Build
	Hash        string
	Stored, Got []byte
}

// ErrConflict is wrapped by a ConflictError.
var ErrConflict = errors.New("cached answers differ from fresh ones")

// ConflictError is what Flush returns when answers conflicted; every other
// answer and edit was written.
type ConflictError struct {
	Conflicts []Conflict
}

func (e *ConflictError) Error() string {
	parts := make([]string, 0, len(e.Conflicts))
	for _, c := range e.Conflicts {
		parts = append(parts, c.Build.Plugin+" "+c.Build.Version+" key "+c.Hash)
	}

	return fmt.Sprintf("%v: %s", ErrConflict, strings.Join(parts, ", "))
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

// Cache is the local directory and, behind it, the remote level.
type Cache struct {
	Dir string
	// Remote is the shared level; nil keeps the cache local.
	Remote Remote
	// Push writes new answers to the remote level (default read-only).
	Push bool
	// HeldBytes bounds the bytes of fresh answers kept in memory until
	// Flush, and of each write Flush makes (default DefaultHeldBytes):
	// the rest are read back from the local directory.
	HeldBytes int

	mu       sync.Mutex
	fresh    map[Build]map[string][]byte
	edits    map[Build]map[string]Edit
	held     int
	replaced map[Build]map[string]string
}

// Keeper is a remote level that drops answers no plan used for a while,
// so it must hear of every answer a plan used, its local hits too: a
// machine whose local directory answers everything would otherwise let
// the shared level forget what every other machine needs. Save keeps
// what was used.
type Keeper interface {
	// Use notes that a plan used an answer from its local directory;
	// local reads it, should the level have lost it.
	Use(b Build, hash string, local func() ([]byte, bool))
	// Using names the builds with uses that no Save has kept yet.
	Using() []Build
}

func (c *Cache) use(b Build, hash string) {
	keeper, ok := c.Remote.(Keeper)
	if !ok || !c.Push {
		return
	}

	keeper.Use(b, hash, func() ([]byte, bool) {
		data, err := os.ReadFile(c.local(hash))

		return data, err == nil
	})
}

// ErrMiss is an answer the cache doesn't have.
var ErrMiss = errors.New("not cached")

// Get returns the answer for a key: from the local directory, else from
// the remote level.
func (c *Cache) Get(ctx context.Context, k Key) ([]byte, error) {
	hash, err := k.Hash()
	if err != nil {
		return nil, err
	}

	if data, err := os.ReadFile(c.local(hash)); err == nil {
		stale, err := c.stale(ctx, k.build(), hash, data)
		if err != nil {
			return nil, err
		}

		if !stale {
			c.use(k.build(), hash)

			return data, nil
		}
	}

	if c.Remote == nil {
		return nil, ErrMiss
	}

	data, ok, err := c.Remote.Answer(ctx, k.build(), hash)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, ErrMiss
	}

	c.saveLocal(hash, data)

	return data, nil
}

// stale tells whether a local copy is of a key the remote level replaced
// since, with other bytes: this machine may have kept the answer the
// replacing run found wrong.
func (c *Cache) stale(ctx context.Context, b Build, hash string, data []byte) (bool, error) {
	if c.Remote == nil {
		return false, nil
	}

	c.mu.Lock()
	replaced, ok := c.replaced[b]
	c.mu.Unlock()

	if !ok {
		got, err := c.Remote.Replaced(ctx, b)
		if err != nil {
			return false, err
		}

		replaced = got

		c.mu.Lock()
		if c.replaced == nil {
			c.replaced = map[Build]map[string]string{}
		}

		c.replaced[b] = replaced
		c.mu.Unlock()
	}

	digest, ok := replaced[hash]

	return ok && digest != Digest(data), nil
}

// Put keeps an answer locally and, when the cache pushes, for the next
// Flush.
func (c *Cache) Put(k Key, data []byte) error {
	hash, err := k.Hash()
	if err != nil {
		return err
	}

	saved := c.saveLocal(hash, data)

	if c.Remote == nil || !c.Push {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.fresh == nil {
		c.fresh = map[Build]map[string][]byte{}
	}

	b := k.build()
	if c.fresh[b] == nil {
		c.fresh[b] = map[string][]byte{}
	}

	if old, ok := c.fresh[b][hash]; ok {
		c.held -= len(old)
	}

	// past the bound an answer the local directory holds is read back from
	// there when it is written: a cold pass puts every answer of a tree
	if saved && c.held+len(data) > c.heldBytes() {
		c.fresh[b][hash] = nil

		return nil
	}

	c.fresh[b][hash] = data
	c.held += len(data)

	return nil
}

// DefaultHeldBytes bounds the fresh answers a Cache keeps in memory
// between Flushes, when HeldBytes is not set.
const DefaultHeldBytes = 64 << 20

func (c *Cache) heldBytes() int {
	if c.HeldBytes > 0 {
		return c.HeldBytes
	}

	return DefaultHeldBytes
}

// Held is how many bytes of fresh answers the cache keeps in memory now.
func (c *Cache) Held() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.held
}

// Items reads an item record: from the local directory, else from the
// remote level; false when neither has it.
func (c *Cache) Items(ctx context.Context, k Key) ([]string, bool, error) {
	hash, err := k.Hash()
	if err != nil {
		return nil, false, err
	}

	if data, err := os.ReadFile(c.local(hash)); err == nil {
		var items []string
		if json.Unmarshal(data, &items) == nil {
			c.use(k.build(), hash)

			return items, true, nil
		}
	}

	if c.Remote == nil {
		return nil, false, nil
	}

	items, ok, err := c.Remote.Items(ctx, k.build(), hash)
	if err != nil || !ok {
		return nil, false, err
	}

	c.saveItems(hash, items)

	return items, true, nil
}

// Edit changes an item record: here at once, and on the remote level at
// the next Flush, against the record as it stands then.
func (c *Cache) Edit(ctx context.Context, k Key, e Edit) error {
	hash, err := k.Hash()
	if err != nil {
		return err
	}

	items, _, err := c.Items(ctx, k)
	if err != nil {
		return err
	}

	c.saveItems(hash, e.apply(items))

	if c.Remote == nil || !c.Push {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.edits == nil {
		c.edits = map[Build]map[string]Edit{}
	}

	b := k.build()
	if c.edits[b] == nil {
		c.edits[b] = map[string]Edit{}
	}

	if old, ok := c.edits[b][hash]; ok {
		e = old.then(e)
	}

	c.edits[b][hash] = e

	return nil
}

// Flush writes the answers and edits made since the last Flush, one write
// per build. Answers the remote level holds with other bytes are left as
// they are and come back in a *ConflictError, after everything else was
// written.
func (c *Cache) Flush(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	builds := map[Build]bool{}
	for b := range c.fresh {
		builds[b] = true
	}

	for b := range c.edits {
		builds[b] = true
	}

	if keeper, ok := c.Remote.(Keeper); ok && c.Push {
		for _, b := range keeper.Using() {
			builds[b] = true
		}
	}

	var conflicts []Conflict

	for _, b := range slices.SortedFunc(maps.Keys(builds), compareBuilds) {
		got, err := c.save(ctx, b)
		if err != nil {
			return err
		}

		conflicts = append(conflicts, got...)

		delete(c.fresh, b)
		delete(c.edits, b)
	}

	c.held = 0

	if len(conflicts) > 0 {
		return &ConflictError{Conflicts: conflicts}
	}

	return nil
}

// save writes one build's fresh answers and edits in writes of at most the
// held bound each, reading answers past the bound back from the local
// directory. Called with c.mu held.
func (c *Cache) save(ctx context.Context, b Build) ([]Conflict, error) {
	var (
		conflicts []Conflict
		chunk     = map[string][]byte{}
		size      int
	)

	write := func(edits map[string]Edit) error {
		got, err := c.Remote.Save(ctx, b, chunk, edits)
		conflicts = append(conflicts, got...)
		chunk, size = map[string][]byte{}, 0

		return err
	}

	for _, hash := range slices.Sorted(maps.Keys(c.fresh[b])) {
		data := c.fresh[b][hash]
		if data == nil {
			read, err := os.ReadFile(c.local(hash))
			if err != nil {
				// gone from the local directory: the answer is only
				// computed again later
				continue
			}

			data = read
		}

		if size > 0 && size+len(data) > c.heldBytes() {
			if err := write(nil); err != nil {
				return nil, err
			}
		}

		chunk[hash] = data
		size += len(data)
	}

	if err := write(c.edits[b]); err != nil {
		return nil, err
	}

	return conflicts, nil
}

// Stored reads the remote level's answer for a key, past the local
// directory: what another machine would read.
func (c *Cache) Stored(ctx context.Context, k Key) ([]byte, bool, error) {
	if c.Remote == nil {
		return nil, false, nil
	}

	hash, err := k.Hash()
	if err != nil {
		return nil, false, err
	}

	return c.Remote.Answer(ctx, k.build(), hash)
}

// StoredItems reads the remote level's item record for a key, past the
// local directory: what another machine would read.
func (c *Cache) StoredItems(ctx context.Context, k Key) ([]string, bool, error) {
	if c.Remote == nil {
		return nil, false, nil
	}

	hash, err := k.Hash()
	if err != nil {
		return nil, false, err
	}

	return c.Remote.Items(ctx, k.build(), hash)
}

// Answer is one answer and its key.
type Answer struct {
	Key  Key
	Data []byte
}

// Replace overwrites the remote level's answers for keys, whatever it
// holds, and keeps them locally.
func (c *Cache) Replace(ctx context.Context, answers []Answer) error {
	byBuild := map[Build]map[string][]byte{}

	for _, a := range answers {
		hash, err := a.Key.Hash()
		if err != nil {
			return err
		}

		c.saveLocal(hash, a.Data)

		b := a.Key.build()
		if byBuild[b] == nil {
			byBuild[b] = map[string][]byte{}
		}

		byBuild[b][hash] = a.Data
	}

	if c.Remote == nil {
		return nil
	}

	for _, b := range slices.SortedFunc(maps.Keys(byBuild), compareBuilds) {
		if err := c.Remote.Replace(ctx, b, byBuild[b]); err != nil {
			return err
		}
	}

	return nil
}

func compareBuilds(a, b Build) int {
	return strings.Compare(a.Plugin+"\x00"+a.Version+"\x00"+a.SHA256, b.Plugin+"\x00"+b.Version+"\x00"+b.SHA256)
}

func (c *Cache) saveItems(hash string, items []string) {
	if items == nil {
		items = []string{}
	}

	data, err := json.Marshal(items)
	if err != nil {
		return
	}

	c.saveLocal(hash, data)
}

func (c *Cache) local(hash string) string {
	return filepath.Join(c.Dir, hash[:2], hash[2:])
}

// saveLocal writes an answer to a temporary name and renames it, so
// concurrent runs never read half an answer, and tells whether it did. A
// failure only costs a recomputation later, so it is not reported.
func (c *Cache) saveLocal(hash string, data []byte) bool {
	if c.Dir == "" {
		return false
	}

	p := c.local(hash)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return false
	}

	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return false
	}

	_, werr := tmp.Write(data)
	cerr := tmp.Close()

	if werr != nil || cerr != nil || os.Rename(tmp.Name(), p) != nil {
		_ = os.Remove(tmp.Name()) //nolint:errcheck // a leftover temporary file costs only space

		return false
	}

	return true
}
