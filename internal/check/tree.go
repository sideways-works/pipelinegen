package check

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/sideways-works/pipelinegen/internal/secret"
)

// Tree is a repository's files: the tracked paths and how to read one,
// from the work tree, git's index or a commit.
type Tree struct {
	Files []string
	Read  func(string) ([]byte, error)
}

// ErrGit wraps a git command that failed.
var ErrGit = errors.New("git")

func git(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git with check's own arguments
	cmd.Dir = root
	cmd.Env = secret.HardenGit(os.Environ())

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: git %s: %w: %s", ErrGit, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}

	return out, nil
}

func nulSplit(b []byte) []string {
	var out []string

	for f := range strings.SplitSeq(string(b), "\x00") {
		if f != "" {
			out = append(out, f)
		}
	}

	return out
}

// WorkTree is the work tree at root: the tracked files and the new ones git
// doesn't ignore, read from disk. A new symlink is left out: a link such as
// tools/plugins is a tool beside the tree.
func WorkTree(ctx context.Context, root string) (Tree, error) {
	tracked, err := git(ctx, root, "ls-files", "-z", "--cached")
	if err != nil {
		return Tree{}, err
	}

	others, err := git(ctx, root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return Tree{}, err
	}

	var files []string

	seen := map[string]bool{}

	add := func(list []byte, isNew bool) {
		for _, f := range nulSplit(list) {
			// a tracked file deleted in the work tree is gone for pack
			info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f)))
			if err != nil || seen[f] || isNew && info.Mode()&os.ModeSymlink != 0 {
				continue
			}

			seen[f] = true
			files = append(files, f)
		}
	}

	add(tracked, false)
	add(others, true)
	slices.Sort(files)

	return DirTree(root, files), nil
}

// DirTree reads the given files from the directory root.
func DirTree(root string, files []string) Tree {
	return Tree{
		Files: files,
		Read: func(p string) ([]byte, error) {
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p))) //nolint:gosec // a path of the repository's own files
			if err != nil {
				return nil, fmt.Errorf("read: %w", err)
			}

			return b, nil
		},
	}
}

// IndexTree is what the next commit holds: git's index at root. A commit
// hook checks this, not the work tree, so a change left unstaged can't make
// a staged one pass. closeFn ends the git process it reads through.
func IndexTree(ctx context.Context, root string) (tree Tree, closeFn func() error, err error) {
	out, err := git(ctx, root, "ls-files", "-z", "--cached", "--stage")
	if err != nil {
		return Tree{}, nil, err
	}

	// <mode> <object> <stage>\t<path>
	return blobTree(ctx, root, out, 1)
}

// CommitTree is the tree of a commit (HEAD, for what a commit hook compares
// the index with).
func CommitTree(ctx context.Context, root, rev string) (tree Tree, closeFn func() error, err error) {
	out, err := git(ctx, root, "ls-tree", "-r", "-z", "--full-tree", rev)
	if err != nil {
		return Tree{}, nil, err
	}

	// <mode> blob <object>\t<path>
	return blobTree(ctx, root, out, 2)
}

// blobTree reads a listing of NUL-separated "<fields>\t<path>" entries
// whose field at objField is the blob; submodules are left out.
func blobTree(ctx context.Context, root string, listing []byte, objField int) (Tree, func() error, error) {
	blobs := map[string]string{}

	var files []string

	for _, line := range nulSplit(listing) {
		meta, p, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)

		if !ok || len(fields) != 3 || fields[0] == "160000" {
			continue
		}

		if _, dup := blobs[p]; !dup {
			files = append(files, p)
		}

		blobs[p] = fields[objField]
	}

	r, err := newBlobReader(ctx, root)
	if err != nil {
		return Tree{}, nil, err
	}

	return Tree{
		Files: files,
		Read: func(p string) ([]byte, error) {
			obj, ok := blobs[p]
			if !ok {
				return nil, fmt.Errorf("%s: %w", p, fs.ErrNotExist)
			}

			return r.read(obj)
		},
	}, r.close, nil
}

// blobReader reads objects through one `git cat-file --batch`, so reading
// hundreds of BUILD files costs one process.
type blobReader struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func newBlobReader(ctx context.Context, root string) (*blobReader, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = root
	cmd.Env = secret.HardenGit(os.Environ())

	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: cat-file: %w", ErrGit, err)
	}

	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: cat-file: %w", ErrGit, err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: cat-file: %w", ErrGit, err)
	}

	return &blobReader{cmd: cmd, in: in, out: bufio.NewReader(out)}, nil
}

func (r *blobReader) read(obj string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, err := io.WriteString(r.in, obj+"\n"); err != nil {
		return nil, fmt.Errorf("%w: cat-file: %w", ErrGit, err)
	}

	header, err := r.out.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("%w: cat-file: %w", ErrGit, err)
	}

	// <object> <type> <size>, or <object> missing
	fields := strings.Fields(header)
	if len(fields) != 3 {
		return nil, fmt.Errorf("%w: cat-file: %s", ErrGit, strings.TrimSpace(header))
	}

	size, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, fmt.Errorf("%w: cat-file: %w", ErrGit, err)
	}

	data := make([]byte, size+1) // the content and its trailing newline
	if _, err := io.ReadFull(r.out, data); err != nil {
		return nil, fmt.Errorf("%w: cat-file: %w", ErrGit, err)
	}

	return data[:size], nil
}

func (r *blobReader) close() error {
	if err := r.in.Close(); err != nil {
		return fmt.Errorf("%w: cat-file: %w", ErrGit, err)
	}

	if err := r.cmd.Wait(); err != nil {
		return fmt.Errorf("%w: cat-file: %w", ErrGit, err)
	}

	return nil
}
