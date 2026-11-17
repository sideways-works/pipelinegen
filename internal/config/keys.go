package config

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// KeyDoc describes one key of the config file for the reference.
type KeyDoc struct {
	Path    string
	Type    string
	Default string
}

var defaults = map[string]string{
	"bazel.workspace":                        ".",
	"bazel.bazelisk":                         "bazelisk",
	"github.status_prefix":                   "pipelinegen",
	"concourse.team":                         "main",
	"concourse.branch":                       defaultBranch,
	"concourse.planner.bootstrap.repository": "sideways-works/pipelinegen",
	"uncovered":                              UncoveredEverything,
	"follow":                                 FollowExecuted,
	"narrowing":                              strconv.Itoa(DefaultNarrowing),
	"narrowing_alarm":                        "2",
	"rebuild_check":                          RebuildError,
	"explain_limit":                          strconv.Itoa(DefaultExplainLimit),
	"keep.runs.for":                          "7d",
	"keep.lists.for":                         "90d",
	"keep.lists.commits":                     strconv.Itoa(DefaultListsCommits),
	"keep.cache":                             string(DefaultCacheBudget),
	"writes.retries":                         strconv.Itoa(DefaultRetries),
	"writes.backoff":                         DefaultBackoff.String(),
	"writes.deadline":                        "10m, or half of limits.steps when that is shorter",
	"store.backend":                          StoreRefs,
	"store.branch":                           DefaultStoreBranch,
	"store.deadline":                         "10m",
	"suites.<name>.bind":                     BindGraph,
	"suites.<name>.parallel":                 strconv.Itoa(DefaultParallel),
	"suites.<name>.lock.slots":               "1",
	"runs.<name>.report.status":              "true",
	"runs.<name>.report.flush":               "30s",
	"runs.<name>.trigger.pull_requests.missed_after": DefaultMissedAfter.String(),
	"bazel_diff.version":                             Pinned.Version,
	"bazel_diff.sha256":                              "the pinned release's, for linux-amd64, linux-arm64 and darwin-arm64",
}

// Pinned is the bazel-diff release the config defaults to; it is checked
// under Bazel 9.2.0.
var Pinned = BazelDiff{
	Version: "v67.0.3",
	// the release publishes no checksum list, and no build for Intel Macs
	SHA256: Checksums{
		LinuxAMD64:  "98d71d5ffc6e03dbe14f6b7f7b132bb6c5704b189065b3fa45915bad001e8e4e",
		LinuxARM64:  "8a3235f10b5823400d7757a390df466637837a167a6bbcc6b0d10502123b29db",
		DarwinARM64: "649e623a05ddf8008f069983de153dc8bc524aec70a17d6035b926751b0cfbed",
	},
}

// Keys lists every key the config file takes, sorted by path.
func Keys() []KeyDoc {
	var out []KeyDoc

	walkKeys(reflect.TypeFor[Config](), "", &out)

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })

	return out
}

func walkKeys(t reflect.Type, prefix string, out *[]KeyDoc) {
	for f := range t.Fields() {
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")

		// an inlined struct's keys are its holder's
		if name == "" && opts == "inline" && f.Type.Kind() == reflect.Struct {
			walkKeys(f.Type, prefix, out)

			continue
		}

		if name == "" || name == "-" {
			continue
		}

		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		*out = append(*out, KeyDoc{Path: path, Type: ft.String(), Default: defaults[path]})

		switch {
		case ft.Kind() == reflect.Struct:
			walkKeys(ft, path, out)
		case ft.Kind() == reflect.Map && structOf(ft.Elem()) != nil:
			walkKeys(structOf(ft.Elem()), path+".<name>", out)
		case ft.Kind() == reflect.Slice && structOf(ft.Elem()) != nil:
			walkKeys(structOf(ft.Elem()), path+"[]", out)
		}
	}
}

func structOf(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t.Kind() == reflect.Struct {
		return t
	}

	return nil
}
