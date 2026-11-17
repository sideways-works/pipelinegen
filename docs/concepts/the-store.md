# The store

pipelinegen remembers things between runs: each run's plan and the state
of its slices, the recordings and the lists made from them, the plugins'
cached answers. Where it keeps them is the store.

The store is the sixth kind of plugin, and still a design. The default store, and today the
only one, keeps everything in your own git repository, under hidden refs,
so you need no database and no service. Other stores can keep the same
records elsewhere, such as in a database, or handle many writers at once
for a large team. Each store also purges what is no longer needed. Until
store plugins land ([the design](../writing-plugins/store.md)), the git
store is built into pipelinegen; [what works
today](../introduction/what-works-today.md) says when that changes.

## The git store: hidden refs

A git ref is a name that points at a commit, like a branch. Branches live
under `refs/heads/`; the git store writes under `refs/pipelinegen/`. Git
hosts don't show those refs, clones don't fetch them by default, and they
never appear in a diff or in the history of a branch. pipelinegen is their
only writer: never push to them by hand.

| Under `refs/pipelinegen/` | What it holds |
|---|---|
| `lists/<commit>` | what each slice executed, for that commit |
| `run/<run>/<change>/<commit>` | a run's plan and the state of its slices |
| `batch/...` | a batch's slices while it runs; folded into the run's ref when its suite ends |
| `locks/<name>/<slot>` | a lease an older pipelinegen took; the next plan step of a trusted run removes it |
| `cache/<plugin>/...` | the plugins' answers, by content |

A run keeps few refs: one per batch while it runs, and one for the whole
run after. Every push to a repository is told about every ref in it, so
fewer refs keep every `git push` fast.

The git store can also keep the same records as ordinary files on a branch
of their own, with `store: {backend: branch}`. The [config
reference](../reference/config.md#store) says what that branch needs.

The stores run the git of the image each step runs in, from 2.39 (Debian
12, as in `golang:1.27.1-bookworm`) on. Their tests run with the newest
git and with a stand-in that answers as a git before 2.45 does (Debian
12's 2.39, Ubuntu 24.04's 2.43), over a plain remote and over smart HTTP,
and they pass on 2.39 itself.

## Lifetimes

Every planning job sweeps what is no longer needed, as the config's `keep`
section says. By default a finished run is kept for seven days, and a
commit's lists for 90 days, or while the commit is among the default
branch's last 200; each branch's newest lists are always kept. Plugin
answers that no plan used for two weeks are dropped. The [keeping
history](../how-to/keeping-history.md) guide has the settings.

## Losing them

If the records are lost (a host migration that copies only branches, say),
nothing breaks and nothing is missed: plans find no lists, so recorded
slices are bound to everything and run until the next recording run gives
them lists again. Back them up with the repository all the same
([backups](../how-to/backups.md)).

Next: [refreshing the lists](refreshing-the-lists.md).
