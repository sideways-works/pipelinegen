# Start without a full first run

On a large repository the first run of everything can take hours. Two
commands let the first `main` run start narrow.

`pipelinegen seed` is for when you have recorded the suites on your
machine. Check out the commit on the default branch, record each suite
with `pipelinegen record --suite <suite>`, look at `pipelinegen seed
--dry-run`, then run `pipelinegen seed` with the store's token in the
environment. The commit gets the lists your recordings made, and the
first `main` runs compare with it: a run on that commit selects nothing,
and a run on a later one only what the change reaches. A slice that
failed or didn't run on your machine gets no list and runs whole.

`pipelinegen baseline --run main` is for when the tree is already known
good (its CI passed before you adopted pipelinegen) and the lists exist,
or are fine to come from the next runs. It posts the run's status on the
commit as a baseline, saying it was set by hand and is not a run (without
a host it writes the same into the store). The first runs compare with it
as with a passed run. A seed sets the base already for the default
branch's runs, so after seeding there is nothing more to do.

Your machine is not the CI system. A test that takes another code path
there, or that your machine skipped, has a list that misses part of what
it runs on CI, and a change to that part would leave it out. The nightly
run of everything replaces every list with what CI saw, so keep one
running from the start ([refreshing the
lists](../concepts/refreshing-the-lists.md) says what it replaces, and
what to do after a long break). The [command reference](../reference/commands.md#pipelinegen-seed)
has every flag and refusal.
