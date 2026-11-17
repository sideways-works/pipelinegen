# Backups of the hidden refs

pipelinegen keeps its state in your repository, under
`refs/pipelinegen/`: the lists, each run's plan and states, and the
plugins' answer cache. A plain `git clone`
or a mirror that copies only branches and tags leaves them out.

## Include them

A mirror or backup should fetch them too:

```sh
git clone --mirror https://github.com/owner/example.git    # all refs
# or, in an existing backup
git fetch origin '+refs/pipelinegen/*:refs/pipelinegen/*'
```

A ruleset that covers every ref must leave `refs/pipelinegen/*` to the
pipeline's token. The pipeline never pushes to a branch, so branch rules
need no bypass for it.

## If they are lost

Losing them is safe. Without lists, a recorded slice is bound to
everything, as on a first run, so runs select more until they record again.
Without a run's refs, its comment and `why` can't be rebuilt. Without the answer cache, the plugins are
asked again about every file, which costs time and changes no answer.

Never push to `refs/pipelinegen/*` by hand: every write is a
compare-and-swap that the tool alone makes, and a hand-made ref can make a
plan read lists that no run recorded.
