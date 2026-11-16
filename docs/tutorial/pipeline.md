# Step 9: the pipeline

This step generates the CI pipeline and sets it on Concourse. It is the
first step that needs the git host and the CI system: GitHub and Concourse
today ([what works today](../introduction/what-works-today.md)).

## What you need

- A Concourse you can set pipelines on, and the `fly` command logged in to
  it (`fly -t ci login -c https://concourse.example.com`). Concourse's own
  [docs](https://concourse-ci.org/docs.html) explain both.
- On GitHub, a fine-grained token for the repository with contents read
  and write (pipelinegen writes its records as hidden refs), commit
  statuses read and write, and pull requests read and write (for the
  comment). Plugins or a pipelinegen release in private repositories need
  contents read on those too. The [secrets guide](../how-to/secrets.md)
  has the details.

This step uses one token for everything, which keeps it short. With a run
that takes pull requests, as `pr` below does, the planning step of a pull
request then holds a token that can write, so `pipelinegen check` and the
plan warn that it will be refused. Give each purpose its own token, the
read one a token that only reads, as [the secrets
guide](../how-to/secrets.md#where-it-never-goes) shows.

## The rest of the config

Add the git host, the CI system, the images the suites run in, the runner
and the runs to `pipelinegen.yaml`. For the notes service, on GitHub as
`owner/notes`:

```yaml
github:
  repository: owner/notes
  token: github_token           # the name of a Concourse var, never the token

concourse:
  url: https://concourse.example.com
  team: main
  repository:
    uri: https://github.com/owner/notes.git
    username: x-access-token
    password: ((github_token))
  # the images the suites' tasks run in
  include: ci/root.yml
  # pipelinegen's own steps run on a stock image and fetch the tool and
  # bazelisk, each checked against its checksum
  planner:
    image: {repository: golang, tag: 1.27.1}
    digest: sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2
    prepare: go mod download
    bootstrap:
      release: {version: v0.2.0, sha256: c5f2dce22a0b3361d66e1bc390560928652fae1f94443be9784c059f44f2e04a}
      bazelisk: {version: v1.30.0, sha256: a248ae2b2b62b5baebb748f6fba7203bff1a27e5d01b55399a8406744cb539e8}

plugins:
  # ... the plugins of step 6 and the drivers of step 3 ...
  - name: concourse
    kind: runner
    repository: sideways-works/pipelinegen-runner-concourse
    release: {version: v0.2.0, sha256: e4295cfc26d59414586d4840b2f4f69609400248b8a00724771ada332dde5b0a}

suites:
  go:
    # ...
    image: golang
  e2e:
    # ...
    image: playwright
    steps:
      - prepare: command
        image: golang
        # ...

runs:
  pr:
    trigger:
      pull_requests: {base: main, draft: false}
    compare: merged_target
    select: affected
    suites: {go: {}, e2e: {}}
    record: true
    report:
      comment: {format: list}
  main:
    trigger: {branch: main}
    compare: last_passed
    select: affected
    suites: {go: {}, e2e: {}}
    record: true
  nightly:
    trigger:
      timer: {start: "3:00", stop: "4:00", location: Europe/Berlin}
      branch: main
    compare: none
    select: all
    suites: {go: {}, e2e: {}}
    record: true
```

The whole file is
[example-notes/pipelinegen.yaml](../../example-notes/pipelinegen.yaml).
Each part, briefly:

- `github` names the repository and the Concourse var that holds the
  token.
- `concourse.planner` is where pipelinegen's own steps run. `image` and
  `digest` pin a stock image; the steps download pipelinegen and bazelisk
  into it, each checked against its checksum (`release.sha256` is the
  `pipelinegen-linux-amd64` line of the release's `SHA256SUMS`). `prepare`
  fills Go's module cache before planning, because plugins never use the
  network.
- `concourse.include` names a pipeline file of your own that the runner
  merges into the generated one; here, the images the suites run in.
- A suite's `image` is the container its tasks run in, and a prepare
  step's `image` the one that step's task runs in: the `e2e` suite's
  specs run in Playwright's image, which has the browsers, and its
  server is built in Go's. Each names a `registry-image` resource of the
  included file, [ci/root.yml](../../example-notes/ci/root.yml):

  ```yaml
  resources:
    - name: golang
      type: registry-image
      source: {repository: golang, tag: 1.27.1}
    - name: playwright
      type: registry-image
      source: {repository: mcr.microsoft.com/playwright, tag: v1.63.0-noble}
  ```

  The Playwright image's tag matches the `@playwright/test` version of
  `package.json`. `ci/**` is under `everything_on` since step 4, so a
  change to the images runs every slice.
- The `concourse` plugin is the runner that writes the pipeline.

To find an image's digest, ask the registry; with Docker:

```
$ docker buildx imagetools inspect golang:1.27.1
Name:      docker.io/library/golang:1.27.1
MediaType: application/vnd.oci.image.index.v1+json
Digest:    sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2
...
```

`crane digest golang:1.27.1` or `skopeo inspect` give the same.
- The three runs are those of [runs and the
  pipeline](../concepts/runs-and-the-pipeline.md): pull requests, each
  commit on `main`, and the nightly.

### Decision: what the planner image needs

The planner image must hold what the plugins need to run:

- Go plugins need the `go` command, at least as new as your `go.mod`'s
  `go` line: the `golang` image of that version.
- TypeScript plugins (ts-sbom, react-correlator) are Node programs and
  need Node 22.18 or newer: without it every plan stops with "plugin
  failed: ts describe: exit status 127: env: 'node': No such file or
  directory", and the check doesn't foresee it. Use an image with both Go
  and Node, named with its digest, or have `prepare` unpack a pinned Node
  release into the image before the plan, as [the worked
  example](../worked-example/README.md#step-10-the-runs-and-the-pipeline-github-and-concourse)
  does.
- Private Go modules: give `prepare` a token through
  `concourse.planner.secrets` and `git_token`, and set `GOPRIVATE` in
  `concourse.planner.params`; plugins can't fetch modules themselves.

## Generate it

Download the runner the config pins into `tools/plugins/concourse`, as in
step 6:

```sh
curl -fsSL -o tools/plugins/concourse \
  https://github.com/sideways-works/pipelinegen-runner-concourse/releases/download/v0.2.0/concourse-runner-linux-amd64
echo "e4295cfc26d59414586d4840b2f4f69609400248b8a00724771ada332dde5b0a  tools/plugins/concourse" | sha256sum -c -
chmod +x tools/plugins/concourse
```

Then write the pipeline, and list the vars it needs:

<!-- output: pipeline -->
```
$ pipelinegen pipeline --out pipeline.yml && pipelinegen pipeline --vars
github_token
```

**You should see** nothing from the first command, which writes
`pipeline.yml`, and `github_token` as the one var the pipeline needs.
Neither file belongs in the repository: `tools/` is already ignored; add
`pipeline.yml` to `.gitignore` too.

**If you don't:**

- `invalid config: github needs a repository and the name of its token
  var`, or `concourse needs a repository source and a planner image`: once
  a config has `runs`, both sections are required.
- `no runner plugin is configured`: the config names no plugin of kind
  `runner`.
- An error that names `tools/plugins/concourse`: the runner's binary isn't
  there, or isn't executable.

## Set it

```sh
fly -t ci set-pipeline -p notes -c pipeline.yml -v github_token="$GITHUB_TOKEN"
fly -t ci unpause-pipeline -p notes
```

> **Output to come.** The output of setting the pipeline, and the jobs as
> Concourse shows them, will be filled in from a real run.

**You should see** in Concourse one job per run for planning (`plan-pr`,
`plan-main`, `plan-nightly`), one per run and suite (`pr-go`, `pr-e2e`,
`main-go` and so on), and `pipeline`, which watches the config. When
`pipelinegen.yaml` changes on `main`, `pipeline` fails until you set the
pipeline again, and its log names the command.

Next: [step 10, the first run](first-run.md).
