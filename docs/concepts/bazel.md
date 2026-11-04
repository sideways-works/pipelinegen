# What Bazel does

[Bazel](https://bazel.build) is a build system. pipelinegen doesn't need
you to build or test with it; it needs Bazel's description of the
repository, the build graph, because that is a precise, machine-readable
answer to "which files belong together".

## Targets and BUILD files

A Bazel workspace has a `MODULE.bazel` file at its root and a
`BUILD.bazel` file in each directory that declares something. Each thing a
BUILD file declares is a target, named by a label: `//text:text` is the
target `text` in the directory `text`. A target lists its source files and
the other targets it depends on.

In the notes service,
[Gazelle](https://github.com/bazel-contrib/bazel-gazelle) writes the BUILD
files from the Go code. `api/BUILD.bazel` holds two targets:

```starlark
go_library(
    name = "api",
    srcs = ["api.go"],
    importpath = "example.com/notes/api",
    deps = ["//notes", "//search", "//text"],
)

go_test(
    name = "api_test",
    srcs = ["create_test.go", "search_test.go"],
    embed = [":api"],
    deps = ["//notes"],
)
```

Together the targets form a graph: `//api:api_test` depends on
`//api:api`, which depends on `//search`, which depends on `//text`.

## bazel-diff

[bazel-diff](https://github.com/Tinder/bazel-diff) takes the graph at two
commits, hashes every target with everything it depends on, and lists the
targets whose hash changed. pipelinegen downloads a pinned release of it
on first use and runs it for every plan. For the edit to `Words`, the
changed targets are `//text:text` and the targets that depend on it, such
as `//search:search` and `//api:api_test`.

A test file's owner is the target that says what the file depends on. In a
Go repository built by Gazelle that is the `go_test` target holding the
file. The browser specs have a target of their own,
[`//e2e:e2e`](../../example-notes/e2e/BUILD.bazel), which names what they
read: the page, the seed notes and the Playwright config. A suite can name
the owner by a pattern ([`owner`](../reference/config.md#suites)). With
the graph alone, a test file runs when its owner changed. For the edit to
`Words` that is four of the five Go test files, and no browser spec:

<!-- output: affected-words -->
```
$ pipelinegen affected --base HEAD~1
e2e: 0 selected, 2 untouched
go: 4 selected, 1 untouched
  run api/create_test.go
  run api/search_test.go
  run search/search_test.go
  run text/text_test.go
```

## What the graph can't see

The graph works per target, and a target is usually a whole package. Both
API tests live in `//api:api_test`, so both run, although
`api/create_test.go` never searches and never calls `Words`.

The browser specs show the bigger gap. `e2e/search.spec.ts` types a word
into the search box, the page asks the server, and the server runs
`Words`. If `Words` breaks, that spec breaks. But the request crosses the
network, not a build edge: the specs' target holds the page, and the page
doesn't depend on any Go package. So by the graph a backend change runs no
browser test at all, which is what the plan above shows. The only fix the
graph allows is to make the specs depend on the whole server, and then
every backend change runs every browser test.

What's missing is a finer answer: which backend code did each browser test
actually reach? That is what the plugins give, step by step over the next
four pages: [units](units-and-sbom.md), [links](links-and-correlators.md),
[recordings](recordings-and-lists.md) and [the
walk](the-graph-and-the-walk.md).

The graph still matters with plugins. Files that no plugin understands (a
fixture a test reads, the page, a config file) reach tests through their
targets, and bazel-diff is what says that they changed. A change to the
page runs both browser specs that way, and no Go test.

Next: [suites, slices and batches](suites-and-slices.md).
