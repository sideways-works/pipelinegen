# The reporter

`internal/report`: a run's state in refs, the commit statuses, the comment
and the sweep. `pipelinegen report` and `pipelinegen slice` call it.

## States

A slice moves through `pending` (no ref yet), `running`, and one of
`passed`, `failed`, `errored` and `not run`. `untouched` is a slice the plan
left out. `not run` is every slice of a batch that ended before any of its
slices started or left a result: its setup failed. The batch's end says so
in one line ("batch 2 recorded nothing: its setup failed before any slice
ran"), asks no correlator, and the suite's status counts those slices as
not run (`1 of 4 slices ran, 2 not run (a batch's setup failed)`), not as
failed. A rerun runs them. A batch that found the worker short of memory
(`slice memory`, or `slice run` before its set-up) is not run the same
way, with the failure `(memory)` and the figures, and the status counts
those apart: `2 not run (worker short of memory or CPU)`. A suite's end that
finds the run's only gaps are such slices, or slices that crashed out of
memory, writes the run's `_short` mark, by which the sweep hands the head
on again. A recording batch whose slices ran but left no
coverage at all is told once too, and none of its slices is observed.
A slice's state comes from its result file: `passed` only when the file
says so and lists no failure, `failed` otherwise, `errored` when the file is
missing or unreadable. In a recording run every slice of a recorded suite
records (a whole-suite check never does): one whose tests passed but whose
recording failed, or that never reached `slice end` and so left no
coverage, is `errored`; a failed one stays `failed`. A slice whose extras
pass 1 MB keeps none and, if it passed, is `errored` too. A slice still pending
or running when its suite's end marker exists is shown as errored: it never
said how it went. One that starts again after that marker is a rerun and
shows as it is.

## Writing

A batch's `slice` steps note each start and end in the batch's journal
(`Reporter.Started`, `Reporter.Ended`). `Reporter.Flush` writes the refs of
every slice noted since the last flush in one atomic push, then publishes;
`Reporter.Slices` does the same for the slices of one event outside a
batch. The refs are the truth: a GitHub error after the push is a warning,
and the next writer publishes again. A batch step run without a token
(`NoToken`) writes nothing: the batch's `report batch-end`, which has one,
flushes the journal when the batch ends.

## Publishing

Publishing reads the plan and every state ref by prefix, builds the whole
run, sets the statuses in scope and rewrites the comment. Then it lists the
refs again: when they moved meanwhile, it renders again, up to five times.
So the last writer always leaves the newest state, and no update ever
patches the comment in place.

A suite's end and the finish also attach a branch's recording run's lists
(`ending`). A run whose every slice passed attaches as it turns green, and
an attach that fails turns it to error instead. A run that failed attaches
once every suite of its plan has ended and a slice of it passed: the
attacher judges each slice by its own state, so the slices that passed
replace or add to their lists, the others keep theirs, only the slices
that passed bring their marks, and a suite's setup list shrinks only when
every slice of that suite passed. Its failure there is a warning, the run
being red already. The same run attached again (a rerun of a suite's
build) brings the slices that passed since, and writes nothing when the
lists hold all it has.

## Statuses

Per suite, `<prefix>/<run>/<suite>`: pending while any slice is pending or
running, then error, failure or success, with a description of at most 140
characters ("3 of 12 slices done, 1 failed so far, 28 untouched"). Per run,
`<prefix>/<run>`: pending while any suite is, error or failure when any is,
success when all are. The prefix is `github.status_prefix`.

## Extras and collect

A slice may keep up to 1 MB of extra files (`result/<slice>/`) in its ref,
next to its state and recording. `report collect` writes every slice of a
suite, with its state, recording and extras, into a directory for a
whole-suite check: those checks judge the suite from what each slice
left.

## GitHub's write limits

A run with a hundred slices would post hundreds of status and comment
writes, and GitHub limits writes that create content. So a slice's report
leaves a pending update that GitHub got less than 15 seconds ago standing
rather than replacing it (`Reporter.Throttle`), posts only the statuses
that differ from what the commit shows (a change of state always goes; a
new count on a pending suite may wait for the next report), and leaves the
comment alone when its body is unchanged. The suite's end shows everything.
When GitHub refuses a write for its rate limits (403 or 429 with
`Retry-After`, or no requests remaining), the client waits as asked, at most 90 seconds
at a time, and tries again.

## The comment

One per pull request and run, found by its first line, a hidden marker:
`<!-- pipelinegen:<run>:<pull request> -->`, never by author or position.
When two writers both made one, the newer ones are deleted. `also-to` pull
requests get the same body.

The body is a heading with each suite's status, then either a list (each
suite, its slices with their state, the untouched count, removed slices) or
a Mermaid flowchart, then the reasons (with `report.reasoning`) and the
failed slices' failing tests with the end of their output. A suite's
shared causes, the reasons two or more of its slices share, come once
under its heading with how many slices each selects, also with the
reasons off when every slice of the suite runs; a slice then lists only
the reasons of its own. A chart past 400
edges or 40,000 characters falls back to the list, under the limits of 500
edges and 50,000 characters GitHub renders Mermaid with
([proof.md](../proof.md)).

A suite that selects 9 slices or more is grouped: its failed slices come
first, each in full, and the others fold per group into a count of each
state, worst first, with the slices under it opened on a click. A slice's
group is the first group of the suite's `group` pattern matched against its
name, else the leading word of its file name. A slice shows by its name (a
test file's path), else by its label.

The comment stays under 65,536 bytes, in this order of giving way: the
reasons go first, then the failures' output, and last the text is cut with
a note. Each slice keeps at most 10 failures and, in the comment, the last 15
lines, at most 1,500 bytes, of each; the run's records keep the last 400
lines, at most 32 KiB.

## The sweep

`report.Sweep` removes what `keep` no longer holds: each run, with its
plan, states, recordings and end marks in one atomic delete (a run of
more than 300 refs in atomic chunks of 300, its plan last); `also-to` refs
of changes with no run left; and old lists, in batches of 300, never the
lists of the commits it is told to protect (each branch's newest). Runs are
ranked per run name by their plan's time. A delete names every ref's value,
so a run that changed while being swept is left for the next sweep.
