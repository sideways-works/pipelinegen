# The nightly

A nightly run compares with nothing and selects everything (`compare:
none`, `select: all`). It is the net under every shortcut the selection
takes, and it does three jobs.

It runs every slice. Whatever a narrowing left out during the day, or
whatever a plugin got wrong, fails here if it is broken. Its failures are
counted against the narrowings that left those slices out of the day's
runs, which is what the [alarm](narrowing.md#the-alarm) reports.

It records every slice. With `record: true`, once its last suite has
ended, the recording of every slice that passed replaces its list,
whether or not other slices failed: code a test no longer runs drops out,
and slices that were marked or never recorded are bound exactly again by
the morning. A slice that failed keeps its older list until a run records
it passing. [Refreshing the
lists](refreshing-the-lists.md) follows one list through it.

It rebuilds the graph cold. Keeping the graph up to date from a cache
rests on every plugin answer depending only on the files it declared. A
plugin that read a file it didn't declare would leave a stale answer that
nothing else notices. So a run that selects everything and records asks
every plugin about every file again, without the cache, and compares the
fresh graph with the cached one. Any difference names the plugin, the file
and the unit or link that differs, and turns the run red (or, with
`rebuild_check: warning`, is listed as a warning). The fresh answers then
replace the cached ones. This costs the plugins' full analysis of the
repository once a night, the same as a first plan on an empty cache. Pull
request runs never rebuild.

A red nightly is the signal to look: either a test broke on a change the
selection left out, or a plugin's answers drifted. Both are worth knowing
by the morning.

A nightly runs only when something changed. A run has one round per
commit, so a night that finds the branch where the last nightly left it,
passed or failed, plans nothing: its log says `nothing changed since the
nightly of <commit>: not run`, no status changes, and no suite starts. A
failed slice of that round runs again when you rerun its suite's build;
to run everything again, push a commit.

That is the last concept. The [glossary](../glossary.md) has them all in
one place, and the [step-by-step guide](../tutorial/README.md) puts them
to work on your own project.
