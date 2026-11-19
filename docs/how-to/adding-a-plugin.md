# Add a plugin

A plugin's entry pins one release of it: the version, and the checksum its
files are checked by before they run. You don't look the checksum up and
type it. `pipelinegen plugin add` finds the release, downloads every file
the pin will name, checks each, and writes the entry into
`pipelinegen.yaml`; `pipelinegen plugin upgrade` moves pins to newer
releases the same way. Both edit the file in place: comments, blank lines
and the order of the entries stay as they were, and only the lines of the
pins they write change. How planning uses each kind of plugin is in
[configuring plugins](plugins.md).

## A plugin from a repository of several

Most plugins live together in one repository, each in a directory of its
kind and name: `driver/go`, `sbom/go`, `correlator/react`. Each has its
own releases, tagged with its directory, so `driver/go/v0.2.1` is a release
of the go driver alone. Name the repository and the directories:

```sh
pipelinegen plugin add https://github.com/sideways-works/pipelinegen-plugins driver/go sbom/go
```

For each directory the command finds the newest release tagged
`<path>/vX.Y.Z` that is no prerelease. It downloads the release's
`SHA256SUMS` and every file of the plugin's program it lists, one per
platform, checks each against its line, and keeps them in the cache the
later commands fetch from. The plugin's name and kind come from the
`plugin.json` its release carries (its answer to `describe`), checked the
same way. It then writes the pin by the checksum of `SHA256SUMS`, one pin
for every platform, into the repository's group, which it makes at the end
of `plugins` when the config has none yet:

```yaml
plugins:
  - repository: https://github.com/sideways-works/pipelinegen-plugins
    with:
      - {name: go-driver, kind: driver, path: driver/go, release: {version: <tag>, sums: <checksum>}}
      - {name: go-sbom, kind: sbom, path: sbom/go, release: {version: <tag>, sums: <checksum>}}
```

A group is a spelling: each entry under `with` takes the group's
repository, and the config reads exactly as if each entry named it. The
group is found under any spelling of the repository, so `add` with
`sideways-works/pipelinegen-plugins` or
`git@github.com:sideways-works/pipelinegen-plugins.git` adds to the same
one. Add what the plugin needs that no file can say, such as a `config`
block, to its entry afterwards; the plugin's README lists its keys.

The command prints a line for each plugin it pinned. It writes nothing
when any plugin can't be pinned: a release whose file doesn't match its
`SHA256SUMS`, a release without that file, a directory with no release, or
a name the config already has. A driver's name is the one its steps use
(`run: go-driver`); `--name` gives one plugin another, `--name go` say,
when you add one directory at a time. `--version v0.2.1` pins that release
instead of the newest, a prerelease too.

## A plugin of a repository of its own

A repository that is one plugin is tagged `vX.Y.Z`. Leave out the
directory:

```sh
pipelinegen plugin add https://github.com/acme/lint-driver --name lint
```

Its entry is one of its own, at the end of `plugins`, with `repository` in
it. `owner/name` is GitHub's shorthand for the same URL and stays as you
write it.

## A plugin without a release

A plugin needs no release at all. A zip that `pipelinegen plugin build`
made from the plugin's `manifest.yaml`, hosted anywhere that serves https,
is pinned by its URL and sha256:

```sh
pipelinegen plugin add --zip https://plugins.example.com/shell-driver.zip --sha256 <sha256>
```

and a commit of the plugin's repository is pinned by hand, with `commit:`
in place of `release:`, and built from the same manifest on the machine
that runs it. [Building and hosting a plugin as a zip](plugin-zip.md) has
both.

## Moving to newer releases

```sh
pipelinegen plugin upgrade
```

moves every plugin pinned by a release to its newest release that is no
prerelease, and prints each plugin's old and new version, or that it is at
the newest already. Name plugins to move only those
(`pipelinegen plugin upgrade go-sbom go-correlator`). `--pre` takes a
prerelease too when it is the newest. Each new release is checked the way
`add` checks one. A plugin that can't be moved, because its releases
can't be listed or its newest release fails the check, is named with the
reason and keeps its pin; the others still move, and the command exits 1
at the end. A pin keeps its form: a pin by `sums` gets the new
`SHA256SUMS`'s checksum, a pin by `sha256` the new `linux-amd64` file's (or
the Node script's). A plugin pinned by a commit or a zip is left to you.

A pull request that changes a pin is planned with the pins the pipeline was
made from, so the new release takes effect once it is merged; [trying a new
pin before merging](plugins.md#trying-a-new-pin-before-merging) runs it
earlier.

## Where releases come from

`add` and `upgrade` read the list of releases from GitHub's API (sending
`GITHUB_TOKEN` when it is set, which GitHub's request limit wants for more
than a few calls an hour) or GitLab's. A repository on any other git server
can be named, but has no release files pipelinegen can fetch yet. Every
command that runs a plugin fetches it the same way, by its pin, and a file
that doesn't match its pin never runs.

## Starting a plugin of your own

```sh
pipelinegen plugin new sbom rust
```

writes a new sbom plugin into `rust-sbom/` (or `--into <dir>`), from the
template the plugins repository keeps for each kind under
`templates/<kind>/`. The templates are released together, tagged
`templates/vX.Y.Z`; the command fetches the newest that is no prerelease,
or `--version`'s, with git, so any repository git can fetch works with
`--from <git-url>`. A template is a Go module with one program under
`cmd/`. The copy gets its own module path, the template's with
`templates/<kind>` made `<kind>/<name>` (`--module` sets another), its
program becomes `<name>-<kind>`, `rust-sbom` here, and the template's own
directory, `templates/<kind>` wherever a file names it (its `BUILD.bazel`,
its README), becomes the new plugin's: its path in the git work tree that
holds it, or `<kind>/<name>` outside one. Each is rewritten in every
file's text and path. [Writing plugins](../writing-plugins/README.md) says
what each kind answers.
