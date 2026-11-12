package proof

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Pins are the releases a proof plans with: the tool's and the Go
// plugins' versions and linux-amd64 checksums.
type Pins struct {
	ToolVersion, ToolSHA             string
	SBOMVersion, SBOMSHA             string
	CorrelatorVersion, CorrelatorSHA string
}

// WriteFixture writes the fixture repository the proof plans on into dir: a
// Bazel workspace with one small Go module, its config pinning the Go
// plugins, and a git repository with no commit yet. Each Bazel server's Java
// heap is heap (1g when empty), since the proof runs several side by side.
func WriteFixture(dir, outputRoot, heap string, pins Pins) error {
	if heap == "" {
		heap = "1g"
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("fixture: %w", err)
	}

	git := [][]string{{"init", "-q", "-b", mainBranch}}
	for _, kv := range [][2]string{{"user.name", "fixture"}, {"user.email", "fixture@example.com"}, {"commit.gpgsign", "false"}} {
		git = append(git, []string{"config", kv[0], kv[1]})
	}

	for _, args := range git {
		cmd := exec.Command(gitCmd, args...) //nolint:noctx,gosec // a quick local git call, its arguments the fixture's own
		cmd.Dir = dir

		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("fixture: git %s: %w: %s", strings.Join(args, " "), err, out)
		}
	}

	config := strings.NewReplacer(
		"$output_root", outputRoot, "$heap", heap,
		"$tool_version", pins.ToolVersion, "$tool_sha", pins.ToolSHA,
		"$sbom_version", pins.SBOMVersion, "$sbom_sha", pins.SBOMSHA,
		"$correlator_version", pins.CorrelatorVersion, "$correlator_sha", pins.CorrelatorSHA,
	).Replace(fixtureConfig)

	files := map[string]string{
		".bazelversion":    "9.2.0\n",
		".bazelignore":     ".pipelinegen\n",
		".gitignore":       ".pipelinegen/\n",
		".bazelrc":         "common --lockfile_mode=off\n",
		"pad/pad.s":        "",
		"pipelinegen.yaml": config,
	}

	maps.Copy(files, fixtureFiles)

	for name, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return fmt.Errorf("fixture: %w", err)
		}

		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}

		if err := os.WriteFile(p, []byte(text), mode); err != nil {
			return fmt.Errorf("fixture: %w", err)
		}

		// the mode as given, whatever the umask took off
		if err := os.Chmod(p, mode); err != nil {
			return fmt.Errorf("fixture: %w", err)
		}
	}

	return nil
}

const fixtureConfig = `pipeline: fixture
bazel:
  workspace: .
  bazelisk: bazelisk
  startup_flags: [--output_user_root=$output_root, --max_idle_secs=120, --host_jvm_args=-Xmx$heap]
github: {repository: example/fixture, token: github_token}
concourse:
  url: https://ci.example.com
  repository: {uri: https://example.com/fixture.git}
  planner:
    image: {repository: golang, tag: 1.27.1}
    digest: sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2
    bootstrap:
      release: {version: $tool_version, sha256: $tool_sha}
      bazelisk: {version: v1.30.0, sha256: a248ae2b2b62b5baebb748f6fba7203bff1a27e5d01b55399a8406744cb539e8}
narrowing: 2
# a plugin's entry lives here: its change samples, and what the entry
# reaches runs on its own
sample_on: [pipelinegen.yaml]
suites:
  go:
    tests: ["**/*_test.go"]
    bind: recorded
    sample: 1
    run_slice: ci/go-batch.yml
runs:
  full:
    trigger: {branch: main}
    compare: none
    select: all
    suites: {go: {}}
    record: true
plugins:
  - name: go-sbom
    kind: sbom
    repository: sideways-works/go-sbom
    release: {version: $sbom_version, sha256: $sbom_sha}
    config: {modules: [.]}
  - name: go-correlator
    kind: correlator
    repository: sideways-works/go-correlator
    release: {version: $correlator_version, sha256: $correlator_sha}
    config: {modules: [.]}
`

// fixtureFiles are the fixture's other files; ci/go-batch.sh records slices
// as a CI worker's batch task would, and the FIXTURE_ variables make the
// cases whose coverage is lost, foreign or empty.
var fixtureFiles = map[string]string{
	"MODULE.bazel": `module(name = "fixture")

local_repository = use_repo_rule("@bazel_tools//tools/build_defs/repo:local.bzl", "local_repository")

local_repository(name = "pipelinegen_deps", path = ".pipelinegen/deps")
`,
	"BUILD.bazel": `# One target owns every tracked file, so no change is to a file nobody
# owns.
filegroup(
    name = "all",
    srcs = glob(["**"]),
)
`,
	"go.mod": `module example.com/fixture

go 1.27.1

require github.com/google/go-cmp v0.6.0
`,
	"app/config.go": `package app

// Config is what Start reads to make a server.
type Config struct {
	Name string
	Port int
}

// defaultPort is the port a server takes when nothing else is said.
const defaultPort = 8080
`,
	"app/start.go": `package app

// Server is a started application.
type Server struct {
	cfg Config
}

// Start makes a server. Every test starts one.
func Start(name string) *Server {
	return &Server{cfg: Config{Name: name, Port: defaultPort}}
}
`,
	"app/registry.go": `package app

import "os"

// registry is filled when the package loads, from the environment.
var registry = load()

func load() []string {
	return []string{os.Getenv("FIXTURE_NAME")}
}
`,
	"app/greet.go": `package app

// Greet says hello to someone.
func Greet(who string) string {
	return "hello, " + who
}
`,
	"app/price.go": `package app

// Total adds prices up. A negative sum is a refund, which no test makes.
func Total(prices ...int) int {
	sum := 0
	for _, p := range prices {
		sum += p
	}

	if sum < 0 {
		return refund(sum)
	}

	hook(0)

	return sum
}

// Opt is how a total went. Only hook's signature names it.
type Opt int

// hook is where a caller could learn how a total went; it does nothing
// yet, so coverage never shows it.
func hook(o Opt) {}

// refund settles a negative total. Only Total names it, and no test runs
// it.
func refund(sum int) int {
	return -rounded(-sum)
}

// rounded rounds down to whole tens. Only refund names it.
func rounded(n int) int {
	return n / 10 * 10
}

// Discount takes a tenth off a total. Only the price tests use it.
func Discount(total int) int {
	return total - total/10
}
`,
	greetTest: `package app

import "testing"

func TestGreet(t *testing.T) {
	if Start("greet") == nil {
		t.Fatal("no server")
	}

	if got := Greet("you"); got != "hello, you" {
		t.Fatalf("Greet: %q", got)
	}
}
`,
	"app/price_test.go": `package app

import "testing"

func TestTotal(t *testing.T) {
	if Start("price") == nil {
		t.Fatal("no server")
	}

	if got := Total(10, 20); got != 30 {
		t.Fatalf("Total: %d", got)
	}
}

func TestDiscount(t *testing.T) {
	if Start("price") == nil {
		t.Fatal("no server")
	}

	if got := Discount(Total(50, 50)); got != 90 {
		t.Fatalf("Discount: %d", got)
	}
}
`,
	"report/report.go": `// Package report writes receipt lines.
package report

import (
	"fmt"

	"example.com/fixture/app"
	"example.com/fixture/pad"
)

// Line is one line of a receipt.
func Line(who string, total int) string {
	return pad.Right(fmt.Sprintf("%s: %d", app.Greet(who), total))
}
`,
	"pad/pad.go": `// Package pad pads receipt lines. Its empty assembly file makes the
// package one the plugin can't follow, so it is bound whole.
package pad

import "example.com/fixture/limits"

// Right pads nothing yet: it returns the line as it is.
func Right(line string) string {
	if len(line) > 1000 {
		return clip(line)
	}

	return line
}

// clip shortens an overlong line; no test makes one.
func clip(line string) string {
	return line[:limits.Width]
}
`,
	"limits/limits.go": `// Package limits holds the receipt's limits.
package limits

// Width is the widest line; only pad's clip, which no test runs, uses it.
const Width = 1000
`,
	"report/report_test.go": `package report

import (
	"testing"

	"example.com/fixture/app"
	"github.com/google/go-cmp/cmp"
)

func TestLine(t *testing.T) {
	if app.Start("report") == nil {
		t.Fatal("no server")
	}

	if diff := cmp.Diff("hello, ann: 3", Line("ann", 3)); diff != "" {
		t.Fatal(diff)
	}
}
`,
	"unused/unused.go": `// Package unused is a package nothing imports.
package unused

// Reverse turns a string around.
func Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}

	return string(r)
}
`,
	"ci/go-batch.sh": `#!/bin/sh
# One batch of the go suite: each slice's tests in their own go test run,
# with their own coverprofile in the slice's coverage directory, where the
# correlators' collect finds what is theirs.
set -u
pg=tools/pipelinegen
"$pg" slice setup-end
"$pg" slice list | while IFS="$(printf '\t')" read -r name label target sources tests; do
	"$pg" slice start "$name"
	mkdir -p "result/$(dirname "$name")" "coverage/$name"
	pkg=./$(dirname "$name")
	run="^($(printf '%s' "$tests" | tr ',' '|'))\$"
	if (cd source && go test -cover -covermode=atomic -coverpkg=example.com/fixture/... \
		-coverprofile="../coverage/$name/tests.coverprofile" -run "$run" "$pkg") >"result/$name.log" 2>&1; then
		echo '{"passed": true}' >"result/$name.json"
	else
		echo '{"passed": false, "failures": [{"test": "go test", "tail": "see the log"}]}' >"result/$name.json"
	fi
	# the proof's untold slice: its coverage files are gone before its end
	if [ "${FIXTURE_LOSE_COVERAGE:-}" = "$name" ]; then
		find "coverage/$name" -type f -delete
	fi
	# the proof's foreign slice: its coverage names a module the tree lacks
	if [ "${FIXTURE_FOREIGN_COVERAGE:-}" = "$name" ]; then
		sed -i 's|^example.com/fixture/|example.com/elsewhere/|' "coverage/$name/tests.coverprofile"
	fi
	# the proof's empty slice: its tests ran, and its coverage counts nothing
	if [ "${FIXTURE_ZERO_COVERAGE:-}" = "$name" ]; then
		sed -i '/^mode:/!s| [0-9][0-9]*$| 0|' "coverage/$name/tests.coverprofile"
	fi
	"$pg" slice end "$name"
done
`,
	"ci/go-batch.yml": `platform: linux
inputs: [{name: source}, {name: batches}, {name: tools}]
outputs: [{name: result}, {name: coverage}]
run: {path: source/ci/go-batch.sh}
`,
}
