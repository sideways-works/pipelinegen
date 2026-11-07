# Keeping history

Two kinds of hidden refs grow over time: each run's plan, slice states and
recordings, under `refs/pipelinegen/plans/` and `refs/pipelinegen/runs/`;
and the lists attached to commits, under `refs/pipelinegen/lists/`. Nothing
is deleted when a run ends, so a finished run's comment and `why` can be
reproduced later. The sweep removes what falls out of `keep`:

```yaml
keep:
  runs: {for: 5d}             # or {last: 20}; with both, kept while either holds
  lists: {last: 500, for: 30d}
runs:
  nightly:
    keep: {last: 30}                # a run may keep its own longer
```

- `keep.runs` (default 7 days) keeps, per run name, the newest `last` runs
  and those planned within `for`. A run's refs go together, in one push.
- `keep.lists` (default 90 days) keeps the newest `last` lists and those
  made within `for`. The newest lists of each branch are kept whatever the
  setting, so planning always finds lists to compare with.
- More pull requests named for a comment (`also-to`) go with the last run
  of their change.

Periods are a number and a unit: `12h`, `5d`, `2w`.

Every planning job runs the sweep. A ref that changed while being swept is
left for the next sweep, so the sweep never removes a run that is still
going.

A commit whose lists were swept falls back to its nearest first-parent
ancestor's lists, as any commit without lists does, so plans against it
stay correct; only the swept runs' comments and `why` can't be rebuilt.
