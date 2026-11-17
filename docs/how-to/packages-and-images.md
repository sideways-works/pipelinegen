# Packages and cached images

How to declare the packages a suite's steps need, so CI and your machine
install them in the image each step runs in, and how to keep an image
with them already installed so nothing is installed twice. After this
page you can write the `packages` and `images` keys, say how each suite
gets its packages on CI, and read what `check` says about it.

## Declare the packages

List them once for every suite, and add per suite:

```yaml
packages: [git, jq]
suites:
  backend:
    image: golang
    packages:
      - {name: postgresql-client, apk: postgresql16-client}
    steps:
      - run: go
concourse:
  planner:
    packages: [curl]
```

- `packages` at the top applies to every suite.
- `suites.<name>.packages` adds to it for that suite.
- `concourse.planner.packages` is for pipelinegen's own steps, which run
  in the planner image.

A name is the package's name in the image's package manager. Where the
names differ, an entry is a mapping by family: `apt` (apt-get), `apk`, and
`rpm` (dnf, microdnf and yum, which install from the same repositories).
Its `name` serves every family it doesn't name. An entry that names no
package for the image's family fails the step, naming the entry. A name
is letters, digits and `+ . _ -`, nothing else: it goes into a shell
script as it is.

## What a step does before its command

Before a step's command, an install script looks for the first of
apt-get, apk, dnf, microdnf and yum in the image, asks it which of the
packages are already installed, and installs the rest. When every package
is there it installs nothing and asks no mirror. A step that runs as
another user than root and lacks a package fails, naming it. On CI the
manager keeps what it downloads in the task's cache (`pkgcache`), so each
worker downloads a package once.

On CI the install goes in front of every task the runner writes: the
tasks of a suite run by [drivers](../concepts/drivers.md) (`slice run`,
`slice prepare`) install the suite's packages, and pipelinegen's own steps
install the planner's. A suite's task files (`prepare`, `run_slice`,
`whole_suite`) are the repository's own scripts, so nothing can go in
front of them: only a cached image brings them the packages. Without one
they run without them, and `check` warns.

## Cached images

With a registry named, the pipeline builds each suite's image once with
the packages installed, and the suite's tasks run in it:

```yaml
images:
  registry: ghcr.io/owner/ci-images
  credentials: registry_push
  pull_credentials: registry_pull
```

An image is tagged with its key, the hash of three things: the base
image's digest, the sorted packages, and the install script. The same
packages in another order give the same key; another base digest, another
package or another way of installing gives another. Two suites with the
same base and packages share one image.

A cached image needs its base pinned by digest: the suite's `image` names
a `registry-image` resource of the root pipeline file whose `version`
names a digest. A tag can move, and an image keyed by a tag could hold
another base than the one the tasks would get. A suite whose image is a
tag installs per task instead, and `check` says so.

```yaml
# ci/root.yml, the file concourse.include names
resources:
  - name: golang
    type: registry-image
    source: {repository: golang, tag: "1.27"}
    version: {digest: "sha256:..."}
```

The Concourse runner adds one job per image, `image-<key>`. When the
config changes, it asks the registry for the tag. When the registry has
it, the job is done. When not, the job builds the image from the pinned
base with the install script (in `concourse/oci-build-task`, which needs
`privileged: true` but no Docker daemon) and pushes it. The job takes
nothing from the repository but the config's version, and it is the only
step that holds `images.credentials`. Two builds that race to push one
tag both push the same packages on the same base, and the later push's
image keeps the tag. Neither waits for the other.

A suite's job fetches its image in a task at the start of its work, never
as a `get`. A get is an input of the job, and a job would wait for an
image the registry hasn't got yet. When the registry hasn't got the image,
or can't be reached, the task fetches the pinned base instead, and every
task the runner writes installs the packages as it would without a
registry. The tasks then install nothing in a cached image.

The planner image is never swapped for a cached one. pipelinegen's own
steps hold the tokens that write, and a cached image is pulled by a tag
that whoever can push may move.

## Credentials

`credentials` and `pull_credentials` name vars, never values, holding a
`username` and a `password`:

```yaml
# a vars file for fly set-pipeline -l
registry_push: {username: ci-bot, password: ...}
registry_pull: {username: ci-reader, password: ...}
```

- `credentials` reaches the image jobs' resources and steps, and nothing
  else.
- `pull_credentials` reaches the step that fetches a suite's cached
  image, which runs no code of the repository's.
- Neither reaches a task that runs the repository's code, in any run. A
  pull request's jobs never hold the var that pushes.

Leave `pull_credentials` out for a registry that lets anyone pull; it may
not be `credentials`. See [secrets](secrets.md#registry-credentials).

## What `check` says

`check` notes, for each suite with packages, how its steps get them:

```text
note: pipelinegen.yaml: suite backend's packages (git, jq, postgresql-client) in golang come in a cached image, ghcr.io/owner/ci-images:3f1c0a9e2b7d…, built once on golang@sha256:...
note: pipelinegen.yaml: suite e2e's packages (git, jq) in node are installed by each task, with a download cache: node is a tag, which can move: pin its version by digest in ci/root.yml for a cached image
warning: pipelinegen.yaml: suite lint runs task files (prepare, run_slice or whole_suite), whose scripts the runner can't put an install in front of, and has no cached image: ...
```

A runner that installs no packages still writes a working pipeline,
without the installs. `pipelinegen pipeline` and `check` say so, and name
the runner's version.

## On your machine

`pipelinegen run` and `record` run a suite's steps in its image. With
packages declared they first make the image with them installed, once:
a container of the base installs them as root and is committed to the
local image store as `pipelinegen-cache/<key>`. Its key is of the base's
image ID, so a base pulled again with other content gets an image of its
own. Later runs reuse it, and the install before each step finds
everything there. No registry is needed. `docker image rm` or
`podman image rm` of a `pipelinegen-cache/...` image removes it.

With `--no-image` the steps run on your machine and nothing is installed:
pipelinegen names the packages it finds under no command on your `PATH`
and in no package database it knows (dpkg, rpm, apk, pacman, brew).

## A changed package list

Packages change what a test observes, so a change to a suite's packages
(or the top level's) runs the suite's slices. The registry and its
credentials change nothing a test sees. On CI a change to the packages
takes effect once the pipeline is set from the branch that has it, as
for any change to what the pipeline is made from: a pull request that adds
a package runs with the packages of the pipeline that is set.
