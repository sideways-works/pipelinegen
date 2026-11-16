# Step 5: the first plan

`pipelinegen affected` plans a change on your machine: it compares two
commits and prints which slices the change reaches. It is the same
planning the CI pipeline does, so you can try pipelinegen before any
pipeline exists.

## Plan a change

Make a change and commit it. In the notes service, an edit to `Words` in
`text/text.go` keeps hyphenated words whole. Then ask what the commit
reaches compared with its parent:

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

`--base` names the commit to compare with; the head is your checkout's
`HEAD` unless `--head` names another.

**You should see** each suite with the number of slices it selected and
left untouched, and a `run` line per selected slice. The first plan takes
longer than the next ones: pipelinegen downloads bazel-diff, and Bazel
starts and loads the workspace.

Four of the five Go test files run, because the suites are bound by the
Bazel graph and `//text:text` is part of what both the search and the API
test targets are built from ([what Bazel does](../concepts/bazel.md)). No
browser spec runs, although `e2e/search.spec.ts` searches and so reaches
`Words` through the server: the graph can't see that. Step 6 fixes both:
the three Go test files and the one spec that ran `Words`.

## Plan a change to the build setup

A change to a file in `everything_on` runs everything, and says why:

<!-- output: affected-gomod -->
```
$ pipelinegen affected --base HEAD~1
every slice: go.mod changed (everything_on go.mod)
e2e: 2 selected, 0 untouched
  run e2e/create.spec.ts
  run e2e/search.spec.ts
go: 5 selected, 0 untouched
  run api/create_test.go
  run api/search_test.go
  run notes/notes_test.go
  run search/search_test.go
  run text/text_test.go
```

## Ask why

`pipelinegen why` explains one slice: why it was selected, or why it was
left out.

<!-- output: why-words-create -->
```
$ pipelinegen why --base HEAD~1 --slice api/create_test.go
go api/create_test.go was selected from a38bd0273694 to 7615acc58598:
  the target //text:text.go changed, through //text:text → //api:api → //api:api_test
```

`api/create_test.go` runs because `//text:text.go`, the changed file,
is a source of `//text:text`, and its target is built from that through
`//api:api`. `--json` on `affected` prints the whole plan with
every reason, for scripts.

**If you don't see what you expect:**

- `every slice: <file> changed and no Bazel target owns it`: a file
  outside every Bazel target changed. Give it a target, or decide in [the
  check](check.md) that it is untested.
- `every slice: <file> changed and no Bazel target owns it: it is in no
  target yet (run gazelle)`, after you added a test file: run Gazelle
  again (`bazel run //:gazelle`) and commit the BUILD files. Until then
  the plan runs everything to be safe:

<!-- output: affected-no-target -->
```
$ pipelinegen affected --base HEAD~1
every slice: text/title_test.go changed and no Bazel target owns it: it is in no target yet (run gazelle)
e2e: 2 selected, 0 untouched
  run e2e/create.spec.ts
  run e2e/search.spec.ts
go: 6 selected, 0 untouched
  run api/create_test.go
  run api/search_test.go
  run notes/notes_test.go
  run search/search_test.go
  run text/text_test.go
  run text/title_test.go
```
- `the base ... is unknown`: the base commit isn't in your checkout. Fetch
  it, or name one that is.

Next: [step 6, plugins for your language](plugins.md).
