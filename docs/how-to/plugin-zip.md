# Build and host a plugin as a zip

A plugin doesn't need a GitHub release. Give its directory a
`manifest.yaml`, build it into a zip with `pipelinegen plugin build`, put
the zip anywhere that serves https, and pin it by its URL and sha256. Or
pin a commit of its repository, and every machine that runs the plugin
builds it from the same manifest. The [manifest reference](../reference/manifest.md)
has every key; this page is the steps.

## 1. Write the manifest

In the plugin's directory, `driver/shell` of a repository of several say:

```yaml
name: shell-driver
kind: driver
program: shell-driver
build:
  image: golang:1.27.1@sha256:<64 hex>
  run: [go, build, -o, "{out}/shell-driver", ./cmd/shell-driver]
  env: {CGO_ENABLED: "0", GOOS: "{os}", GOARCH: "{arch}"}
platforms: [linux/amd64, linux/arm64, darwin/amd64, darwin/arm64]
files: [LICENSE, NOTICE]
```

Pin the image by its digest, which `docker buildx imagetools inspect
golang:1.27.1` prints. The build has no network: vendor a Go plugin's
dependencies (`go mod vendor`), or set `build.network: true`. A Node
plugin's `run` writes `{out}/<program>.mjs`, one script for every
platform.

## 2. Build the zip

```sh
pipelinegen plugin build driver/shell --zip dist/shell-driver.zip
```

builds the plugin for each of its platforms in the image, with docker or
podman, and packs it into `dist/shell-driver.zip`. It writes
`dist/SHA256SUMS` beside it and prints the zip's sha256:

```
built shell-driver (driver) into dist/shell-driver.zip, sha256 <sha256>; dist/SHA256SUMS lists it
pin it with: zip: {url: <where you host shell-driver.zip>, sha256: <sha256>}
```

`--platform linux/amd64` builds that platform alone, and may be given
again for more. The same commit builds to the same zip, so anyone can
build it again and compare the sha256.

## 3. Host it

Upload the zip wherever the machines that run the plugin can download it
over https: object storage, a web server, a release of any forge. The URL
must stay: the pin names it, and a machine that hasn't cached the zip yet
downloads it from there.

## 4. Pin it

```sh
pipelinegen plugin add --zip https://plugins.example.com/shell-driver.zip --sha256 <sha256>
```

downloads the zip, checks it against `--sha256`, unpacks it the way a run
does, and adds an entry named and kinded by its manifest:

```yaml
plugins:
  - name: shell-driver
    kind: driver
    zip: {url: 'https://plugins.example.com/shell-driver.zip', sha256: <sha256>}
```

Without `--sha256` the pin takes the sha256 of what was downloaded; give
it whenever you have the one `plugin build` printed. `--name shell` names
the entry otherwise. Each machine downloads the zip once, checks its
sha256 before opening it, and keeps it unpacked in its cache by that
sha256. `pipelinegen plugin upgrade` leaves a zip pin alone: build the new
zip, host it under its own URL, and give the entry the new URL and sha256.

## Pinning a commit instead

```yaml
plugins:
  - {name: shell-driver, kind: driver, repository: https://git.example.com/acme/plugins, path: driver/shell, commit: <40 hex>}
```

builds the plugin from the `manifest.yaml` at that commit, on the machine
that runs it and for its platform, in the same container, and keeps the
build in the cache. git fetches the commit with the credentials it has, so
any git server works. A `tools/plugins/<name>` that is there runs instead,
as it always has; a commit with no manifest, or one git can't fetch, fails
with the commands to build the plugin by hand.

## In a pipeline

The steps of a pipeline run the plugins in `tools/plugins`, which the
runner's bootstrap fills from the spec's `tools`, where a zip pin is a
tool's `zip`, its url and sha256. A runner has to know that field to
fetch a zip-pinned plugin there. On your machine, `affected`, `why`,
`try`, `run`, `slice run`, `pipeline` and `plugins claims` resolve a
plugin that `tools/plugins` lacks themselves, as described above; in a
pipeline `slice run` does, for a suite's drivers, and `pipeline`, for the
runner. A step's `--cache` keeps what they build or unpack under
`<cache>/plugins/`, where `keep.cache` counts it; on your machine it goes
into the user cache.
