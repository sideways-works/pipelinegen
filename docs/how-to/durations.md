# Durations and batches

A run deals each suite's selected slices into batches, so that each batch
sets up once and the batches take about the same time. It deals by how long
each test file took before, from the suite's durations file, checked
in:

```json
{"version": 1, "files": {
  "e2e/editor.spec.ts": {"chromium": 35.0, "firefox": 31.5},
  "api/notes_test.go": 7.9
}}
```

A file's value is seconds, one number or a map per parameter set (the
browsers or screen types the suite runs with), which are summed. Paths are
from the repository's root. Name the file in the suite:

```yaml
suites:
  e2e:
    durations: ci/durations/e2e.json
    parallel: 3
```

## How batches are dealt

At most `parallel` batches (default 4). The longest slice goes first, each
into the batch with the least time so far; the same slices give the same
batches whatever order they come in. A file missing from the durations file
counts as the median of those that are there (as one second when none is),
so a new test file needs no entry to run. A missing durations file is an
empty one.

Three selected files and `parallel: 3` make three batches of one; one
selected file makes one batch and one setup.

## Keeping it current

`pipelinegen durations` reads `go test -json` output or Playwright's JSON
report and updates the file: measured files get their new times per
parameter set, others keep theirs, and files that no longer exist are
dropped. Times are rounded to a tenth of a second.

```sh
go test -json ./... > unit.json
pipelinegen durations --suite unit --go-json unit.json
npx playwright test --reporter=json > e2e.json
pipelinegen durations --suite e2e --playwright e2e.json
```

A run keeps each slice's time itself: its batches' journals time every
slice from its start to its end (the sum of its parts, process start-up
included), and the slice's state holds it. So the durations can come from
a full run without any report file:

```sh
pipelinegen durations --suite e2e --run nightly --change main --param chromium
```

Run it now and then from a full run's output and commit the result. Stale
times only make batches less even; they never change what runs.
