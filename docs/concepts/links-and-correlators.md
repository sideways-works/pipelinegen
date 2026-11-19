# Links and correlators

## Link

A link says that one unit uses another. `go:api#func:list` calls
`go:search#func:Find`, and `Find` calls `go:text#func:Words`, so there are
links from `list` to `Find` and from `Find` to `Words`. Links are typed by
what the use is:

- a call or a reference in code (`uses`): a handler to the function it
  calls, a function to the type it builds;
- a read or a write of data: a query's code to the table it reads, an
  insert to the table it writes;
- a request to an API: a web component's fetch to the API operation it
  calls, and the operation to the handler that serves it.

In the notes service, the API is described in an OpenAPI contract,
`api/notes.yaml`, and its server code is generated from it. openapi-sbom
lists each operation of the contract as a unit, `api:createNote` and
`api:listNotes`, and oapi-codegen-correlator links the generated code that
serves an operation to that unit. It also reads which requests a browser
spec made, so `e2e/create.spec.ts` is tied to `api:createNote`. A change
to that operation in the contract then runs the spec:

<!-- output: why-contract-spec -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/create.spec.ts was selected from 4c3820d1b492 to 2df3bd8da6d6:
  it requested POST /notes (createNote) during its recording, which changed in this commit
  it executed notesapi.ServerInterfaceWrapper.CreateNote during its recording (api/notesapi/notesapi.gen.go)
    → which leads to POST /notes (createNote) (a link from oapi-codegen-correlator), which changed in this commit
  observed by: go-correlator, oapi-codegen-correlator
```

Links cross languages, which is where they earn their keep. A Go function
that runs `SELECT ... FROM notes` links to the `notes` table that pg-sbom
lists from the migrations, so a change to that table reaches the Go code
that reads it, and from there the tests that ran that code.

## Correlators

A correlator is the plugin that finds links and observes tests. Where an
sbom plugin answers "what exists", a correlator answers "who touches it":

- from the source, it links units to each other (go-correlator type-checks
  the Go code and links each declaration to what it uses; sql-correlator
  reads the SQL in the code and links it to tables);
- after a test ran, it reads what the test left behind (Go's coverage
  files, a browser's coverage, a database's statement log) and says which
  units the test executed. That is a recording, the subject of [the next
  page](recordings-and-lists.md).

A correlator owns no units. It only refers to units an sbom plugin lists,
by the same ids, which is why the plugins come in pairs that share a small
library for naming things:

| Domain | sbom plugin | Correlators |
|---|---|---|
| Go | go-sbom | go-correlator |
| TypeScript and React | ts-sbom, npm-sbom | react-correlator |
| PostgreSQL | pg-sbom | sql-correlator, gorm-correlator |
| OpenAPI | openapi-sbom | oapi-codegen-correlator |

A correlator is named for the tool whose conventions it follows (React,
GORM, oapi-codegen); an sbom plugin for what it describes.

## When a plugin can't tell

Some things can't be read from files: SQL assembled at run time, a
function called through reflection, a test whose coverage went missing. A
plugin then answers "can't tell" for that piece rather than guess, and
pipelinegen binds wide to make up for it: the unit it couldn't follow
reaches every test that could depend on it. Not knowing always costs runs,
never safety.

Next: [recordings and lists](recordings-and-lists.md).
