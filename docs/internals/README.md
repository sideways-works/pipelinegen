# Internals

For people changing pipelinegen. The design and its reasons are in
[design.md](../design.md), the interfaces between the tool, its parts and a
repository in [contracts.md](../contracts.md); these pages say how the code
carries them out and link to those two for the details.

## The packages and who calls whom

```mermaid
flowchart TD
  main["cmd/pipelinegen"] --> cli["internal/cli<br/>commands and flags"]
  cli --> config["internal/config<br/>pipelinegen.yaml"]
  cli --> affected["internal/affected<br/>the plan"]
  cli --> plugins["internal/plugins<br/>sbom and correlator calls"]
  cli --> report["internal/report<br/>run state, statuses, comment, sweep"]
  cli --> lists["internal/lists<br/>lists, marks, attach"]
  cli --> lock["internal/lock<br/>leases between repositories"]
  cli --> check["internal/check<br/>files no slice covers"]
  cli --> spec["internal/spec<br/>the runner's input"]
  cli --> bazeldiff["internal/bazeldiff<br/>runs and fetches bazel-diff"]
  cli --> depsrepo["internal/depsrepo<br/>the stub an older setup's MODULE.bazel names"]
  affected --> bazel["internal/bazel<br/>Bazel queries"]
  affected --> gitx["internal/gitx<br/>local git"]
  affected --> sample["internal/sample<br/>the sample of a suite"]
  bazeldiff -. "implements affected.Differ" .-> affected
  plugins --> plugincache["internal/plugincache<br/>answers by content"]
  plugins --> proto["plugin<br/>the protocol"]
  depsrepo --> deps["internal/deps<br/>the deps tree"]
  check --> buildfile["internal/buildfile<br/>BUILD files without Bazel"]
  lists --> gitstore["internal/gitstore<br/>refs as compare-and-swap cells"]
  report --> gitstore
  lock --> gitstore
  plugincache --> gitstore
  report --> github[("GitHub API")]
  gitstore --> remote[("the repository's refs")]
  cli -. "pipeline" .-> runner["the runner plugin<br/>the generated pipeline"]
```

`internal/batch` deals selected slices into batches and
`internal/durations` keeps the seconds per test file they are dealt by.
`internal/gotests` reads a Go test file's test names for the test-file
slices, `internal/recording` is a slice's recording and its marks,
`internal/runs` reads a build's change and applies pull request filters,
`internal/attached` names the runs whose recordings went into a commit's
lists, `internal/secret` masks the token in every error, and
`internal/kit` is the conformance kit behind `pipelinegen plugin test`.
`internal/testrepo` and `internal/bazeldifftest` serve tests;
`internal/example` tests the example repository;
`internal/docscheck` keeps these docs in step with the code.

`affected` defines the `Differ` interface it needs from bazel-diff, and
`bazeldiff` implements it, so planning can be tested with a fake and with
the real binary alike.

## Plugins

What knows a language, a package ecosystem or a CI system lives in plugins
([plugins.md](../plugins.md)). `internal/plugins` describes the configured
sbom and correlator plugins, has them claim their files, asks for units,
links through the content cache, and assembles the graph of each tree it
plans, which `internal/graph` walks; at slice ends, the same package calls the
correlators' `collect` and `observe`. The pipeline comes from the runner
plugin through `internal/spec`. The core keeps the config, Bazel and
bazel-diff, the stored graph and the walk over it, the narrowing levels and
the sample, plans, refs, reports and locks, and the commands a
pipeline's steps call.

## The pages

- [Planning](planning.md): from two commits to a plan, step by step.
- [Binding by recording](binding.md): recordings, unbound units and
  widths; the deps repository it describes is how a build with the
  `pipelinegen_forward` tag binds slices.
- [The stored graph and the outward walk](graph.md): how planning walks
  outward from what changed.
- [The generated pipeline](pipeline.md): the jobs, the gate, batches,
  reruns, the bootstrap.
- [Refs](refs.md): the layout under `refs/pipelinegen/` and its guarantees.
- [The lock](lock.md): slots, leases, takeover.
- [The reporter](reporter.md): states, statuses, the comment, the sweep.
- [Batches](batches.md): dealing by duration, the durations file.
- [Safety](safety.md): why a change can't be missed, and the known limits.
- [Concourse](concourse.md): the facts the design rests on, and how they
  were proven.
- [Testing and releases](testing-and-releases.md).
