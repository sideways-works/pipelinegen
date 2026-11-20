# Bazel and the plugins side by side

pipelinegen answers "which tests does this change reach" by joining two
answers. This page puts them next to each other.

| Question | Who answers | How |
|---|---|---|
| Which files make up the repository, grouped into targets? | Bazel | the BUILD files |
| Which targets changed between base and head? | bazel-diff | hashes every target with its inputs at both commits |
| What does each target depend on? | Bazel | the build graph |
| Which units (functions, tables, operations, modules) exist, and with which digests? | sbom plugins | read the source files |
| Which unit uses which? | correlators | read the source files (type-check, parse SQL, read fetch calls) |
| Which units did each test execute? | correlators | read the coverage the test left behind |
| Which units changed? | pipelinegen | compares digests at base and head |
| Which slices does a change reach? | pipelinegen | walks the links back from the changed units to the slices' roots, and adds the slices whose owner's data changed in Bazel's answer |
| Which pipeline runs them? | the runner plugin | writes the CI system's pipeline from the config |
| How does each slice run? | the suite's drivers, or its own task files | runs the test tool on the batch's slices and writes a result per slice |
| Where are results shown? | the forge | statuses and a comment on the git host |

A slice of a recorded suite runs when either side reaches it: the walk
over units, or bazel-diff for the files of its owner that no plugin
describes (a fixture, an image). A slice of a graph suite runs when its
owner target changed in bazel-diff's answer, or one of its own files did.
A tagged slice (lint, say) is selected by bazel-diff alone, when its
target changed.

## Why both

Each side covers what the other can't. Bazel sees every file, including
those no plugin understands, but only at the coarseness of targets, and
nothing that happens at run time. The plugins see function by function and
what each test really ran, but only in the languages they know. Joined,
the answer is as fine as the plugins allow and never blind to a file.

## What each side needs from you

For Bazel: a workspace whose targets cover your files, so every file
belongs to some target. `pipelinegen check` tells you which files don't.

For the plugins: an entry per plugin in `pipelinegen.yaml`, and for a
recorded suite a CI task that turns coverage on and leaves the files where
the correlators look. Nothing else: the plugins find their own files.

Next: [runs and the pipeline](runs-and-the-pipeline.md).
