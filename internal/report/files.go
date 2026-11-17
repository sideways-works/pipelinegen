package report

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

// FailureFile is a file a result names for its slice, by its path from the
// result directory, with a short label: what a failed test left to show
// why (a screenshot, a trace).
type FailureFile struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
}

// KeptFile is a failure file a slice's ref holds, at files/<Path>.
type KeptFile struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
	Bytes int64  `json:"bytes"`
}

// DroppedFile is a failure file the result named and the ref doesn't
// hold, and why.
type DroppedFile struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
	Bytes int64  `json:"bytes,omitempty"`
	Why   string `json:"why"`
}

const (
	filesDir         = "files/"
	maxFilesPerSlice = 20
	maxFileLabel     = 80
	maxFilePath      = 300
	maxWhy           = 300
)

// FilesDir is where a driver leaves a slice's failure files: pipelinegen's
// own part of the batch's result directory, which no slice's extras take
// in and no slice may be named.
func FilesDir(result, slice string) string {
	return filepath.Join(result, JournalDir, "files", filepath.FromSlash(slice))
}

// FilesPrefix is FilesDir as a result file names it, from the result
// directory.
func FilesPrefix(slice string) string { return JournalDir + "/files/" + slice + "/" }

// resultRoot is the directory a result's files are named from: the batch's
// result directory, which holds result/<slice>.json, or a whole-suite
// check's, which holds result.json.
func resultRoot(ev SliceEvent) string {
	if root, ok := strings.CutSuffix(ev.Result, filepath.FromSlash(ev.Slice)+".json"); ok && root != "" {
		return filepath.Clean(root)
	}

	return filepath.Dir(ev.Result)
}

// checkFilePath refuses what could leave the result directory or can't be
// a path in a ref: an absolute path, a part that is empty, "." or "..",
// ".git", a backslash or a control character.
func checkFilePath(p string) error {
	if p == "" || len(p) > maxFilePath || path.IsAbs(p) || filepath.IsAbs(p) || strings.ContainsRune(p, '\\') {
		return errOutside
	}

	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return errOutside
		}
	}

	for part := range strings.SplitSeq(p, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return errOutside
		}
	}

	return nil
}

var (
	errOutside  = errors.New("not a plain path inside the result directory")
	errNotPlain = errors.New("not a regular file")
	errNoFile   = errors.New("no such file")
)

// candidate is a failure file read and ready to keep.
type candidate struct {
	kept KeptFile
	data []byte
}

// readFailureFiles reads the files a result names, through the result
// directory as a root: no path, symlink included, reaches outside it. It
// reads none larger than limit, and none beyond the first few.
func readFailureFiles(root string, slice string, list []FailureFile, limit int64) ([]candidate, []DroppedFile) {
	var (
		got     []candidate
		dropped []DroppedFile
		seen    = map[string]bool{}
	)

	drop := func(f FailureFile, n int64, why string) {
		p := f.Path
		if checkFilePath(p) == nil {
			p = storedName(slice, p)
		}

		dropped = append(dropped, DroppedFile{Path: capFileText(p, maxFilePath), Label: capFileText(f.Label, maxFileLabel), Bytes: n, Why: capFileText(why, maxWhy)})
	}

	r, err := os.OpenRoot(root)
	if err != nil {
		for _, f := range list {
			drop(f, 0, "the result directory can't be read: "+unrooted(err).Error())
		}

		return nil, dropped
	}

	defer func() { _ = r.Close() }() //nolint:errcheck // only read

	for i, f := range list {
		switch {
		case i >= maxFilesPerSlice:
			drop(f, 0, fmt.Sprintf("the result names more than %d files", maxFilesPerSlice))

			continue
		case checkFilePath(f.Path) != nil:
			drop(f, 0, errOutside.Error())

			continue
		case seen[storedName(slice, f.Path)]:
			continue
		}

		name := storedName(slice, f.Path)
		seen[name] = true

		data, n, err := readInRoot(r, f.Path, limit)
		if err != nil {
			drop(f, n, err.Error())

			continue
		}

		data = redactText(data)
		got = append(got, candidate{kept: KeptFile{Path: name, Label: capFileText(f.Label, maxFileLabel), Bytes: int64(len(data))}, data: data})
	}

	return got, dropped
}

// readInRoot reads a regular file whose size is at most limit. The size is
// asked before reading, so a file over the cap is never read, and checked
// again on what was opened, so nothing swapped in between is read either.
func readInRoot(r *os.Root, p string, limit int64) ([]byte, int64, error) {
	// a FIFO or a device would block or never end: only a regular file,
	// after any symlink inside the root, is opened at all
	fi, err := r.Stat(filepath.FromSlash(p))
	if err != nil {
		return nil, 0, unrooted(err)
	}

	if !fi.Mode().IsRegular() {
		return nil, 0, errNotPlain
	}

	if fi.Size() > limit {
		return nil, fi.Size(), fmt.Errorf("%w: %s", errOverSlice, size(limit))
	}

	f, err := r.Open(filepath.FromSlash(p))
	if err != nil {
		return nil, 0, unrooted(err)
	}

	defer func() { _ = f.Close() }() //nolint:errcheck // only read

	if fi, err = f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, 0, errNotPlain
	}

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, 0, unrooted(err)
	}

	if int64(len(data)) > limit {
		return nil, int64(len(data)), fmt.Errorf("%w: %s", errOverSlice, size(limit))
	}

	return data, int64(len(data)), nil
}

// unrooted says what went wrong without the machine's own paths.
func unrooted(err error) error {
	var pe *fs.PathError

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errNoFile
	case errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "escapes"):
		return errOutside
	case errors.As(err, &pe):
		return pe.Err
	}

	return err
}

var (
	errOverSlice = errors.New("over keep.files.slice")
	errOverRun   = errors.New("over keep.files.run")
)

// storedName is a file's path in the slice's ref: a driver's files by
// their path in the slice's files directory, a task's by their path from
// the result directory.
func storedName(slice, p string) string {
	if rest, ok := strings.CutPrefix(p, FilesPrefix(slice)); ok && rest != "" {
		return rest
	}

	return p
}

func capFileText(s string, n int) string {
	s = secret.Redact(s)
	if len(s) <= n {
		return s
	}

	return strings.ToValidUTF8(s[:n], "") + "..."
}

// capFiles keeps the files within limit, dropping the largest first, and
// says why each went.
func capFiles(got []candidate, limit int64, why error) ([]candidate, []DroppedFile) {
	sizes := make([]int64, len(got))
	for i, c := range got {
		sizes[i] = c.kept.Bytes
	}

	gone := largestOver(sizes, limit)

	var (
		kept    []candidate
		dropped []DroppedFile
	)

	for i, c := range got {
		if gone[i] {
			dropped = append(dropped, c.dropped(fmt.Sprintf("%v: %s", why, size(limit))))

			continue
		}

		kept = append(kept, c)
	}

	return kept, dropped
}

// largestOver says which of the sizes go so the rest add up to at most
// limit, taking the largest first.
func largestOver(sizes []int64, limit int64) []bool {
	gone := make([]bool, len(sizes))

	var total int64
	for _, n := range sizes {
		total += n
	}

	order := make([]int, len(sizes))
	for i := range order {
		order[i] = i
	}

	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(sizes[b], sizes[a]) })

	for _, i := range order {
		if total <= limit {
			break
		}

		total -= sizes[i]
		gone[i] = true
	}

	return gone
}

func (c candidate) dropped(why string) DroppedFile {
	return DroppedFile{Path: c.kept.Path, Label: c.kept.Label, Bytes: c.kept.Bytes, Why: why}
}

// redactText masks secrets in a file that reads as text. A binary file
// (a screenshot, a zipped trace) goes as it is: masking would break it,
// and its secrets are past finding by a pattern anyway.
func redactText(data []byte) []byte {
	if !utf8.Valid(data) || slices.Contains(data, 0) {
		return data
	}

	return []byte(secret.Redact(string(data)))
}

func size(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KB", n>>10)
	}

	return fmt.Sprintf("%d bytes", n)
}

// ending is one slice of a write: its state, its ref's other files, and
// the failure files found for it.
type ending struct {
	ev    SliceEvent
	sf    StateFile
	files map[string][]byte
	found []candidate
}

func (e *ending) dropAll(why string) {
	for _, c := range e.found {
		e.sf.Dropped = append(e.sf.Dropped, c.dropped(why))
	}

	e.found = nil
}

// filesUntil ends the retries of a write that carries failure files at
// half the store's retry time, so a slow store leaves the write without
// them time of its own within the step's limit. It bounds the retries, never
// a git process under way: one killed could leave a lock that fails the
// write without them too. It returns what puts the store's bound back.
func filesUntil(store *gitstore.Store, ends []*ending, now time.Time) func() {
	if !withFiles(ends) {
		return func() {}
	}

	half := store.RetryFor / 2
	if half <= 0 {
		half = filesWriteFor
	}

	was := store.Until
	if until := now.Add(half); was.IsZero() || until.Before(was) {
		store.Until = until
	}

	return func() { store.Until = was }
}

const filesWriteFor = 5 * time.Minute

func withFiles(ends []*ending) bool {
	return slices.ContainsFunc(ends, func(e *ending) bool { return len(e.found) > 0 })
}

// updates are the writes of the slices' refs, each with its failure files.
func updates(id ID, ends []*ending) ([]gitstore.Update, error) {
	ups := make([]gitstore.Update, 0, len(ends))

	for _, e := range ends {
		sf := e.sf
		files := maps.Clone(e.files)

		for _, c := range e.found {
			sf.Files = append(sf.Files, c.kept)
			files[filesDir+c.kept.Path] = c.data
		}

		data, err := encode(sf)
		if err != nil {
			return nil, err
		}

		files[stateFile] = data

		// only this slice's steps write its ref, one after another
		ups = append(ups, gitstore.Update{Ref: id.StateRef(e.ev.Suite, e.ev.Slice), Any: true, Files: files})
	}

	return ups, nil
}

// capRun keeps the write's failure files within what keep.files.run leaves
// the run, by what the run's other slices hold already, dropping the
// largest first. Batches write side by side with no lock between them, so
// two that count at once may each take what is left: the run may hold up
// to one write's files more per batch writing at that moment.
func (r *Reporter) capRun(ctx context.Context, ends []*ending) {
	if !withFiles(ends) {
		return
	}

	_, runCap := r.Config.FilesCaps()

	held, err := r.heldFiles(ctx, ends)
	if err != nil {
		for _, e := range ends {
			e.dropAll(capFileText("the run's kept files could not be counted: "+err.Error(), maxWhy))
		}

		return
	}

	var sizes []int64

	for _, e := range ends {
		for _, c := range e.found {
			sizes = append(sizes, c.kept.Bytes)
		}
	}

	gone := largestOver(sizes, max(runCap-held, 0))
	why := fmt.Sprintf("%v: %s", errOverRun, size(runCap))
	i := 0

	for _, e := range ends {
		var keep []candidate

		for _, c := range e.found {
			if gone[i] {
				e.sf.Dropped = append(e.sf.Dropped, c.dropped(why))
			} else {
				keep = append(keep, c)
			}

			i++
		}

		e.found = keep
	}
}

// heldFiles is how many bytes of failure files the run's slices hold, but
// for those of this write, whose refs it replaces.
func (r *Reporter) heldFiles(ctx context.Context, ends []*ending) (int64, error) {
	refs, err := r.Store.List(ctx, r.ID.Prefix())
	if err != nil {
		return 0, err
	}

	mine := map[string]bool{}
	for _, e := range ends {
		mine[r.ID.StateRef(e.ev.Suite, e.ev.Slice)] = true
	}

	snap := snapshot{refs: map[string]gitstore.Ref{}, states: map[string]StateFile{}}

	var commits []string

	for name, ref := range refs {
		if mine[name] || strings.HasPrefix(name, r.ID.EndRef("")) {
			continue
		}

		snap.refs[name] = ref
		commits = append(commits, ref.Commit)
	}

	if err := snap.readStates(ctx, r.Store, r.ID, commits); err != nil {
		return 0, err
	}

	var held int64

	for _, sf := range snap.states {
		for _, f := range sf.Files {
			held += f.Bytes
		}
	}

	return held, nil
}

// SliceFiles is what one slice of a run keeps of its failure files, and
// what it dropped.
type SliceFiles struct {
	Suite   string        `json:"suite"`
	Slice   string        `json:"slice"`
	Label   string        `json:"label"`
	State   State         `json:"state"`
	Files   []KeptFile    `json:"files,omitempty"`
	Dropped []DroppedFile `json:"dropped,omitempty"`

	commit string
}

// ErrNoRecords is a run the store holds nothing of: never run, or swept
// past keep.runs.
var ErrNoRecords = errors.New("the store holds no records of the run")

// RunFiles lists the slices of a run that keep failure files or dropped
// some, of one suite and one slice (by its name or label) when named.
func RunFiles(ctx context.Context, store *gitstore.Store, id ID, suite, slice string) ([]SliceFiles, error) {
	if err := id.Check(); err != nil {
		return nil, err
	}

	prefix := id.Prefix()

	if suite != "" {
		if err := checkName("suite", suite); err != nil {
			return nil, err
		}

		prefix += suite + "/"
	}

	refs, err := store.List(ctx, prefix)
	if err != nil {
		return nil, err
	}

	snap := snapshot{refs: map[string]gitstore.Ref{}, states: map[string]StateFile{}}

	var commits []string

	for name, ref := range refs {
		if strings.HasPrefix(name, id.EndRef("")) {
			continue
		}

		snap.refs[name] = ref
		commits = append(commits, ref.Commit)
	}

	if len(commits) == 0 {
		return nil, fmt.Errorf("%w: %s %s at %s", ErrNoRecords, id.Run, id.Change, short(id.Commit))
	}

	if err := snap.readStates(ctx, store, id, commits); err != nil {
		return nil, err
	}

	var out []SliceFiles

	for name, ref := range snap.refs {
		su, sl, ok, err := id.sliceOf(name)
		if err != nil || !ok {
			continue
		}

		sf := snap.states[su+"/"+sl]
		if slice != "" && slice != sl && slice != sf.Label {
			continue
		}

		if len(sf.Files) == 0 && len(sf.Dropped) == 0 {
			continue
		}

		out = append(out, SliceFiles{Suite: su, Slice: sl, Label: sf.Label, State: sf.State, Files: sf.Files, Dropped: sf.Dropped, commit: ref.Commit})
	}

	slices.SortFunc(out, func(a, b SliceFiles) int { return cmp.Or(cmp.Compare(a.Suite, b.Suite), cmp.Compare(a.Slice, b.Slice)) })

	return out, nil
}

// WriteFiles writes each slice's kept files into out, at
// <suite>/<slice>/<path>, and returns how many it wrote. It writes only
// what the slice's state names, each at a path checked again, through out
// as a root.
func WriteFiles(ctx context.Context, store *gitstore.Store, list []SliceFiles, out string) (int, error) {
	if err := os.MkdirAll(out, 0o750); err != nil {
		return 0, fmt.Errorf("files: %w", err)
	}

	root, err := os.OpenRoot(out)
	if err != nil {
		return 0, fmt.Errorf("files: %w", err)
	}

	defer func() { _ = root.Close() }() //nolint:errcheck // its writes are checked one by one

	n := 0

	for _, sf := range list {
		if len(sf.Files) == 0 {
			continue
		}

		if err := errors.Join(checkName("suite", sf.Suite), checkSlice(sf.Slice)); err != nil {
			return n, err
		}

		got, err := store.Under(ctx, sf.commit, strings.TrimSuffix(filesDir, "/"))
		if err != nil {
			return n, err
		}

		for _, f := range sf.Files {
			data, ok := got[filesDir+f.Path]
			if !ok || checkFilePath(f.Path) != nil {
				continue
			}

			dst := path.Join(sf.Suite, sf.Slice, f.Path)
			if err := root.MkdirAll(filepath.FromSlash(path.Dir(dst)), 0o750); err != nil {
				return n, fmt.Errorf("files: %w", err)
			}

			if err := root.WriteFile(filepath.FromSlash(dst), data, 0o600); err != nil {
				return n, fmt.Errorf("files: %w", err)
			}

			n++
		}
	}

	return n, nil
}

// FilesCommand is the command that fetches a slice's failure files, for a
// person to copy from the comment or from why.
func FilesCommand(run, change, commit, slice string) string {
	parts := []string{"pipelinegen report files", "--run", shellWord(run), "--change", shellWord(change)}
	if commit != "" {
		parts = append(parts, "--commit", shellWord(short(commit)))
	}

	if slice != "" {
		parts = append(parts, "--slice", shellWord(slice))
	}

	return strings.Join(append(parts, "--out", "failure-files"), " ")
}

func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=+,@") == "" {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
