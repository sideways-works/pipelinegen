# Step 10: the first run

Push a commit to `main`, or trigger `plan-main` in Concourse by hand. The
first `main` run has no earlier run to compare with, so it plans every
slice, with the reason "nothing to compare with", and records them all.
To skip that run of everything, see [start without a full first
run](../how-to/starting-narrow.md).

## What happens

1. `plan-main` checks out the commit, plans it, and stores the plan in the
   repository's hidden refs. It sets a pending status per suite.
2. `main-go` deals the selected slices into batches (up to four by
   default) and runs your batch task once per batch.
3. As each slice ends, its state goes into the run's records; at the end
   of each slice the correlators read its coverage and record what it
   executed.
4. When the suite ends, it sets its status, `pipelinegen/main/go`, and the
   run's, `pipelinegen/main`. When the last suite has ended, the
   recordings of the slices that passed are attached to the commit as its
   lists.

> **Output to come.** The planning job's log, the suite job's batches and
> the statuses on the commit will be filled in from a real run.

**You should see** both jobs green in Concourse and the two statuses on
the commit in GitHub.

## Check the lists

Once the run has passed, fetch the hidden refs and look at what each slice
executed:

```sh
git fetch origin '+refs/pipelinegen/lists/*:refs/pipelinegen/lists/*'
pipelinegen lists show
```

For the notes service, recorded on your machine in [step
8](on-your-machine.md) (after a CI run, the first line names the
repository's remote instead of the local store):

<!-- output: lists-show -->
```
$ pipelinegen lists show
pipelinegen lists show: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
lists of 26118462acef: its own lists
with the recordings of the live runs:
  local local-<time> 26118462acef (5 slices)
  local local-<time> 26118462acef (2 slices)
e2e: 3 slices
  _setup 7
  e2e/create.spec.ts 12
  e2e/search.spec.ts 9
go: 5 slices
  api/create_test.go 10
  api/search_test.go 12
  notes/notes_test.go 4
  search/search_test.go 3
  text/text_test.go 2
```

The `e2e` suite has one list more than it has specs: `_setup`, what the
server ran while it started (its `main`, the seed notes) before the first
spec. The server keeps running across the specs, so go-correlator keeps
that apart from each spec's list, and a change to it runs every spec of
the suite.

One slice's units, here a browser spec's: the API operation it called
and the Go code the server ran for it:

<!-- output: lists-show-slice -->
```
$ pipelinegen lists show --slice e2e/search.spec.ts
pipelinegen lists show: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
lists of 26118462acef: its own lists
with the recordings of the live runs:
  local local-<time> 26118462acef (5 slices)
  local local-<time> 26118462acef (2 slices)
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

**If you don't see lists:**

- The slice didn't pass. Only a slice that passed gets a list, so a slice
  that failed stays unrecorded and keeps running on every change. Fix the
  failure and rerun its suite's build.
- A slice shows as marked: a correlator couldn't tell what it executed,
  usually because its coverage was missing. Check that the task writes
  coverage into `coverage/<slice>/` ([step 7](tasks.md)).

## The next change

From now on, every commit on `main` is compared with the last one whose
run passed, and runs only what it reaches. Make the edit to `Words` from
step 5 again and push it: `main-go` runs three slices instead of five, and
`main-e2e` runs `e2e/search.spec.ts` alone.

Next: [step 11, pull requests](pull-requests.md).
