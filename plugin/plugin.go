// Package plugin is pipelinegen's plugin protocol: the messages a plugin
// reads and writes, Serve for a plugin's main, and Run for the core. A
// plugin is one executable called as `<executable> <call>`, with a JSON
// request on stdin and a JSON response on stdout.
package plugin

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// Protocol is the protocol version this package speaks.
const Protocol = 1

// The kinds of plugin.
const (
	KindCorrelator = "correlator"
	KindSBOM       = "sbom"
	KindRunner     = "runner"
)

// The calls every plugin answers, and those of each kind.
const (
	CallDescribe = "describe"
	CallUnits    = "units"
	CallLinks    = "links"
	CallBind     = "bind"
	CallObserve  = "observe"
	CallCollect  = "collect"
	CallWhy      = "why"
	CallDocument = "document"
	CallPipeline = "pipeline"
	CallDetect   = "detect"
)

// ExitCantTell is the exit code of a plugin that can't answer for this
// tree or this slice: an answer of its own, apart from a failure and from
// an empty answer.
const ExitCantTell = 3

// Request is what every call carries.
type Request struct {
	Protocol int            `json:"protocol"`
	Work     string         `json:"work"`
	Cache    string         `json:"cache,omitempty"`
	Config   map[string]any `json:"config,omitempty"`
}

// Description is the answer to describe.
type Description struct {
	ConnectorDescription
	DriverDescription

	Protocol     int         `json:"protocol"`
	Kind         string      `json:"kind"`
	Name         string      `json:"name"`
	Version      string      `json:"version"`
	Namespaces   []string    `json:"namespaces,omitempty"`
	Capabilities []string    `json:"capabilities"`
	Narrowings   []Narrowing `json:"narrowings,omitempty"`
	// ResourceTypes are, for a runner, the CI system's own resource types
	// its pipeline needs for a connector's run; a config that says its CI
	// system lacks one is refused.
	ResourceTypes []string `json:"resource_types,omitempty"`
	Needs         []string `json:"needs,omitempty"`
	// Toolchains are the versions the plugin resolved for its needs in
	// the environment it runs in, by need ("go": "go1.27.1"): part of the
	// key a cached bind answer is found by. A plugin with needs and no
	// toolchains for them is never answered from that cache.
	Toolchains map[string]string `json:"toolchains,omitempty"`
	Caches     []string          `json:"caches,omitempty"`
	Candidates []string          `json:"candidates,omitempty"`
	Inputs     map[string]string `json:"inputs,omitempty"`
	// Collects names the collect actions the plugin handles (setup-end,
	// slice-start, slice-end); none named is all three. The core calls
	// collect only for these.
	Collects []string `json:"collects,omitempty"`
	// CacheScopes are, for a runner, how widely each cache its pipeline
	// keeps is shared, by the cache's name (the plan's cache, say): one
	// job's (ScopeJob), every job's of the pipeline (ScopePipeline) or every
	// job's on a worker (ScopeWorker). A plan cache shared beyond one job
	// would let a proposed change's plan leave what a trusted plan reads.
	CacheScopes map[string]string `json:"cache_scopes,omitempty"`
	// Fill says, per cache the plugin declares, how the core fills it on
	// a person's machine before planning there: the plugin itself never
	// reaches the network.
	Fill map[string]Fill `json:"fill,omitempty"`
	// ObserveReads names the config keys whose values are paths the plugin
	// reads while it observes or collects; the core refuses a value outside
	// what the observing step sees.
	ObserveReads []string `json:"observe_reads,omitempty"`
	// ObservesForms name, for a correlator that observes, the forms of
	// recording it reads (a driver's RecordingForms): a recorded suite
	// whose driver leaves a form none of its correlators reads would
	// record nothing they can tell.
	ObservesForms []string `json:"observes_forms,omitempty"`
}

// What the step that observes sees in its working directory: the tree and
// the coverage output, and nothing else. A correlator may rely on these two
// alone.
const (
	ObserveTree     = "source"
	ObserveCoverage = "coverage"
)

// ObserveRead is one value of a key a plugin reads while it observes.
type ObserveRead struct {
	Key   string
	Value string
}

// ObserveReadsOutside are the values of conf's ObserveReads keys that the
// step that observes can't see: an absolute path, one that leaves its
// working directory, or one under neither the tree nor the coverage
// output. A key the config leaves out keeps the plugin's default.
func (d Description) ObserveReadsOutside(conf map[string]any) []ObserveRead {
	var out []ObserveRead

	for _, key := range d.ObserveReads {
		var values []any

		switch v := conf[key].(type) {
		case nil:
		case []any:
			values = v
		default:
			values = []any{v}
		}

		for _, v := range values {
			s, ok := v.(string)
			if !ok {
				out = append(out, ObserveRead{Key: key, Value: fmt.Sprint(v)})

				continue
			}

			if s != "" && !Observable(s) {
				out = append(out, ObserveRead{Key: key, Value: s})
			}
		}
	}

	return out
}

// Observable tells whether a path, relative to the working directory of
// the step that observes, lies in what that step sees.
func Observable(p string) bool {
	p = filepath.ToSlash(p)
	if path.IsAbs(p) || filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return false
	}

	first, _, _ := strings.Cut(path.Clean(p), "/")

	return first == ObserveTree || first == ObserveCoverage
}

// Fill is how a declared cache is filled: Run, a program and its
// arguments, runs in every directory of the tree holding the first of
// Files, once per content of the Files there, with the cache's variable
// pointing at the cache.
type Fill struct {
	Files []string `json:"files"`
	Run   []string `json:"run"`
}

// The scopes of a runner's caches.
const (
	ScopeJob      = "job"
	ScopePipeline = "pipeline"
	ScopeWorker   = "worker"
)

// CollectActions are the actions of collect, in the order a batch meets
// them.
var CollectActions = []string{CollectSetupEnd, CollectSliceStart, CollectSliceEnd}

// Handles tells whether the plugin wants collect called for an action.
func (d Description) Handles(action string) bool {
	return len(d.Collects) == 0 || slices.Contains(d.Collects, action)
}

// Narrowing is one way a plugin narrows a selection, and the lowest level
// that switches it off.
type Narrowing struct {
	Name  string `json:"name"`
	OffAt int    `json:"off_at"`
	Note  string `json:"note"`
}

// Answer is what a call's answer may carry besides its own fields: files
// in the work directory holding it as JSON Lines, and the inputs it
// depended on, for the core's cache.
type Answer struct {
	Files  []string `json:"files,omitempty"`
	Inputs []string `json:"inputs,omitempty"`
	// Cacheable false says the answer depends on something outside its
	// declared inputs (a module missing from the module cache): the core
	// asks again next time.
	Cacheable *bool `json:"cacheable,omitempty"`
}

// The ways an answer depends on its files, as describe's inputs say.
const (
	PerFile = "per-file"
	PerSet  = "per-set"
)

// CannotTell is a unit whose content or links the plugin couldn't work
// out; the core binds it wide.
type CannotTell struct {
	Unit   string `json:"unit"`
	Reason string `json:"reason"`
}

// Set is one cached part of a per-set answer.
type Set struct {
	Group  string   `json:"group"`
	Inputs []string `json:"inputs"`
	// Absent are paths the answer looked for and didn't find: the core keys
	// the set on their absence, so one that appears asks the set again.
	Absent []string `json:"absent,omitempty"`
	// InputUnits, in a correlator's set, are units an sbom lists whose
	// digests the answer depends on besides its input files: a package's
	// links lean on what its imports declare, not on their bodies. The
	// core keys the set on their digests as on its files' blobs.
	InputUnits []string     `json:"input_units,omitempty"`
	Whole      bool         `json:"whole,omitempty"`
	Units      []Unit       `json:"units,omitempty"`
	Links      []Link       `json:"links,omitempty"`
	CannotTell []CannotTell `json:"cannot_tell,omitempty"`
	// DigestFrom, in a correlator's set, says which units' content derives
	// from others, as the answer's own digest_from does; in a set it is
	// cached with the set's files, where the answer's keeps the plugin
	// from being answered from the cache at all.
	DigestFrom []DigestFrom `json:"digest_from,omitempty"`
	// Complete, in an sbom's set, are the files whose units hold everything
	// a test can observe of them when they run as code; a change that leaves
	// every unit as it was reaches no slice bound by recording.
	Complete []string `json:"complete,omitempty"`
}

// Claim is a file a plugin found to be its own, in a role.
type Claim struct {
	Path    string   `json:"path"`
	Role    string   `json:"role"`
	Depends []string `json:"depends,omitempty"`
	ByUnits string   `json:"by_units,omitempty"`
	// Anchor, with ByUnits, is a never-changing unit of the sbom's own that
	// a recording holds whenever it covers this file: only a slice whose
	// list holds it is bound to the units of the file it touched.
	Anchor string `json:"anchor,omitempty"`
	// Recorded, set only by a correlator that observes, says that a
	// recording made with it holds units of this file whenever a slice
	// depends on it (a script the browser runs): a change to a file
	// without it must reach the slices another way.
	Recorded bool `json:"recorded,omitempty"`
	// Erased, never with Recorded and set only by a correlator that
	// observes the app's build, says the build erases this file (it holds
	// nothing but types): it runs in no test, no recording holds it, and a
	// change to it reaches only a type check.
	Erased bool `json:"erased,omitempty"`
}

// DetectRequest asks a plugin which of the candidate paths are its own.
type DetectRequest struct {
	Request

	Tree  string   `json:"tree"`
	Paths []string `json:"paths"`
}

// DetectAnswer is the claimed files.
type DetectAnswer struct {
	Answer

	Claims []Claim `json:"claims"`
}

// DigestFrom says a unit's content derives from other units.
type DigestFrom struct {
	Unit      string   `json:"unit"`
	From      []string `json:"from"`
	Narrowing string   `json:"narrowing,omitempty"`
}

// Unit is something a change can touch and a slice can depend on.
type Unit struct {
	ID         string   `json:"id"`
	Group      string   `json:"group,omitempty"`
	Digest     string   `json:"digest"`
	File       string   `json:"file,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Executable bool     `json:"executable,omitempty"`
	DigestFrom []string `json:"digest_from,omitempty"`
	// Entry is the name a test runner selects the unit by, for a test
	// entry function (TestCreate): a slice that names the tests it runs
	// starts only from the entries among them.
	Entry string `json:"entry,omitempty"`
	// NotRoot keeps a unit from being a starting point of the slice whose
	// file declares it (a type whose test-local twin stands for it there).
	NotRoot bool `json:"not_root,omitempty"`
	// Label is the unit as a developer names it (api.validate,
	// Type.Method, POST /notes), for the reasons a plan prints; without
	// one the core shows a plain form of the id.
	Label      string         `json:"label,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// The link kinds the core understands; any other kind is a plain link.
const (
	LinkReads     = "reads"
	LinkWrites    = "writes"
	LinkUses      = "uses"
	LinkGenerates = "generates"
)

// The typed link kinds: how far a link carries when the graph is walked
// outward from what changed.
const (
	// LinkFollow binds the target and carries on from it: a reference to
	// a type, a constant, a variable or a test function.
	LinkFollow = "follow"
	// LinkBind binds the target and stops there at the executed width.
	LinkBind = "bind"
	// LinkDependentsOnly counts only at the dependents width.
	LinkDependentsOnly = "dependents-only"
)

// TypedKinds are the typed link kinds, sorted.
var TypedKinds = []string{LinkBind, LinkDependentsOnly, LinkFollow}

// Typed tells whether a link has one of the typed kinds.
func (l Link) Typed() bool { return slices.Contains(TypedKinds, l.Kind) }

// Link says that when From is bound, To is bound too.
type Link struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Kind      string `json:"kind,omitempty"`
	Narrowing string `json:"narrowing,omitempty"`
	// While is when the link holds: off (the default, a wide link that
	// stands while its narrowing is off) or on (a narrow link that
	// replaces it while the narrowing is on).
	While string `json:"while,omitempty"`
	// IfLinked says the link holds only for slices whose binaries link the
	// target's group: a method reached through an interface, say, exists
	// only where its package is linked in.
	IfLinked bool `json:"if_linked,omitempty"`
	// Ref marks a link that comes from a reference in the source, apart from
	// a type's methods and an interface's implementations: only a reference
	// makes a new name count as bound.
	Ref bool `json:"ref,omitempty"`
}

// The namespaces the core owns, besides file:. A link may name an id in
// them without any sbom listing it: such an id has no digest, never
// changes and is never dangling.
const (
	// GroupPrefix names a group as a link end, group:<group>: a link into it
	// says the group is linked.
	GroupPrefix = "group:"
	// NodePrefix names a junction that is no unit, node:<namespace>:<rest>,
	// such as the meeting point of an interface method and its
	// implementations.
	NodePrefix = "node:"
)

// CoreOwned tells whether an id is in a namespace the core owns: a group
// or a junction.
func CoreOwned(id string) bool {
	return strings.HasPrefix(id, GroupPrefix) || strings.HasPrefix(id, NodePrefix)
}

// Problem says what is wrong with a link, empty when nothing is: both
// ends named, a known while, a narrowing for a narrow link, and no kind
// that is a misspelling of a kind the core understands (it would be a
// plain link, followed further than the plugin meant).
func (l Link) Problem() string {
	switch {
	case l.From == "" || l.To == "":
		return fmt.Sprintf("link %q to %q: an end is empty", l.From, l.To)
	case l.While != "" && l.While != WhileOff && l.While != WhileOn:
		return fmt.Sprintf("link %s to %s: while %q", l.From, l.To, l.While)
	case l.While == WhileOn && l.Narrowing == "":
		return fmt.Sprintf("link %s to %s: holds while a narrowing is on, but names none", l.From, l.To)
	}

	if known := misspelt(l.Kind); known != "" {
		return fmt.Sprintf("link %s to %s: kind %q, did you mean %q?", l.From, l.To, l.Kind, known)
	}

	return ""
}

func misspelt(kind string) string {
	norm := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == '-' || r == '_' || r == ' ' {
				return -1
			}

			return unicode.ToLower(r)
		}, s)
	}

	for _, k := range slices.Concat(TypedKinds, []string{LinkReads, LinkWrites, LinkUses, LinkGenerates}) {
		if kind != k && norm(kind) == norm(k) {
			return k
		}
	}

	return ""
}

// TreeRequest is the request of units and links.
type TreeRequest struct {
	Request

	Tree       string   `json:"tree"`
	Narrowings []string `json:"narrowings,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	Claimed    []Claim  `json:"claimed,omitempty"`
}

// UnitsAnswer answers units. It may carry the tree's links too, when
// finding them costs the same parse; then the core doesn't call links.
type UnitsAnswer struct {
	Answer

	Units      []Unit       `json:"units,omitempty"`
	Links      []Link       `json:"links,omitempty"`
	WithLinks  bool         `json:"with_links,omitempty"`
	Sets       []Set        `json:"sets,omitempty"`
	CannotTell []CannotTell `json:"cannot_tell,omitempty"`
	Complete   []string     `json:"complete,omitempty"`
}

// LinksAnswer answers links.
type LinksAnswer struct {
	Answer

	Links      []Link       `json:"links,omitempty"`
	Sets       []Set        `json:"sets,omitempty"`
	CannotTell []CannotTell `json:"cannot_tell,omitempty"`
	DigestFrom []DigestFrom `json:"digest_from,omitempty"`
}

// The values of Link.While.
const (
	WhileOff = "off"
	WhileOn  = "on"
)

// ObserveRequest asks what a slice touched.
type ObserveRequest struct {
	Request

	Tree  string   `json:"tree"`
	Slice string   `json:"slice"`
	Dirs  []string `json:"dirs"`
	// Claimed are the plugin's own claims in the tree, as in units and
	// links, when the core has them: a plugin reads the files it needs from
	// these and walks nothing.
	Claimed []Claim `json:"claimed,omitempty"`
}

// ObserveAnswer is the unit ids a slice touched.
type ObserveAnswer struct {
	Answer

	Touched   []string `json:"touched"`
	SetupLike []string `json:"setup_like,omitempty"`
}

// The actions of collect.
const (
	CollectSetupEnd   = "setup-end"
	CollectSliceStart = "slice-start"
	CollectSliceEnd   = "slice-end"
)

// CollectRequest asks a plugin to gather what a batch's slice left behind.
type CollectRequest struct {
	Request

	Action string `json:"action"`
	Slice  string `json:"slice,omitempty"`
	Dir    string `json:"dir"`
	// Stack is set when the suite runs long-lived processes: then finding
	// none to ask is a failure, not an empty answer.
	Stack   bool    `json:"stack,omitempty"`
	Claimed []Claim `json:"claimed,omitempty"`
}
