# Concepts

This part explains how pipelinegen thinks, one idea per page, each
introduced before the next page uses it. You don't need any of it to
follow the [step-by-step guide](../tutorial/README.md), but when the guide
uses a word you don't know, it links back here.

## The running example

Every page uses the same small repository: a notes service in Go, in
[example-notes/](../../example-notes/) next to this book. It stores notes
in memory, finds them by the words in them, and serves both over HTTP to a
small web page.

```
example-notes/
├── api/                the HTTP routes: create a note, list or search notes
│   ├── api.go
│   ├── create_test.go
│   └── search_test.go
├── notes/              the store: add, get and list notes
│   ├── notes.go
│   └── notes_test.go
├── search/             finds the notes that hold every word of a query
│   ├── search.go
│   └── search_test.go
├── text/               splits text into words, tidies titles
│   ├── text.go
│   └── text_test.go
├── web/index.html      the page: a form to add a note, a search box
├── cmd/notes/main.go   starts the server, which serves the page and the API
├── e2e/                browser tests of the page, run with Playwright
│   ├── create.spec.ts
│   └── search.spec.ts
├── ci/                 the CI tasks that run the tests
├── MODULE.bazel, BUILD.bazel files
└── pipelinegen.yaml
```

The Go code calls down the list: `api` uses `notes`, `search` and `text`;
`search` uses `notes` and `text`. There are seven test files: five Go test
files, and two browser specs that open the page and click through it while
the page calls the API.

The question the book keeps coming back to: when someone changes the
backend, which browser specs should run? No file under `e2e/` or `web/`
changed, yet a spec that uses the changed code can now fail.

## The pages

1. [Changes and tests](changes-and-tests.md): what a change is, and what
   it means for a change to reach a test.
2. [What Bazel does](bazel.md): targets, the build graph and bazel-diff,
   and what the graph can't see.
3. [Suites, slices and batches](suites-and-slices.md): how test files
   become the units pipelinegen selects and runs.
4. [Drivers: how a batch runs](drivers.md): the suite's steps, the
   drivers that run its slices, and bundling.
5. [Units and sbom plugins](units-and-sbom.md): what exists in the code.
6. [Links and correlators](links-and-correlators.md): how those units
   connect.
7. [Recordings and lists](recordings-and-lists.md): what each test ran.
8. [The graph and the walk](the-graph-and-the-walk.md): how a change
   selects tests.
9. [Narrowing](narrowing.md): when to run fewer tests than the strict
   answer, and when to run all of them.
10. [Bazel and the plugins side by side](bazel-and-plugins.md): who
    answers which question.
11. [Runs and the pipeline](runs-and-the-pipeline.md): runs, the runner,
    the generated pipeline and how it reports.
12. [Forges](forges.md): where the code lives.
13. [The store](the-store.md): where pipelinegen keeps its records.
14. [The nightly](the-nightly.md): the run that checks everything.

The [glossary](../glossary.md) has every term in one place.
