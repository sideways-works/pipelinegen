# Declaring runs

pipelinegen has no built-in kinds of run. You declare each run by name in
`pipelinegen.yaml`, and each says what starts it, what it compares with,
what it selects, which suites it covers with which params, and how it
reports. Most repositories want three:

```yaml
runs:
  pr:
    trigger:
      pull_requests: {base: main, draft: false}
    compare: merged_target
    select: affected
    suites: {unit: {}, smoke: {}}
    record: true
    report:
      comment: {format: mermaid}
  main:
    trigger: {branch: main}
    compare: last_passed
    select: affected
    suites: {unit: {}, smoke: {}}
    record: true
  nightly:
    trigger:
      timer: {start: "3:00", stop: "4:00", location: Europe/Berlin}
      branch: main
    compare: none
    select: all
    suites: {unit: {}, smoke: {}}
    record: true
```

A run's name is lower case letters, digits and dashes. It names the run's
jobs (`plan-pr`, `pr-unit`), its statuses (`pipelinegen/pr/unit`) and its
refs.

## What starts a run

Exactly one trigger:

- `branch: main`: each new commit on the branch. Concourse takes the newest
  commit; a commit skipped in a burst is covered by the next one's run,
  which compares with the last commit whose run passed.
- `pull_requests`: open pull requests into `base`, each new head commit.
  Filter them with `draft` (`false`, the default, for ready ones only,
  `true` for drafts only, `any` for both), `labels` (all of these), `labels_without` (none of these) and
  `head` (a pattern for the head branch, such as `train/*`). The planning
  job applies the filters: a head that doesn't pass them is not planned,
  the log says why, and every suite job of that head skips.
- `timer`: once a day in a window, on the tip of `branch`. `days` limits it
  to some days of the week.
- `manual: true`: only when someone starts the planning job, on the tip of
  `branch`.

A run has one round per commit of its branch. A timer, a manual start or
a continuous build that finds the commit its run last planned, passed or
failed, runs nothing: the plan step says `nothing changed since the
<run> of <commit>: not run`, posts no status, and no suite starts. To run
a failed slice again, rerun its suite's build; that is the same round.
There is no flag to plan a commit again: its round's records are the
commit's, and a second round of it would mix with the first, so push a
commit to run again.
- `continuous: true`, beside `branch`: every commit on the branch queues a
  build, and one runs at a time, so runs follow each other back to back,
  each of the newest commit. See below.

A pull request run has one round per head commit too. Its planning job
hands a head on to the suite jobs only when its build wrote the head's
plan, so planning the same head again starts no second set of suite
builds beside the first: a build that finds the head planned says
`planned, a suite began: nothing handed on` (or `planned, its suites on
their way`), ends green, and no suite build starts. The one exception is a
head planned at least `missed_after` ago none of whose suites began: the
build hands it on again, as the run's sweep would, and marks it as the
sweep does, so the sweep and other builds leave it to the suite builds it
queued. That holds for a head the sweep gave up on too, so starting the
planning job by hand is how a head whose suite builds never started gets
them. A suite began once a build of it started: its start step records
that before the suite's prepare, so a build busy with its prepare and its
first batch is never handed on again, however long they take. A build
waiting for a worker has started nothing, though, and a head whose suite
builds wait longer than `missed_after` is handed on again; once a worker
is free, both builds run every slice without a pass. Set `missed_after`
above the longest wait for a worker you expect. A suite build that started
and was then lost with no hook of the CI system run after it (a lost
worker usually still runs the job's error hook, which reports the suite
as errored) leaves its suite pending: nothing hands the head on again,
just as for a build lost after its first batch. Rerun that suite build by
hand; it runs. To run a pull request's suites again, rerun the suite build: a
rerun takes the selected slices that have no pass for this head, a failed
slice among them. Or push a commit, which is a new head and a new round.
Triggering the planning job again no longer runs a head's suites a second
time.

`once_per_ready: true` on a pull request trigger tests a pull request once
each time it becomes ready for review. A push to a ready pull request then
starts nothing until the pull request goes back to draft and is marked ready
again. Use it where every test run is expensive and authors mark a pull
request ready when it is done.

### Continuous runs

```yaml
runs:
  full:
    trigger: {branch: main, continuous: true}
    compare: none
    select: all
    record: true
    suites: {e2e: {}, go: {}}
```

A continuous run is made for the run of everything: instead of one a night,
rounds back to back all day, one round at a time, each of the newest
commit. Every commit of the branch starts a build of the planning job.
The planning job is never serial: its builds run side by side, and each
records its round in the run's records for the others to read (below).
Each build first decides, in a step
of its own before the planner's prepare command (`pipelinegen plan
--decide`), and reads the branch's tip from the remote. A build whose
commit is not the tip exits at once, green, with nothing prepared and
nothing planned:

```
full 4f1c2a9e07b3: superseded by 7d20b6e1c9aa: not run, the newer commit's run covers it
```

If its commit is the tip and a round of the run is in progress, it waits,
and says so on its first look and once a minute after:

```
full 7d20b6e1c9aa: waiting for the round of 9a0e51c2d4f8 to end (its last step was 312 seconds ago; it counts as dead after 4h30m0s)
```

It reads the tip again at every look, every 15 seconds: when a newer
commit appears while it waits, it exits at once with the same superseded
line, though the round goes on. When the round has ended and its commit
is still the newest, the build goes on:

```
full 7d20b6e1c9aa: the newest of main, and no round in progress: planning
```

Only then does it run the prepare command, and then the plan step, which
decides once more (at once, unless the branch moved or a round started
meanwhile: then it exits or waits as above) and plans. So whatever the
prepare leaves for the plan, such as a git credential cache with a short
life, need only last the plan's own limit, `limits.plan`, counted from
the end of the wait; and a build that is superseded never prepares.

Nothing is taken or held: whether a round is in progress is read from the
run's own records, the newest plan of the run whose suites haven't all
ended.

- A build that exits hands nothing on: the planning job puts the run's
  planned hand-off only when its step wrote a plan, and the suite jobs
  follow that hand-off (and fetch the commit the same planning build
  had), so a superseded or unchanged commit shows one green planning
  build and no suite build.
- A superseded commit gets no status in the run's context: a commit status
  can't say "skipped", and green would claim a run that never happened. The
  plan job's log says `superseded by <commit>`, and `pipelinegen status`
  shows the commit as superseded (exit 3, no run). Its change is not left
  untested: the next round compares with the run's last passed commit
  (with `compare: last_passed`), which comes before it, so the change is
  in that round's comparison and selects what it reaches.
- A build waits at most `wait_minutes` (480 by default). By then the
  branch has most often moved on, and the build ends superseded; one whose
  commit is still the newest fails, naming the round it waited for. The
  planning task's time limit is `limits.plan` and `wait_minutes`
  together; the decision may take all of it but `limits.plan`, and the
  prepare and the plan have `limits.plan` from the decision's end.
- A round whose build is gone (its worker was lost) records nothing more.
  Once it has recorded nothing for `idle_minutes` it counts as dead, and
  the next build no longer waits for it. By default that is the longest
  stretch the run's limits allow without a recorded step: `limits.steps`
  and twice the largest `minutes` of its suites (a batch records nothing
  until it ends, and a prepare comes before it), plus 10. When you know a
  round is dead, end it at once:

  ```
  pipelinegen end --run full --commit 9a0e51c2d4f8
  ```

  It records that the round is over and ends each of its suites: their
  slices without a result are errored "the run was ended by hand", and no
  success is posted for what didn't run. The waiting build sees it at its
  next look, within 15 seconds; a suite job of that round that starts
  later runs nothing. `end` takes `--change` too and ends any run of a
  change the same way, such as a pull request's run nobody wants finished.
- `turn_minutes` is no longer read; `pipelinegen check` says so, and a
  run's steps don't.
- A finished continuous run attaches its lists as a nightly does: each
  round that passes [replaces the
  lists](../concepts/refreshing-the-lists.md). A repository may keep its
  nightly beside it, though it adds little.
- A rerun of an older round's suite job, started by hand, runs beside the
  round in progress: nothing stops it, and it starts no round of its own.
- The planning job's builds run side by side, in every runner and in a
  CI system you drive yourself. Each build's plan step records, as its
  first act once its commit
  is the tip, that its round started, and, as the act that ends its wait,
  that its round plans; after each it reads the tip and the run's rounds
  again. Of two builds that decide at once each sees the other's record or
  the moved tip, so the older commit's exits and the newer one waits for
  it. A build of a commit whose round another build of it started runs
  nothing (`nothing changed since ...`). A round with those records and no
  plan yet counts as dead after `limits.plan` and 10 minutes without a
  newer record, and its plan must be written within `limits.plan`:
  a plan that takes longer fails and says to raise `limits.plan`. The
  records are written once and never renewed; they are swept after 14
  days. `pipelinegen end` ends such a round too.
- A round whose plan fails records that, beside its statuses: the next
  build no longer waits for it and plans at once. A build of the same
  commit that starts again records anew, and the next build waits for it.

## What it compares with

- `merged_target` (pull requests only): the pull request's head merged into
  its base's tip, compared with that tip. The tree under test is that merge,
  so a pull request is tested as it would land.
- `last_passed`: the newest earlier commit on the branch whose run of this
  name passed, read from the run's commit status. With none, the newest
  earlier commit whose run is still going, and the plan warns "no earlier
  run of main passed; compared with <commit>, whose run is still going".
  That run is selecting everything and will make the lists, so only it
  runs everything, not every commit pushed while it runs. With neither,
  every slice runs. A failed run is never a base. While the
  `passed-since` narrowing is on (levels 1 to 3), a slice is compared
  from the newer of that base and the newest earlier commit at which it
  passed in this run with its tests run, so a slice that keeps failing
  doesn't keep every other slice running on each commit; see
  [narrowing](narrowing.md#slices-that-passed-since-the-base).
- `none`: nothing, so every slice runs.

## What it selects

`select: affected` runs the slices the change reaches; `select: all` runs
every slice. A run that compares with nothing selects everything whatever
it says, and so does any run that can't be sure (see
[what it does](../introduction/what-it-is.md)).

## Suites and their params

`suites` names the suites the run covers. Each may carry params, merged over
the suite's own `params` (the run's win), `secrets` that add to the
suite's (see [secrets](secrets.md)), and `minutes` for a run whose
batches take longer than the suite's time limit:

```yaml
nightly:
  suites:
    e2e: {params: {PROJECTS: all}, minutes: 240}
    backend: {}
```

The run plans, runs and waits for these suites and no others. A suite the
config defines and the run leaves out gets no status in it and no line in
its comment, and the run ends, attaches its lists and ends a continuous
run's round once its own suites have ended. A change that reaches only
suites the run leaves out selects nothing in it: the run's statuses say
"nothing selected" and pass. A suite that every run leaves out is never
run at all, so `pipelinegen check` fails on it ("suite <name> is in no
run: its tests never run") while the config declares runs; planning only
warns, so a pipeline keeps running while you list it under a run or
remove it.

A branch's own commit runs the suites its configuration names: the
pipeline is made again from that configuration, so a suite the commit
takes out of a run has no job left and isn't waited for. A pull request,
or any other proposed change, can add a suite to its run, and the run then
plans and waits for it too. It can't take one out: its run keeps every
suite the configuration the pipeline was made from names for that run,
the plan says the change edits `runs.<name>.suites`, and the suite is
tested as the pipeline's jobs test it until the change is merged.

## Time limits

Every step of a run has a time limit, so a step that hangs ends and says
which limit it reached instead of holding its run pending for ever, and a
continuous run's next round after it. A suite's own tasks have the
suite's `minutes` (120 by default) each: its prepare, each batch and each
whole-suite check, so one batch that hangs ends while the others go on.
The suite's total is bounded by its batches times its
`minutes`, and by Concourse's own settings for the job, not by a key.
`batch_minutes` is what each batch is dealt to take, and the limit must be
at least that. The planning tasks have `limits.plan` (120) and
pipelinegen's own steps `limits.steps` (20), and a continuous run's plan
step its `wait_minutes` on top. See [troubleshooting](../troubleshooting.md#a-step-reached-its-time-limit)
for what a stopped step says.

## Recording

`record: true` makes the run's recorded suites (`bind: recorded`) record
what each slice touched; it changes nothing for a suite bound by graph. Record in every run that tests code that lands:
pull requests, the branch and the nightly. A branch's own run attaches what
it recorded to the commit it tested. What a pull request recorded reaches
the branch's lists when whatever merges it calls `pipelinegen lists
attach`; see [attaching the lists](attaching-lists.md).

## How it reports

- `report.status` (default `true`): one commit status per suite,
  `pipelinegen/<run>/<suite>`, and one for the whole run,
  `pipelinegen/<run>`. Require the run's status in a branch rule; it is
  pending while any suite is, failure or error when any suite is, and
  success when all passed. A suite with nothing selected passes with
  "nothing selected". `github.status_prefix` replaces `pipelinegen`.
- `report.comment` (pull request runs only): one comment per pull request
  and run, `format: list` or `format: mermaid`. It is found again by a
  hidden marker line, never by its author or its place.
- `report.reasoning: true`: the comment shows why each selected slice runs,
  up to three reasons and a count of the rest.

A suite's status says how far it is and how it went:

| State | Description |
|---|---|
| pending | `3 of 12 slices done, 1 failed so far, 28 untouched` |
| success | `12 of 40 slices ran, all passed` |
| success | `nothing selected, 40 untouched` |
| failure | `12 of 40 slices ran, 2 failed (5 tests)` |
| error | `12 of 40 slices ran, 1 errored` |

## Overrides per run

A run may override, for its own runs, what planning does with uncovered
files (`uncovered`), how far a change is followed (`follow`) and how long
its runs are kept (`keep`). The [config reference](../reference/config.md#runs)
has every key.
