# Where pipelinegen keeps things on your machine

`pipelinegen affected`, `why`, `check` and `plugins claims` run on your
machine. Everything they keep between runs lives in one cache directory;
nothing is written into your repository's working tree.

## The cache directory

By default the cache is a directory per repository,
`pipelinegen/repos/<repository>` under your user cache (worktrees of one
repository share it, and two repositories never share one):

| System | Default |
|---|---|
| Linux | `$XDG_CACHE_HOME/pipelinegen/repos/<repository>`, or `~/.cache/pipelinegen/repos/<repository>` when `XDG_CACHE_HOME` isn't set |
| macOS | `~/Library/Caches/pipelinegen/repos/<repository>` |

`--cache <dir>` on `affected` (and the other commands that plan) uses
another directory; setting `XDG_CACHE_HOME` moves the default on Linux. It
is safe to delete the whole directory: the next plan builds it again, only
slower.

## What is in it

| Path | What it holds |
|---|---|
| `tree/` | a checkout of the commit being planned. Bazel and the plugins run there, never in your working tree, so a plan sees exactly the committed files. |
| `answers/` | the plugins' answers, by file content, so a plan asks a plugin only about files that changed |
| `plugins/<name>/` | each plugin's own scratch space |
| `snapshots/` | what Bazel and bazel-diff said about a commit, reused when the same commit is planned again |
| `caches/` | caches the plugins read while they run (Go's module and build caches); `affected`, `why` and `try` fill them first, as each plugin's `describe` says |
| `bazel-diff/` | the pinned bazel-diff release, downloaded on first use and checked against its checksum |
| `store/` | a local copy of the records `affected` and `why` read from the repository's remote |
| `lock` | held while a plan runs, so two plans don't share the work tree at once |

Plugins themselves are kept once for every repository, in
`pipelinegen/releases` under your user cache: a release's file under its
sha256, a plugin pinned by a zip unpacked under the zip's sha256, and a
plugin pinned by a commit built under a key of its source, its manifest,
the platform and the build image. Beside them, `releases/git/` holds a bare
copy of each repository a commit was fetched from. Whatever no command
used for 30 days is removed by the next command that fetches a plugin.

## The work tree and your toolchain

The plugins run in `tree/`, a directory outside your repository. A version
manager that picks the Go or Node version by a file in the repository
(`.tool-versions`, `.go-version`, `.nvmrc`) finds that file there only if
it is committed. When it isn't, the work tree gets your default version,
and a Go plugin that needs a newer Go than that can't type-check the code:
the plan warns that the plugin couldn't tell, and binds wide, so more
slices run than should. Commit the version file, or put the right Go first
on your `PATH` before you plan.
[Troubleshooting](../troubleshooting.md#a-go-plugin-cant-tell-because-of-the-go-version)
has the message.

Bazel keeps its own output under `~/.cache/bazel` as usual; set
`TEST_TMPDIR` or a `--output_user_root` startup flag
(`bazel.startup_flags` in the config) to move it.
