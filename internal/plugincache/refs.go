package plugincache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

const RefPrefix = "refs/pipelinegen/cache/"

func Ref(b Build) string {
	sha := b.SHA256
	if len(sha) > 12 {
		sha = sha[:12]
	}

	return RefPrefix + b.Plugin + "/" + b.Version + "-" + sha
}

// ReplacedPrefix holds the records of replaced keys, one ref per build, apart from its answers so a plan
// reads the small record without fetching every answer.
const ReplacedPrefix = "refs/pipelinegen/replaced/"

func ReplacedRef(b Build) string {
	return ReplacedPrefix + strings.TrimPrefix(Ref(b), RefPrefix)
}

// replacedFile is the record's one file: a JSON object of hash to digest.
const replacedFile = "replaced.json"

// GenRef is the ref holding a build's answers of one generation: an ISO
// week (Generation).
func GenRef(b Build, gen string) string { return Ref(b) + "." + gen }

// Generation is the generation an answer written at t goes into: its ISO
// week.
func Generation(t time.Time) string {
	y, w := t.UTC().ISOWeek()

	return fmt.Sprintf("%04d-W%02d", y, w)
}

// Refs keeps the remote level as one ref per plugin build and ISO week, holding the answers plans wrote or
// used that week; a read falls back to last week's and Save carries it over, and Drop removes older ones.
type Refs struct {
	Store *gitstore.Store
	Clock func() time.Time

	mu    sync.Mutex
	read  map[string]refEntries
	carry map[Build]map[string]func() ([]byte, bool)
}

func (r *Refs) generations() (string, string) {
	now := time.Now()
	if r.Clock != nil {
		now = r.Clock()
	}

	return Generation(now), Generation(now.AddDate(0, 0, -7))
}

type refEntries struct {
	commit  string
	entries map[string][]byte
}

const saveTries = 5

func entryPath(hash string) string { return hash[:2] + "/" + hash[2:] }

// Answer reads one answer: this week's, else last week's, which the next
// Save carries into this week's.
func (r *Refs) Answer(ctx context.Context, b Build, hash string) ([]byte, bool, error) {
	gen, prev := r.generations()

	cur, err := r.entries(ctx, GenRef(b, gen))
	if err != nil {
		return nil, false, err
	}

	if data, ok := cur.entries[entryPath(hash)]; ok {
		return data, true, nil
	}

	old, err := r.entries(ctx, GenRef(b, prev))
	if err != nil {
		return nil, false, err
	}

	data, ok := old.entries[entryPath(hash)]
	if ok {
		r.Use(b, hash, nil)
	}

	return data, ok, nil
}

// Use notes that a plan used an answer, so the next Save keeps it in this
// week's ref.
func (r *Refs) Use(b Build, hash string, local func() ([]byte, bool)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.carry == nil {
		r.carry = map[Build]map[string]func() ([]byte, bool){}
	}

	if r.carry[b] == nil {
		r.carry[b] = map[string]func() ([]byte, bool){}
	}

	if r.carry[b][hash] == nil {
		r.carry[b][hash] = local
	}
}

// Using names the builds with answers used and not kept yet.
func (r *Refs) Using() []Build {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Build, 0, len(r.carry))

	for b, hashes := range r.carry {
		if len(hashes) > 0 {
			out = append(out, b)
		}
	}

	return slices.SortedFunc(slices.Values(out), compareBuilds)
}

func (r *Refs) Items(ctx context.Context, b Build, hash string) ([]string, bool, error) {
	data, ok, err := r.Answer(ctx, b, hash)
	if err != nil || !ok {
		return nil, false, err
	}

	var items []string
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, false, fmt.Errorf("cache %s: item record %s: %w", Ref(b), hash, err)
	}

	return items, true, nil
}

// Save writes a build's answers and edits by compare-and-swap: when the ref moved it reads again and applies
// the edits to the records as they stand, so two plans both land. An answer held with other bytes conflicts.
func (r *Refs) Save(ctx context.Context, b Build, answers map[string][]byte, edits map[string]Edit) ([]Conflict, error) {
	return r.save(ctx, b, answers, edits, false)
}

// Replace overwrites answers whatever the ref holds, and notes each key
// in the build's record of replaced keys in the same write.
func (r *Refs) Replace(ctx context.Context, b Build, answers map[string][]byte) error {
	_, err := r.save(ctx, b, answers, nil, true)

	return err
}

func (r *Refs) Replaced(ctx context.Context, b Build) (map[string]string, error) {
	got, err := r.entries(ctx, ReplacedRef(b))
	if err != nil {
		return nil, err
	}

	return replacedOf(got, ReplacedRef(b))
}

func replacedOf(got refEntries, ref string) (map[string]string, error) {
	out := map[string]string{}

	data, ok := got.entries[replacedFile]
	if !ok {
		return out, nil
	}

	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("cache %s: %w", ref, err)
	}

	return out, nil
}

func (r *Refs) replacedUpdate(ctx context.Context, b Build, answers map[string][]byte) (gitstore.Update, bool, error) {
	ref := ReplacedRef(b)

	got, err := r.entries(ctx, ref)
	if err != nil {
		return gitstore.Update{}, false, err
	}

	record, err := replacedOf(got, ref)
	if err != nil {
		return gitstore.Update{}, false, err
	}

	changed := false

	for hash, data := range answers {
		if d := Digest(data); record[hash] != d {
			record[hash], changed = d, true
		}
	}

	data, err := json.Marshal(record)
	if err != nil {
		return gitstore.Update{}, false, fmt.Errorf("cache %s: %w", ref, err)
	}

	return gitstore.Update{Ref: ref, Old: got.commit, Files: map[string][]byte{replacedFile: data}}, changed, nil
}

func (r *Refs) save(ctx context.Context, b Build, answers map[string][]byte, edits map[string]Edit, replace bool) ([]Conflict, error) {
	r.mu.Lock()
	carry := maps.Clone(r.carry[b])
	r.mu.Unlock()

	if len(answers) == 0 && len(edits) == 0 && len(carry) == 0 {
		return nil, nil
	}

	gen, prev := r.generations()
	ref := GenRef(b, gen)

	for range saveTries {
		got, err := r.entries(ctx, ref)
		if err != nil {
			return nil, err
		}

		old, err := r.entries(ctx, GenRef(b, prev))
		if err != nil {
			return nil, err
		}

		files, conflicts, err := merged(got.entries, old.entries, b, answers, edits, carry, replace)
		if err != nil {
			return nil, fmt.Errorf("cache %s: %w", ref, err)
		}

		// a write that leaves the ref as it stands is no write: a quiet
		// night's rebuild edits every record to what it holds already
		var updates []gitstore.Update
		if !maps.EqualFunc(files, got.entries, bytes.Equal) {
			updates = append(updates, gitstore.Update{Ref: ref, Old: got.commit, Files: files})
		}

		if replace {
			u, changed, err := r.replacedUpdate(ctx, b, answers)
			if err != nil {
				return nil, err
			}

			if changed {
				updates = append(updates, u)
			}
		}

		if len(updates) == 0 {
			r.kept(b, carry)

			return conflicts, nil
		}

		_, err = r.Store.Write(ctx, "pipelinegen cache", updates...)

		r.forget(ref)
		r.forget(ReplacedRef(b))

		if err == nil {
			r.kept(b, carry)

			return conflicts, nil
		}

		if !errors.Is(err, gitstore.ErrStale) {
			return nil, fmt.Errorf("cache %s: %w", ref, err)
		}
	}

	return nil, fmt.Errorf("cache %s: %w", ref, gitstore.ErrStale)
}

func (r *Refs) kept(b Build, carried map[string]func() ([]byte, bool)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for h := range carried {
		delete(r.carry[b], h)
	}
}

func merged(entries, old map[string][]byte, b Build, answers map[string][]byte, edits map[string]Edit,
	carry map[string]func() ([]byte, bool), replace bool,
) (map[string][]byte, []Conflict, error) {
	files := maps.Clone(entries)
	if files == nil {
		files = map[string][]byte{}
	}

	held := func(hash string) ([]byte, bool) {
		if data, ok := files[entryPath(hash)]; ok {
			return data, true
		}

		data, ok := old[entryPath(hash)]

		return data, ok
	}

	var conflicts []Conflict

	for _, hash := range slices.Sorted(maps.Keys(answers)) {
		data := answers[hash]
		if stored, ok := held(hash); ok && !bytes.Equal(stored, data) && !replace {
			conflicts = append(conflicts, Conflict{Build: b, Hash: hash, Stored: stored, Got: data})

			// what other machines read stays in use
			files[entryPath(hash)] = stored

			continue
		}

		files[entryPath(hash)] = data
	}

	for hash, e := range edits {
		var items []string
		if stored, ok := held(hash); ok && json.Unmarshal(stored, &items) != nil {
			items = nil
		}

		data, err := json.Marshal(e.apply(items))
		if err != nil {
			return nil, nil, fmt.Errorf("item record %s: %w", hash, err)
		}

		files[entryPath(hash)] = data
	}

	for hash, local := range carry {
		if _, ok := files[entryPath(hash)]; ok {
			continue
		}

		if data, ok := old[entryPath(hash)]; ok {
			files[entryPath(hash)] = data

			continue
		}

		// the shared level lost it: this plan's local copy goes back
		if local != nil {
			if data, ok := local(); ok {
				files[entryPath(hash)] = data
			}
		}
	}

	return files, conflicts, nil
}

func (r *Refs) forget(ref string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.read, ref)
}

func (r *Refs) entries(ctx context.Context, ref string) (refEntries, error) {
	r.mu.Lock()
	got, ok := r.read[ref]
	r.mu.Unlock()

	if ok {
		return got, nil
	}

	found, ok, err := r.Store.First(ctx, []string{ref})
	if err != nil {
		return refEntries{}, fmt.Errorf("cache %s: %w", ref, err)
	}

	got = refEntries{}

	if ok {
		files, err := r.Store.Files(ctx, found.Commit)
		if err != nil {
			return refEntries{}, fmt.Errorf("cache %s: %w", ref, err)
		}

		got = refEntries{commit: found.Commit, entries: files}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.read == nil {
		r.read = map[string]refEntries{}
	}

	r.read[ref] = got

	return got, nil
}

// replacedKeep is how long a build's replaced keys outlive its last generation: a machine with a stale
// local copy would plan with it only on a branch that old.
const replacedKeep = 90 * 24 * time.Hour

// Drop removes answer refs of generations before last week's or without one, and old records of
// replaced keys; a ref written meanwhile is left for the next time.
func Drop(ctx context.Context, store *gitstore.Store, now time.Time) (int, error) {
	refs, err := store.List(ctx, RefPrefix, ReplacedPrefix)
	if err != nil {
		return 0, fmt.Errorf("cache: %w", err)
	}

	prev := Generation(now.AddDate(0, 0, -7))
	live := map[string]bool{}

	var doomed []gitstore.Ref

	for name, ref := range refs {
		rest, ok := strings.CutPrefix(name, RefPrefix)
		if !ok {
			continue
		}

		base, gen, ok := cutGeneration(rest)
		if !ok || gen < prev {
			doomed = append(doomed, ref)

			continue
		}

		live[base] = true
	}

	for name, ref := range refs {
		if base, ok := strings.CutPrefix(name, ReplacedPrefix); ok && !live[base] && now.Sub(ref.Time) > replacedKeep {
			doomed = append(doomed, ref)
		}
	}

	if len(doomed) == 0 {
		return 0, nil
	}

	slices.SortFunc(doomed, func(a, b gitstore.Ref) int { return strings.Compare(a.Name, b.Name) })

	ups := make([]gitstore.Update, 0, len(doomed))
	for _, ref := range doomed {
		ups = append(ups, gitstore.Update{Ref: ref.Name, Old: ref.Commit, Delete: true})
	}

	_, err = store.Write(ctx, "pipelinegen cache: drop old generations", ups...)

	switch {
	case errors.Is(err, gitstore.ErrStale):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("cache: %w", err)
	}

	return len(doomed), nil
}

func cutGeneration(name string) (string, string, bool) {
	base, gen, ok := strings.CutLast(name, ".")
	if !ok {
		return "", "", false
	}

	y, w, ok := strings.Cut(gen, "-W")
	if !ok || len(y) != 4 || len(w) != 2 || strings.Trim(y+w, "0123456789") != "" {
		return "", "", false
	}

	return base, gen, true
}
