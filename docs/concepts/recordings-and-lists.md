# Recordings and lists

## Recording

A recording is the list of units one slice executed in one run. The CI
task runs the slice's tests with coverage turned on and leaves the
coverage files where the correlators look for them. At the end of the
slice, each correlator reads them and answers with unit ids.

For a browser spec, the coverage that matters is the server's. The notes
service's batch task starts the server for each spec with Go's coverage
turned on, runs the spec, and stops the server, which writes down which
functions it ran. When `e2e/search.spec.ts` searches for "Milk", the
server loads the seed notes, serves the page, and answers the search. Its
recording:

<!-- output: lists-show-slice -->
```
$ pipelinegen lists show --slice e2e/search.spec.ts
pipelinegen lists show: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
lists of 5d0f0a819953: its own lists
with the recordings of the live runs:
  local local-<time> 5d0f0a819953 (5 slices)
  local local-<time> 5d0f0a819953 (2 slices)
e2e e2e/search.spec.ts: 9 units
  api:listNotes
  go:api#func:writeJSON
  go:api#method:server.ListNotes
  go:api/notesapi#method:ServerInterfaceWrapper.ListNotes
  go:cmd/notes#func:logRoutes
  go:notes#method:Store.List
  go:search#func:Find
  go:search#func:containsAll
  go:text#func:Words
```

`api:listNotes` is the API operation the search request reached, which
oapi-codegen-correlator read from the server's request log; the rest is
the Go code the server ran. `e2e/create.spec.ts` fills in the form
instead. Its recording holds `api:createNote`, `server.CreateNote`,
`validate` and `Title`, and no `Find` or `Words`. That difference is what
lets pipelinegen run one spec and not the other for a backend change,
although both open the same page.

The Go tests are recorded the same way, each from its own `go test` run:
`api/search_test.go` executed `Words`, `api/create_test.go`, in the same
package and the same Bazel target, didn't.

## The lists

The lists are every slice's recording for one commit. When a run that
records is over, pipelinegen attaches the recordings of the slices that
passed in it to the commit it tested, so the next change can be planned
against them; a slice that failed keeps what the lists held for it. A plan reads the
lists of its base commit; a commit without lists uses those of its nearest
ancestor that has them. Most runs only add to the lists; a run of
everything replaces them, which [refreshing the
lists](refreshing-the-lists.md) explains.

On a pull request, the run tested a merge commit that never lands. Its
recordings are attached to the commit the merge produces once the pull
request is merged, by whatever merges it, through `pipelinegen lists
attach`.

## Bound by the graph or by recording

Each suite says how its slices are bound to code, with `bind`:

- `bind: graph` (the default) uses the Bazel graph alone. A slice runs
  when its owner target changed. No plugins, no coverage, no recordings.
  A test file with no owner target and no other target holding it has
  nothing to be bound through: the check refuses it, and a plan that
  meets it anyway runs it on every change.
- `bind: recorded` binds each slice to what it executed, plus the files of
  its owner target that no plugin describes. It needs an sbom plugin and a
  correlator, and a CI task that writes coverage.

For the edit to `Words`, the graph selected four Go test files and no
browser spec ([what Bazel does](bazel.md)). Bound by recording, the same
change selects the three Go test files and the one browser spec that ran
`Words`:

<!-- output: affected-words-recorded -->
```
$ pipelinegen affected --base HEAD~1
pipelinegen affected: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e: 1 selected, 1 untouched
  run e2e/search.spec.ts
go: 3 selected, 2 untouched
  run api/search_test.go
  run search/search_test.go
  run text/text_test.go
```

## Without a recording

A slice that has never been recorded can't say what it executes, so it is
bound to everything: it runs on every change until a recording run gives
it a list. The same goes for a slice whose correlator couldn't tell what
it ran (its coverage was missing, say). Its old list stays, but a mark
binds it to everything until a later recording tells again, and every plan
warns about it with the reason of the newest recording that couldn't
tell.

A list that exists binds exactly its units, even an empty one. A test that
ran and executed nothing of the repository is suspicious, so by default
that counts as "couldn't tell" too; a suite whose tests really touch
nothing of the tree says `empty_recordings: allow`.

Next: [the graph and the walk](the-graph-and-the-walk.md).
