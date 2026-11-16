# Estimate what you would save

Before you set up a pipeline, you can ask how much pipelinegen would have
left out of your recent history: `pipelinegen try` plans each of your last
commits against its parent, under the config you have now, as if
pipelinegen had run all along. All it needs is the first config ([step
3](../tutorial/first-config.md)) and a passing check ([step
4](../tutorial/check.md)), on your machine; the config doesn't have to be
committed, and the commits can be older than it.

## Plan past commits

From the repository's root, on your main branch:

```sh
pipelinegen try --last 50
```

On the notes service, over its last four commits (an edit to the API's
validation, one to `Words`, one to the page, and one to a Go test):

<!-- output: try -->
```
$ pipelinegen try --last 4
154381da674e  e2e 0/2  go 1/5  notes/notes_test.go: its test file changed: notes/notes_test.go
e12b18fa2252  e2e 2/2  go 0/5  e2e/create.spec.ts: the target //web:index.html changed, through //web:web → //e2e:e2e
8390548f14b8  e2e 0/2  go 4/5  api/create_test.go: the target //text:text.go changed, through //text:text → //api:api → //api:api_test
b6a6da333fec  e2e 0/2  go 2/5  api/create_test.go: the target //api:api.go changed, through //api:api → //api:api_test
total: 4 commits, e2e median 0% worst 100%, go median 20% worst 80%, 0 ran everything
```

Each line names a commit, how many slices of each suite it would have run,
and the reason of one selected slice. The last line sums it up: three of
the four commits would have run no browser spec at all, and the worst one
ran both. `pipelinegen affected --same-config --base <commit>^ --head
<commit>` plans one of those commits again with every reason, under the
same config.

## What the estimate says, and doesn't

The estimate uses what pipelinegen knows before anything is recorded: the
Bazel graph, and recorded suites bound to everything. Recorded suites
select less once CI has recorded them, so the real saving is larger. On
the other hand the estimate counts slices, not minutes: a browser test
file that takes two minutes counts the same as a unit test that takes a
second. Weigh the count by your suites' durations if you keep them.

Planning a commit takes as long as one plan in CI (Bazel starts, hashes
both commits), so fifty commits on a large repository take a while.
