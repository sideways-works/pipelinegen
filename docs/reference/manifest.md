# The plugin manifest

A plugin that is built from source, or packed as a zip, has a
`manifest.yaml` in its directory: the repository's root for a repository
that is one plugin, or the plugin's `path` in a repository of several. It
names the plugin and says how to build it. pipelinegen reads it to build a
plugin pinned by `commit`, `pipelinegen plugin build` reads it to make a
zip, and a zip carries it unchanged at its top level, where a plugin
pinned by `zip` is named by it. The [config reference](config.md#plugins)
has the three ways to pin a plugin, and
[building and hosting a plugin as a zip](../how-to/plugin-zip.md) the
steps.

```yaml
name: go-driver
kind: driver
program: go-driver
build:
  image: golang:1.27.1@sha256:<64 hex>
  run: [go, build, -trimpath, -o, "{out}/go-driver", ./cmd/go-driver]
  env: {CGO_ENABLED: "0", GOOS: "{os}", GOARCH: "{arch}"}
platforms: [linux/amd64, linux/arm64, darwin/amd64, darwin/arm64]
files: [LICENSE, NOTICE]
```

A key the table doesn't name is refused, and so is a second YAML document
in the file.

| Key | Type | Default | What it does |
|---|---|---|---|
| `name` | string | (required) | The plugin's name, the one its `describe` answer gives: lower-case letters, digits and dashes. `pipelinegen plugin add --zip` names the entry by it unless `--name` says otherwise. |
| `kind` | string | (required) | The plugin's kind. pipelinegen runs `sbom`, `correlator`, `runner`, `driver` and `connector`, and refuses a pin whose entry names another kind than the manifest. Other hosts may know other kinds. |
| `program` | file name | (required, but with `platforms: [any]`) | The program the build writes into `{out}`: `{out}/<program>` for a binary, or `{out}/<program>.mjs` for a Node plugin. Without the extension. A plugin built for `[any]` may leave it out when it holds assets alone; pipelinegen refuses such a plugin, since it runs every plugin as a program. |
| `build.image` | image | (required) | The image the build runs in, pinned by digest: `name[:tag]@sha256:<64 hex>`. An image by tag alone is refused. docker or podman pulls it when the machine lacks it. |
| `build.run` | list | (required) | The command, as a list of arguments, run with the plugin's directory as the working directory. The image's entrypoint is not used. |
| `build.env` | mapping | none | Variables for the build. The build gets the image's own environment, these, `HOME=/tmp` and `GOFLAGS=-trimpath`, and nothing of the machine's; a `GOFLAGS` here is added after `-trimpath`. |
| `build.network` | boolean | `false` | Gives the build a network. Without one, a Go plugin with dependencies needs them vendored, and a Node plugin its packages in the tree. |
| `platforms` | list | (required) | The platforms to build for, each `os/arch` (`linux/amd64`, `darwin/arm64`), or `[any]` alone for one build that runs everywhere: a Node script, or assets. |
| `files` | list of paths | none | Files of the plugin's directory copied into the zip beside the program, under their base names: `LICENSE`, `NOTICE`. A path may not leave the directory, and a file that is a symlink is refused. |
| `assets` | list of directories | none | Directories the build writes under `{out}` (`{out}/dist`), kept in the zip and the cache under the same relative paths beside `manifest.yaml`. None of their files may be executable or a symlink. |

## Placeholders

`build.run` and the values of `build.env` may hold three placeholders,
filled in for each platform's build:

| Placeholder | Value |
|---|---|
| `{out}` | The directory the build writes the program and assets into. |
| `{os}` | The platform's operating system, `linux` or `darwin`. |
| `{arch}` | The platform's architecture, `amd64` or `arm64`. |

`{os}` and `{arch}` have no value for `platforms: [any]`, and a manifest
that uses them there is refused, as is any other `{...}`.

## How a build runs

Each platform is built in a container of `build.image`, with docker or
podman, whichever is on `PATH` first. The repository is mounted read-only,
`{out}` is the only directory the build can write, and the build runs as
your user, so what it writes is yours. It has no network unless
`build.network` is `true`. After it ends, pipelinegen takes
`{out}/<program>` (named `<program>-<os>-<arch>` in the zip and the cache)
or `{out}/<program>.mjs`, which is the same on every platform, so a Node
plugin is built once whatever the platforms. It then copies the `assets`
directories and the `files`. Anything else the build writes is dropped.

A build for a `commit` pin builds the files of that commit alone, with no
`.git`, for the platform of the machine that asks. It is kept in the user
cache under a key of the repository, the commit, the path, the manifest's
SHA-256, the platform and the image's digest, so a machine builds each
once.

## The zip

A plugin's zip holds, at its top level and with no directory around them:

- `manifest.yaml`, byte for byte the one in the plugin's directory;
- the program for each platform built, `<program>-<os>-<arch>`, or one
  `<program>.mjs`;
- the `files`, under their base names;
- the `assets` directories, under their own paths.

One zip holds every platform, so one pin serves a Linux worker and a Mac
alike. Its entries are sorted and carry a fixed time, so the same build
packs to the same bytes and the same sha256. A zip is refused when its
`manifest.yaml` isn't at the top level, when an entry is a symlink, leaves
the zip's directory (`..`, an absolute path) or is no part of the plugin,
when anything but the program is executable, or when it unpacks to more
than 1 GiB or holds more than 20,000 entries.
