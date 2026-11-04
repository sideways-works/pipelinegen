# Changes and tests

## A change

A change is the difference between two commits: the base, which already
ran, and the head, which is new. On a pull request the head is the pull
request merged into its target branch, and the base is the target branch's
tip. On a branch the base is the newest earlier commit whose run passed.
pipelinegen always looks at what differs between those two commits, never
at a single commit on its own.

In the notes service, say someone edits `Words` in `text/text.go` so that
hyphenated words stay whole:

```diff
 func Words(s string) []string {
 	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
-		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
+		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-')
 	})
 }
```

The base is the commit before this edit, the head is the commit with it.

## Reaching a test

A change reaches a test file when the test could behave differently
because of it. That happens in three ways:

1. The test file itself changed, or a file it reads.
2. Code the test is built from changed. `text/text_test.go` is compiled
   with `text/text.go`, so the edit above reaches it.
3. Code the test ran changed, even though nothing in the build says the
   test depends on it. `e2e/search.spec.ts` opens the page in a browser
   and types a word into the search box; the page sends a request to the
   server, the server's `list` handler calls `search.Find`, and `Find`
   calls `Words`. The edit reaches that browser test too, although no file
   of the page or of the test changed.

The third way is the one that matters most and is the hardest to see.
`pipelinegen why`, which later pages introduce, prints the path for each
test it runs. For an edit to the backend's validation, which the browser
spec that submits the form reached through the server:

<!-- output: why-validate-create -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/create.spec.ts was selected from 5d0f0a819953 to 05bf3ac32254:
  it executed api.validate during its recording (api/api.go), which changed in this commit
  observed by: go-correlator, oapi-codegen-correlator
```

And for a change to the operation in the API contract, which the same spec
called; the arrow is a link one of the plugins knew:

<!-- output: why-contract-spec -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/create.spec.ts was selected from 5d0f0a819953 to 2935eb2b32eb:
  it requested POST /notes (createNote) during its recording, which changed in this commit
  it executed notesapi.ServerInterfaceWrapper.CreateNote during its recording (api/notesapi/notesapi.gen.go)
    → which leads to POST /notes (createNote) (a link from oapi-codegen-correlator), which changed in this commit
  observed by: go-correlator, oapi-codegen-correlator
```
A browser test that clicks through a web app reaches every backend route
it called, and no build file records that. pipelinegen learns it by
recording what each test ran ([recordings](recordings-and-lists.md)).

A test file the change doesn't reach can't fail because of it, so it can
be left out of the run. pipelinegen calls it untouched. For the edit
above, `api/create_test.go`, `notes/notes_test.go` and
`e2e/create.spec.ts` are untouched: they never ran `Words`.

## Why the whole file

pipelinegen decides per test file, not per test function. A file is what
test runners take as input, what developers think of as "a test", and
small enough that leaving one out saves real time. When a file runs, all
of its tests run.

Next: [what Bazel does](bazel.md).
