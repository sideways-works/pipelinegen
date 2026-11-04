# Step 13: upgrading

pipelinegen and each plugin are pinned in `pipelinegen.yaml` by version
and checksum, so nothing changes until you change a pin.

## pipelinegen

1. Read the [changelog](../../CHANGELOG.md) from your version to the new
   one. Before 1.0.0 a minor version can change the config.
2. Download the new release on your machine as in [step 2](install.md) and
   run `pipelinegen check`. A line the new release no longer needs shows
   as a warning with its fix; a config key that went away is an error that
   names it.
3. Change `concourse.planner.bootstrap.release` to the new version and the
   checksum of `pipelinegen-linux-amd64` from its `SHA256SUMS`.
4. Generate the pipeline again and set it (step 9). The `pipeline` job
   fails until you do.

Release candidates (`vX.Y.Z-rc.N`) are published as pre-releases. Pin one
to try a release before it is final; never pin one by accident, since the
releases page doesn't list them as the latest.

## A plugin

Change its `release` (version and checksum) and download it into
`tools/plugins/` for your machine. A plugin's entry is part of what every
slice it binds depends on: an sbom's, of the slices that bind a unit it
lists; a correlator's, of the recorded slices of the suites it observes
and of the slices that bind what it answered. From narrowing level 4 the
change runs those slices again, with the reason "plugin `<name>`
changed"; below it, each suite they belong to runs its sample, and the
plan names the rest under `plugin-entry` (see
[narrowing](../how-to/narrowing.md)). With `pipelinegen.yaml` in
`everything_on`, it runs every slice. Its cached answers are kept per
release, so the first plan after the upgrade asks the new release about
every file.

## Setup lines planning no longer needs

A repository may still hold setup lines from planning through a hidden
Bazel repository: a `pipelinegen_deps` repository in `MODULE.bazel`, and
`.pipelinegen/` in `.gitignore` and `.bazelignore`. They do nothing now.
`pipelinegen check` warns about each, says it will be refused, and names
the line to remove. See [fresh clones](../how-to/fresh-clones.md).

That is the last step. The [how-to guides](../how-to/README.md) cover what
comes up later: adding a test or a suite, tuning narrowing, sharing
machines between repositories, secrets, keeping history.
