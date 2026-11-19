# Step 2: install pipelinegen

pipelinegen is one static binary. You run it on your machine to set things
up and to see what a change would run; the CI pipeline downloads its own
copy, pinned by version and checksum.

## From a release (Linux and macOS)

Each release on the [releases
page](https://github.com/sideways-works/pipelinegen/releases) has a binary
for Linux and for macOS, on amd64 and arm64 (macOS since v0.2.0),
and a `SHA256SUMS` file. Download the binary for your machine and the
checksums, and check one against the other. The
version below is the one every output in this book was made with ([what
works today](../introduction/what-works-today.md#versions)); a newer
release can print some things differently.

```
$ VERSION=v0.4.0
$ curl -fsSLO https://github.com/sideways-works/pipelinegen/releases/download/$VERSION/pipelinegen-linux-amd64
$ curl -fsSLO https://github.com/sideways-works/pipelinegen/releases/download/$VERSION/SHA256SUMS
$ sha256sum --check --ignore-missing SHA256SUMS
pipelinegen-linux-amd64: OK
$ install -m 755 pipelinegen-linux-amd64 ~/bin/pipelinegen
$ pipelinegen version
v0.4.0
```

On macOS, download `pipelinegen-darwin-arm64` (Apple silicon) or
`pipelinegen-darwin-amd64` (Intel) instead, and check it with `grep
pipelinegen-darwin-arm64 SHA256SUMS | shasum -a 256 --check`.

**You should see** `OK` from the checksum and the version you picked.

**If you don't:** a checksum that fails means the download is broken or
not what the release published. Download it again; never run a binary
whose checksum fails.

## From source (any platform)

With Go 1.27 or newer:

```sh
go install github.com/sideways-works/pipelinegen/cmd/pipelinegen@latest
```

This puts `pipelinegen` in `$(go env GOPATH)/bin`, which should be on your
`PATH`. A binary built this way says `dev` when asked for its version.

## What else it uses

pipelinegen runs `bazelisk` from your `PATH` (step 1), and git. It
downloads one more tool itself on first use, a pinned release of
bazel-diff, into your user cache, and checks its checksum.

Every command describes itself:

```
$ pipelinegen help
usage: pipelinegen <command> [flags]

  slices     list the slices of every suite, or of one, with their sources
  affected   print the slices a change reaches
  check      check that every file is covered by a slice or declared untested: [--staged]
  ...
```

and `pipelinegen <command> --help` lists a command's flags.

Next: [step 3, a first config](first-config.md).
