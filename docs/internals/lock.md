# The lock, removed

pipelinegen no longer takes a lock between runs. A suite used to take a
lease, a ref under `refs/pipelinegen/locks/` (or a file under `locks/` on
the branch store's `-locks` branch), around its build or each batch, to
keep two repositories' pipelines from overfilling a shared machine. That
job is now the wait for memory: a batch starts once its worker has its
predicted peak free and low memory pressure, whichever pipeline holds the
rest; see [sharing machines](../how-to/sharing-machines.md#between-repositories).

What is left, for one release:

- `suites.<name>.lock` still loads, and `check` says it is no longer read.
  Its `minutes` is still read as the old spelling of the suite's `minutes`.
- `pipelinegen lock` and `pipelinegen unlock` do nothing and exit 0, so a
  lock step that runs this release passes. A pipeline runs the pipelinegen
  it was generated with, so one generated before keeps its working locks
  until it is set again. `lock acquire
  --started <dir>` still stamps the directory, for that pipeline's batch
  end to judge the batch's time limit by.
- The one-step plan of a trusted run (`planAfter` in `internal/cli`)
  deletes the leases an older pipelinegen left in its store; a run that
  isn't trusted leaves them, and a failed sweep only warns.
- A runner ignores an older tool's spec that holds a lock and says so in a
  notice; the conformance kit refuses a pipeline that runs `pipelinegen
  lock` or `unlock`.

The release after removes the commands, the sweep and the key.
