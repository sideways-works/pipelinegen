# Uncovered files at planning

The commit hook refuses uncovered files, but a commit can get past it (a
hook that wasn't installed, a merge made on GitHub). So planning runs
[the check](../reference/check.md) on the whole tree too, and the `uncovered`
setting says what it does with what it finds:

```yaml
uncovered: everything      # the default
runs:
  pr:
    uncovered: fail          # beside the run's other keys
```

| Setting | What planning does | When to use it |
|---|---|---|
| `fail` | The run fails at planning, before any slice starts. Its statuses and the comment list each uncovered file and its fix. | Once the repository is clean, at least for pull requests: an uncovered file then can't land. |
| `everything` | The run selects every slice and reports the same list as a warning. | While you adopt pipelinegen step by step: nothing is missed, it only costs time. |

A run's own `uncovered` overrides the top-level one, so a nightly can stay
on `everything` while pull requests fail.

## Other reasons a run selects everything

Some things can't be known before a run, and each selects every slice with
its reason instead of failing. [Troubleshooting](../troubleshooting.md#everything-ran)
lists each reason and what to do about it.
