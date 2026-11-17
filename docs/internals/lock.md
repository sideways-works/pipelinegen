# The lock, removed

pipelinegen no longer takes a lock between runs. A suite used to take a
lease, a ref under `refs/pipelinegen/locks/` (or a file under `locks/` on
the branch store's `-locks` branch), around its build or each batch, to
keep two repositories' pipelines from overfilling a shared machine. That
job moves to admission by each node's observed free memory and pressure,
which is coming next; see [sharing machines](../how-to/sharing-machines.md#between-repositories).

What is left, for one release:

- `suites.<name>.lock` still loads, and `check` says it is no longer read.
  Its `minutes` is still read as the old spelling of the suite's `minutes`.
- `pipelinegen lock` and `pipelinegen unlock` do nothing and exit 0, so a
  pipeline generated before keeps running its lock steps. `lock acquire
  --started <dir>` still stamps the directory, for that pipeline's batch
  end to judge the batch's time limit by.
- The one-step plan of a trusted run (`planAfter` in `internal/cli`)
  deletes the leases an older pipelinegen left in its store; a run that
  isn't trusted leaves them, and a failed sweep only warns.
- A runner ignores an older tool's spec that holds a lock and says so in a
  notice; the conformance kit refuses a pipeline that runs `pipelinegen
  lock` or `unlock`.

The release after removes the commands, the sweep and the key.
