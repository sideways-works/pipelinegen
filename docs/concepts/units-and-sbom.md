# Units and sbom plugins

## Unit

A unit is a piece of the repository that a change can touch and a test can
depend on: a Go function, a TypeScript component, a database table, an API
operation, a third-party module. Each unit has an id and a digest.

The id names the unit the same way at every commit. Go functions are named
by their package directory, kind and name:

```
go:text#func:Words
go:notes#method:Store.Add
go:search#func:Find
```

The digest is a hash of what the unit is. For a Go function it covers the
function's code but not its comments or line breaks, so rewording a
comment changes no digest and runs no test. A unit changed when its digest
at the head differs from its digest at the base. The edit to `Words`
changes exactly one digest, that of `go:text#func:Words`.

Units are much finer than Bazel targets. `//api:api` is one target, but it
holds the units `go:api#func:Handler`, `go:api#func:create`,
`go:api#func:validate`, `go:api#func:list` and a few more.

## sbom plugins

An sbom plugin lists the units of the files it understands. The name comes
from "software bill of materials": a list of what something is made of.
Each language or ecosystem has its own: `go-sbom` lists Go declarations
and the Go modules a repository uses, `ts-sbom` TypeScript declarations,
`npm-sbom` npm packages, `pg-sbom` PostgreSQL tables and functions from
migration files, `openapi-sbom` the operations of an OpenAPI document.

An sbom plugin answers "what exists". It finds its own files (go-sbom
claims `.go` files, `go.mod` and `go.sum`), reads them, and returns the
units with their ids and digests. It also says how the units hang together
where the files say so plainly: which file a function is in, which modules
a module requires.

Like every plugin but a driver, an sbom plugin is a plain program: files
in, JSON out.
It starts no server, opens no database and uses no network, so its answer
for a file depends on that file alone. pipelinegen caches every answer by
the file's content and asks again only about files that changed.

`pipelinegen plugins claims` shows which plugin claimed which file. In the
notes service:

<!-- output: claims -->
```
$ pipelinegen plugins claims
go-sbom	MODULE.bazel	directive
go-sbom	api/api.go	source
go-sbom	api/create_test.go	source
go-sbom	api/notesapi/generate.go	source
go-sbom	api/notesapi/notesapi.gen.go	generated
go-sbom	api/search_test.go	source
go-sbom	cmd/notes/main.go	source
go-sbom	go.mod	manifest
go-sbom	go.sum	lockfile
go-sbom	notes/notes.go	source
go-sbom	notes/notes_test.go	source
go-sbom	search/search.go	source
go-sbom	search/search_test.go	source
go-sbom	text/text.go	source
go-sbom	text/text_test.go	source
go-sbom	web/web.go	source
go-correlator	api/api.go	source
go-correlator	api/create_test.go	source
go-correlator	api/notesapi/generate.go	source
go-correlator	api/notesapi/notesapi.gen.go	generated
go-correlator	api/search_test.go	source
go-correlator	cmd/notes/main.go	source
go-correlator	go.mod	manifest
go-correlator	go.sum	lockfile
go-correlator	notes/notes.go	source
go-correlator	notes/notes_test.go	source
go-correlator	search/search.go	source
go-correlator	search/search_test.go	source
go-correlator	text/text.go	source
go-correlator	text/text_test.go	source
go-correlator	web/web.go	source
openapi-sbom	api/notes.yaml	contract
oapi-codegen-correlator	api/api.go	source
oapi-codegen-correlator	api/create_test.go	source
oapi-codegen-correlator	api/notes.yaml	contract
oapi-codegen-correlator	api/notesapi/generate.go	directive
oapi-codegen-correlator	api/notesapi/notesapi.gen.go	generated
oapi-codegen-correlator	api/search_test.go	source
oapi-codegen-correlator	cmd/notes/main.go	source
oapi-codegen-correlator	go.mod	manifest
oapi-codegen-correlator	notes/notes.go	source
oapi-codegen-correlator	notes/notes_test.go	source
oapi-codegen-correlator	search/search.go	source
oapi-codegen-correlator	search/search_test.go	source
oapi-codegen-correlator	text/text.go	source
oapi-codegen-correlator	text/text_test.go	source
oapi-codegen-correlator	web/web.go	source
```

go-correlator, the second plugin in that list, is the subject of the next
page.

Next: [links and correlators](links-and-correlators.md).
