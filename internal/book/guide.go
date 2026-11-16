package book

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const moduleBazel = `module(name = "notes")

bazel_dep(name = "rules_go", version = "0.64.1")
bazel_dep(name = "gazelle", version = "1.0.0-beta.1")

go_sdk = use_extension("@rules_go//go:extensions.bzl", "go_sdk")
go_sdk.download(version = "1.27.1")

go_deps = use_extension("@gazelle//:extensions.bzl", "go_deps")
go_deps.from_file(go_mod = "//:go.mod")
`

const driverEntries = `plugins:
  - name: go
    kind: driver
    repository: sideways-works/pipelinegen-driver-go
    release: {version: v0.2.0, sha256: ea8ef7dbb3b6b8d14fddd3342ab4c6090e0818d8d44b878dcb51ca0cf4720e76}
  - name: playwright
    kind: driver
    repository: sideways-works/pipelinegen-driver-playwright
    release: {version: v0.2.0, sha256: 1062d19c4dfbb2cc4cfddbf1eeb786c9d89a9040c012d495444fdac6e39572f9}
`

const firstConfig = `pipeline: notes

` + driverEntries + `
suites:
  go:
    tests: ["**/*_test.go"]
    steps:
      - run: go
  e2e:
    tests: ["e2e/**/*.spec.ts"]
    owner: "//e2e:e2e"
    steps:
      - run: playwright
        with: {config: e2e/playwright.config.ts}
`

const coverage = `
# A change to the build setup runs every test.
everything_on: [pipelinegen.yaml, MODULE.bazel, MODULE.bazel.lock, .bazelversion, go.mod, go.sum, package.json, package-lock.json, "ci/**"]

untested:
  - paths: [BUILD.bazel]
    reason: holds only Gazelle's own target
  - paths: [.gitignore]
    reason: repository settings
  - paths: ["cmd/notes/**"]
    reason: starts the server; the browser specs run it, bound by their recordings, which the check doesn't read
`

const titleTest = `package text

import "testing"

func TestTitleKeepsShortTitles(t *testing.T) {
	if Title("Milk") != "Milk" {
		t.Fatal("changed a short title")
	}
}
`

const pluginSums = `9bc2115506021ab2f324258a54486951e69873a166fb43c723a8566e508fbb95  tools/plugins/go-sbom
2abdbae3cdb4ed1d04bdc926a417907e36ba01846eb05b02fd30ccb471d6a210  tools/plugins/go-correlator
fb9adfeebe3a4498d12f3b774c76c158026fd4e90c608121b31d067be9a7cbab  tools/plugins/openapi-sbom
155b4bce79efb0b098b3868014e89399f43a749f4496a7c64027b34634ac1bce  tools/plugins/oapi-codegen-correlator
`

const driverSums = `ea8ef7dbb3b6b8d14fddd3342ab4c6090e0818d8d44b878dcb51ca0cf4720e76  tools/plugins/go
1062d19c4dfbb2cc4cfddbf1eeb786c9d89a9040c012d495444fdac6e39572f9  tools/plugins/playwright
`

const pluginEntries = `plugins:
  - name: go-sbom
    kind: sbom
    repository: sideways-works/pipelinegen-sbom-go
    release: {version: v0.2.0, sha256: 9bc2115506021ab2f324258a54486951e69873a166fb43c723a8566e508fbb95}
  - name: go-correlator
    kind: correlator
    repository: sideways-works/pipelinegen-correlator-go
    release: {version: v0.2.0, sha256: 2abdbae3cdb4ed1d04bdc926a417907e36ba01846eb05b02fd30ccb471d6a210}
  - name: openapi-sbom
    kind: sbom
    repository: sideways-works/pipelinegen-sbom-openapi
    release: {version: v0.2.0, sha256: fb9adfeebe3a4498d12f3b774c76c158026fd4e90c608121b31d067be9a7cbab}
  - name: oapi-codegen-correlator
    kind: correlator
    repository: sideways-works/pipelinegen-correlator-oapi-codegen
    release: {version: v0.2.0, sha256: 155b4bce79efb0b098b3868014e89399f43a749f4496a7c64027b34634ac1bce}
    config:
      # the server appends the route of each request it serves here
      request_log: work/command/requests.log
      request_log_lines: patterns
      other_routes: ["GET /"]
`

func bindByRecording(s string) string {
	s = strings.Replace(s, "plugins:\n", pluginEntries, 1)
	s = strings.ReplaceAll(s, "    tests: [\"**/*_test.go\"]\n", "    tests: [\"**/*_test.go\"]\n    bind: recorded\n    correlators: [go-correlator]\n")
	s = strings.ReplaceAll(s, "    owner: \"//e2e:e2e\"\n", "    owner: \"//e2e:e2e\"\n    bind: recorded\n    correlators: [go-correlator, oapi-codegen-correlator]\n")
	s = strings.ReplaceAll(s, "go.mod, go.sum, ", "")
	s = strings.ReplaceAll(s, "  - paths: [\"cmd/notes/**\"]\n    reason: starts the server; the browser specs run it, bound by their recordings, which the check doesn't read\n",
		"  - paths: [cmd/notes/BUILD.bazel]\n    reason: builds the server, which the browser specs run; their recordings bind its code\n")

	return s
}

const buildBazel = `load("@gazelle//:def.bzl", "gazelle")

# gazelle:prefix example.com/notes
gazelle(name = "gazelle")
`

const coverModule = "github.com/sideways-works/pipelinegen-correlator-go/cover@v0.2.0"

const e2eSteps = `    # the specs talk to one server for the whole batch, whose coverage
    # go-correlator takes at each spec's edges
    stack: true
    steps:
      # the server, built once with coverage and go-correlator's cover
      # package
      - prepare: command
        with: {run: [go, build, -tags, pipelinegen_cover, -cover, -covermode=atomic, "-coverpkg=example.com/notes/...", -o, "{prepared}/notes-server", ./cmd/notes]}
      # started once per batch with the seed notes; it stops when the batch
      # ends
      - setup: command
        with:
          start: ["{prepared}/notes-server", -addr, "127.0.0.1:8080", -seed, e2e/seed.json]
          env: {PIPELINEGEN_COVER_DIR: "{work}/ctl", NOTES_REQUEST_LOG: "{work}/requests.log"}
          ready: {http: "http://127.0.0.1:8080/notes", seconds: 30}
          export: {NOTES_URL: "http://127.0.0.1:8080"}
      # one spec file at a time: pipelinegen takes the server's coverage
      # between them
      - run: playwright
        with: {config: e2e/playwright.config.ts}
`

func runTheServer(s string) string {
	s = strings.Replace(s, "    release: {version: v0.2.0, sha256: 2abdbae3cdb4ed1d04bdc926a417907e36ba01846eb05b02fd30ccb471d6a210}\n",
		"    release: {version: v0.2.0, sha256: 2abdbae3cdb4ed1d04bdc926a417907e36ba01846eb05b02fd30ccb471d6a210}\n"+
			"    config:\n      # the running server's counters pass through here between specs\n      cover_dir: work/command/ctl\n", 1)
	s = strings.Replace(s, "  - paths: [cmd/notes/BUILD.bazel]\n    reason: builds the server, which the browser specs run; their recordings bind its code\n",
		"  - paths: [cmd/notes/BUILD.bazel]\n    reason: builds the server, which the browser specs run; their recordings bind its code\n"+
			"  - paths: [cmd/notes/cover.go]\n    reason: links go-correlator's cover package into the e2e suite's coverage build only, which Bazel never builds\n", 1)

	return strings.Replace(s, "    steps:\n      - run: playwright\n        with: {config: e2e/playwright.config.ts}\n", e2eSteps, 1)
}

func (g *guide) withoutCover() error {
	for _, f := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(g.notes, f)) //nolint:gosec // the guide's own copy
		if err != nil {
			return fmt.Errorf("steps: %w", err)
		}

		var keep []string

		for l := range strings.SplitSeq(string(data), "\n") {
			if !strings.Contains(l, "pipelinegen-correlator-go/cover ") {
				keep = append(keep, l)
			}
		}

		if err := g.write(f, strings.Join(keep, "\n")); err != nil {
			return err
		}
	}

	return nil
}

func (g *guide) executable() error {
	entries, err := os.ReadDir(filepath.Join(g.notes, "tools", "plugins"))
	if err != nil {
		return fmt.Errorf("steps: %w", err)
	}

	for _, e := range entries {
		p := filepath.Join(g.notes, "tools", "plugins", e.Name())

		info, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("steps: %w", err)
		}

		if err := os.Chmod(p, info.Mode().Perm()|0o111); err != nil {
			return fmt.Errorf("steps: %w", err)
		}
	}

	return nil
}

func (g *guide) onItsPort(config string) string {
	return strings.ReplaceAll(config, "127.0.0.1:8080", "127.0.0.1:"+g.port)
}

func (g *guide) drivers() error {
	for _, p := range []string{"go", "playwright"} {
		if err := g.copyFile(filepath.Join(g.plugins, p), "tools/plugins/"+p, 0o755); err != nil {
			return err
		}
	}

	return g.write("tools/drivers.sums", driverSums)
}

func (g *guide) copySource(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-z", ".")
	cmd.Dir = g.src

	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("example-notes: %w", err)
	}

	for f := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if f == "" {
			continue
		}

		info, err := os.Stat(filepath.Join(g.src, f))
		if err != nil {
			return fmt.Errorf("example-notes: %w", err)
		}

		if err := g.copyFile(filepath.Join(g.src, f), f, info.Mode().Perm()); err != nil {
			return err
		}
	}

	return nil
}

func do(actions ...func() error) error {
	for _, a := range actions {
		if err := a(); err != nil {
			return err
		}
	}

	return nil
}

func (g *guide) run(ctx context.Context) error {
	if err := os.MkdirAll(g.notes, 0o750); err != nil {
		return fmt.Errorf("steps: %w", err)
	}

	step := func(name, line string) func() error { return func() error { return g.step(ctx, name, line) } }
	commitAll := func(m string) func() error { return func() error { return g.commitAll(ctx, m) } }
	reset := func(to *string) func() error { return func() error { return g.reset(ctx, *to) } }
	cp := func(from, to string) func() error {
		return func() error { return g.copyFile(filepath.Join(g.src, from), to, 0o644) }
	}

	var cfg, plugins, rec string

	head := func(into *string) func() error {
		return func() (err error) {
			*into, err = g.head(ctx)

			return err
		}
	}

	err := do(
		func() error { return g.copySource(ctx) },
		func() error {
			// what the steps add: the config, Bazel's files, the CI's images
			// and the coverage build's hook (step 7)
			for _, f := range []string{"pipelinegen.yaml", "MODULE.bazel", "BUILD.bazel", ".bazelversion", ".gitignore", ".tool-versions", "ci/root.yml", "cmd/notes/cover.go"} {
				if err := os.Remove(filepath.Join(g.notes, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("steps: %w", err)
				}
			}

			return filepath.WalkDir(g.notes, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && d.Name() == "BUILD.bazel" {
					return os.Remove(p) //nolint:gosec // the guide's own copy
				}

				return err
			})
		},
		g.withoutCover,
		func() error { return g.write(".gitignore", "/node_modules/\n/test-results/\n") },
		func() error { return g.git(ctx, "init", "-q", "-b", "main") },
		commitAll("the notes service"),

		// step 1: Bazel
		func() error { return g.write(".bazelversion", "9.2.0\n") },
		func() error { return g.write("MODULE.bazel", moduleBazel) },
		func() error { return g.write("BUILD.bazel", buildBazel) },
		func() error { return g.appendTo(".gitignore", "/bazel-*\n") },
		step("mod-tidy", "bazel mod tidy"),
		step("gazelle", "bazel run //:gazelle"),
		cp("e2e/BUILD.bazel", "e2e/BUILD.bazel"),
		cp("api/BUILD.bazel", "api/BUILD.bazel"),
		cp("api/notesapi/BUILD.bazel", "api/notesapi/BUILD.bazel"),
		step("query", "bazel query //..."),
		commitAll("build the notes service with Bazel"),

		// step 3: first config, and its drivers where pipelinegen finds
		// them
		func() error { return g.write("pipelinegen.yaml", firstConfig) },
		g.drivers,
		step("sha-drivers", "sha256sum -c tools/drivers.sums"),
		func() error { return g.appendTo(".gitignore", "/tools/\n") },
		step("slices", "pipelinegen slices"),
		step("check-first", "pipelinegen check"),
		step("check-summary", "pipelinegen check --summary"),
		func() error { return g.appendTo("pipelinegen.yaml", coverage) },
		step("check-passes", "pipelinegen check"),
		commitAll("pipelinegen config"),
		head(&cfg),

		// step 5: first plan
		g.wordsEdit, commitAll("text: keep hyphenated words whole"),
		step("affected-words", "pipelinegen affected --base HEAD~1"),
		step("why-words-create", "pipelinegen why --base HEAD~1 --slice api/create_test.go"),
		reset(&cfg),
		func() error { return g.write("text/title_test.go", titleTest) },
		commitAll("text: a test of short titles"),
		step("affected-no-target", "pipelinegen affected --base HEAD~1"),
		reset(&cfg),
		func() error { return g.appendTo("go.mod", "// tidy\n") },
		commitAll("go.mod: a comment"),
		step("affected-gomod", "pipelinegen affected --base HEAD~1"),
		reset(&cfg),

		// step 6: plugins
		func() error {
			for _, p := range []string{"go-sbom", "go-correlator", "openapi-sbom", "oapi-codegen-correlator"} {
				if err := g.copyFile(filepath.Join(g.plugins, p), "tools/plugins/"+p, 0o644); err != nil {
					return err
				}
			}

			return g.write("tools/sums", pluginSums)
		},
		step("sha-plugins", "sha256sum -c tools/sums"),
		g.executable,
		func() error {
			data, err := os.ReadFile(filepath.Join(g.notes, "pipelinegen.yaml"))
			if err != nil {
				return fmt.Errorf("steps: %w", err)
			}

			return g.write("pipelinegen.yaml", bindByRecording(string(data)))
		},
		step("check-plugins", "pipelinegen check"),
		step("claims", "pipelinegen plugins claims"),
		commitAll("bind both suites by recording"),
		head(&plugins),
		g.wordsEdit, commitAll("text: keep hyphenated words whole"),
		step("affected-plugins", "pipelinegen affected --base HEAD~1"),
		reset(&plugins),

		// step 7: the suites' steps
		cp("cmd/notes/cover.go", "cmd/notes/cover.go"),
		step("go-get-cover", "go get "+coverModule+" && go mod tidy"),
		cp("BUILD.bazel", "BUILD.bazel"),
		func() error {
			data, err := os.ReadFile(filepath.Join(g.notes, "pipelinegen.yaml"))
			if err != nil {
				return fmt.Errorf("steps: %w", err)
			}

			return g.write("pipelinegen.yaml", g.onItsPort(runTheServer(string(data))))
		},
		step("check-steps", "pipelinegen check"),
		commitAll("run the server for the browser specs"),
	)
	if err != nil {
		return err
	}

	return g.recorded(ctx, step, commitAll, reset, head, &rec, &cfg)
}

func (g *guide) recorded(ctx context.Context, step func(string, string) func() error, commitAll func(string) func() error,
	reset func(*string) func() error, head func(*string) func() error, rec, cfg *string,
) error {
	edit := func(file string, f func(string) string) func() error { return func() error { return g.edit(file, f) } }
	commitTracked := func(m string) func() error { return func() error { return g.commitTracked(ctx, m) } }

	err := do(
		// step 8: record on this machine
		step("record-go", "pipelinegen record --suite go"),
		step("record-e2e", "pipelinegen record --suite e2e"),
		func() error {
			for _, f := range []string{"record-go", "record-e2e"} {
				data, err := os.ReadFile(filepath.Join(g.out, f+".txt")) //nolint:gosec // the guide's own output
				if err != nil || !strings.Contains("\n"+string(data), "\n[exit 0]\n") {
					return fmt.Errorf("%w: %s failed; see %s", ErrStep, f, filepath.Join(g.out, f+".txt"))
				}
			}

			return nil
		},
		head(rec),
		step("lists-show", "pipelinegen lists show"),
		step("lists-show-slice", "pipelinegen lists show --slice e2e/search.spec.ts"),
		edit("api/api.go", sub("a note needs a title", "every note needs a title")), commitAll("api: a clearer message"),
		step("affected-validate", "pipelinegen affected --base HEAD~1"),
		step("why-validate-create", "pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts"),
		step("why-validate-search", "pipelinegen why --base HEAD~1 --slice e2e/search.spec.ts"),
		step("why-validate-ids", "pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts --ids"),
		reset(rec),
		g.wordsEdit, commitAll("text: keep hyphenated words whole"),
		step("affected-words-recorded", "pipelinegen affected --base HEAD~1"),
		reset(rec),
		edit("web/index.html", sub("<h1>Notes</h1>", "<h1>My notes</h1>")), commitAll("web: a friendlier heading"),
		step("affected-page", "pipelinegen affected --base HEAD~1"),
		reset(rec),
		func() error { return g.edit("api/notes.yaml", firstTitleOnly()) },
		commitAll("api: a title has at most 80 characters"),
		step("affected-contract", "pipelinegen affected --base HEAD~1"),
		step("why-contract-spec", "pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts"),
		step("why-contract-test", "pipelinegen why --base HEAD~1 --slice api/create_test.go"),
		reset(rec),
		edit("api/notes.yaml", subRe(`      summary: Adds a note.`, "      summary: Adds a note; it needs a title.")),
		commitAll("api: say that a note needs a title"),
		step("affected-contract-docs", "pipelinegen affected --base HEAD~1"),
		step("why-contract-docs", "pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts"),
		reset(rec),

		// how-to: estimate what you would save: try over four everyday
		// commits on the config of step 4, whose suites are still bound by
		// the graph
		reset(cfg),
		edit("api/api.go", sub("a note needs a title", "every note needs a title")), commitTracked("api: a clearer message"),
		g.wordsEdit, commitTracked("text: keep hyphenated words whole"),
		edit("web/index.html", sub("<h1>Notes</h1>", "<h1>My notes</h1>")), commitTracked("web: a friendlier heading"),
		edit("notes/notes_test.go", sub(`"milk, eggs")`, `"milk, eggs, bread")`)), commitTracked("notes: a longer grocery list in the test"),
		step("try", "pipelinegen try --last 4"),
		reset(rec),

		// step 9: the rest of the config
		func() error {
			data, err := os.ReadFile(filepath.Join(g.src, "pipelinegen.yaml"))
			if err != nil {
				return fmt.Errorf("steps: %w", err)
			}

			return g.write("pipelinegen.yaml", g.onItsPort(string(data)))
		},
		func() error { return g.copyFile(filepath.Join(g.src, "ci", "root.yml"), "ci/root.yml", 0o644) },
		func() error {
			return g.copyFile(filepath.Join(g.plugins, "concourse"), "tools/plugins/concourse", 0o755)
		},
		func() error { return g.appendTo(".gitignore", "/pipeline.yml\n") },
		step("pipeline", "pipelinegen pipeline --out pipeline.yml && pipelinegen pipeline --vars"),
		func() error { return g.countJobs() },
		commitAll("the pipeline's config"),
	)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintln(os.Stdout, "done") //nolint:errcheck // the log is for people

	return nil
}

func firstTitleOnly() func(string) string {
	done := false

	return func(l string) string {
		if !done && l == "        title: {type: string}" {
			done = true

			return "        title: {type: string, maxLength: 80}"
		}

		return l
	}
}

func (g *guide) countJobs() error {
	data, err := os.ReadFile(filepath.Join(g.notes, "pipeline.yml"))

	n := 0

	if err == nil {
		for l := range strings.SplitSeq(string(data), "\n") {
			if strings.HasPrefix(l, "- name:") || strings.HasPrefix(l, "  - name:") {
				n++
			}
		}
	}

	if err := os.WriteFile(filepath.Join(g.out, "pipeline-jobs-count.txt"), fmt.Appendf(nil, "%d\n", n), 0o644); err != nil { //nolint:gosec // the guide's own output
		return fmt.Errorf("steps: %w", err)
	}

	return nil
}
