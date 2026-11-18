// Package plugin is pipelinegen's plugin protocol: an executable called as `<executable> <call>`,
// a JSON request on stdin and a JSON answer on stdout; Serve for a plugin's main, Run for the core.
package plugin

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

const Protocol = 1

const (
	KindCorrelator = "correlator"
	KindSBOM       = "sbom"
	KindRunner     = "runner"
)

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

// ExitCantTell is the exit code of a plugin that can't answer for this tree or slice: an
// answer of its own, apart from a failure and from an empty answer.
const ExitCantTell = 3

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
	// ResourceTypes are, for a runner, the CI system's resource types its pipeline needs for a
	// connector's run; a config that says its CI system lacks one is refused.
	ResourceTypes []string `json:"resource_types,omitempty"`
	Needs         []string `json:"needs,omitempty"`
	// Toolchains are the versions resolved for the plugin's needs ("go": "go1.27.1"), part of a
	// cached bind answer's key: a plugin with needs and no toolchains is never answered from it.
	Toolchains map[string]string `json:"toolchains,omitempty"`
	Caches     []string          `json:"caches,omitempty"`
	Candidates []string          `json:"candidates,omitempty"`
	Inputs     map[string]string `json:"inputs,omitempty"`
	// Collects names the collect actions the plugin handles, all three when none: the core
	// calls collect only for these.
	Collects []string `json:"collects,omitempty"`
	// CacheScopes say, for a runner, how widely each of its pipeline's caches is shared: a plan
	// cache shared beyond one job would let a proposed change's plan leave what a trusted one reads.
	CacheScopes map[string]string `json:"cache_scopes,omitempty"`
	// Fill says how the core fills each declared cache on a person's machine before planning
	// there: the plugin itself never reaches the network.
	Fill map[string]Fill `json:"fill,omitempty"`
	// ObserveReads names the config keys whose values are paths read while observing or
	// collecting; the core refuses a value outside what the observing step sees.
	ObserveReads []string `json:"observe_reads,omitempty"`
	// ObservesForms are, for an observing correlator, the recording forms it reads: a recorded
	// suite whose driver leaves a form none of its correlators reads records nothing they can tell.
	ObservesForms []string `json:"observes_forms,omitempty"`
}

// What the observing step sees in its working directory, and all a correlator may rely on:
// the tree and the coverage output.
const (
	ObserveTree     = "source"
	ObserveCoverage = "coverage"
)

type ObserveRead struct {
	Key   string
	Value string
}

// ObserveReadsOutside are the values of conf's ObserveReads keys the observing step can't see (absolute,
// leaving it, or under neither the tree nor the coverage output); a key conf leaves out keeps the default.
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

// Fill is how a declared cache is filled: Run runs in every directory of the tree holding the
// first of Files, once per content of the Files there, with the cache's variable pointing at it.
type Fill struct {
	Files []string `json:"files"`
	Run   []string `json:"run"`
}

const (
	ScopeJob      = "job"
	ScopePipeline = "pipeline"
	ScopeWorker   = "worker"
)

// CollectActions are in the order a batch meets them.
var CollectActions = []string{CollectSetupEnd, CollectSliceStart, CollectSliceEnd}

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

// Answer is what any call's answer may carry: files in its work directory holding it as JSON
// Lines, and the inputs it depended on, for the core's cache.
type Answer struct {
	Files  []string `json:"files,omitempty"`
	Inputs []string `json:"inputs,omitempty"`
	// Cacheable false says the answer depends on something outside its declared inputs (a module
	// missing from the module cache): the core asks again next time.
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
	// InputUnits, in a correlator's set, are sbom units whose digests the answer depends on too (a
	// package's links lean on what its imports declare): the set is keyed on them as on its files.
	InputUnits []string     `json:"input_units,omitempty"`
	Whole      bool         `json:"whole,omitempty"`
	Units      []Unit       `json:"units,omitempty"`
	Links      []Link       `json:"links,omitempty"`
	CannotTell []CannotTell `json:"cannot_tell,omitempty"`
	// DigestFrom, in a correlator's set, is the answer's digest_from cached with the set's files;
	// the answer's own keeps the plugin from being answered from the cache at all.
	DigestFrom []DigestFrom `json:"digest_from,omitempty"`
	// Complete, in an sbom's set, are the files whose units hold all a test can observe of them as
	// code: a change that leaves every unit as it was reaches no slice bound by recording.
	Complete []string `json:"complete,omitempty"`
}

// Claim is a file a plugin found to be its own, in a role.
type Claim struct {
	Path    string   `json:"path"`
	Role    string   `json:"role"`
	Depends []string `json:"depends,omitempty"`
	ByUnits string   `json:"by_units,omitempty"`
	// Anchor, with ByUnits, is a never-changing unit a recording holds whenever it covers this
	// file: only a slice whose list holds it is bound to the units of the file it touched.
	Anchor string `json:"anchor,omitempty"`
	// Recorded, set only by an observing correlator, says a recording holds units of this file
	// whenever a slice depends on it (a script the browser runs); without it a change needs another way.
	Recorded bool `json:"recorded,omitempty"`
	// Erased, set only by a correlator observing the app's build, says the build erases this file
	// (it holds only types): no recording holds it, and a change to it reaches only a type check.
	Erased bool `json:"erased,omitempty"`
}

// DetectRequest asks a plugin which of the candidate paths are its own.
type DetectRequest struct {
	Request

	Tree  string   `json:"tree"`
	Paths []string `json:"paths"`
}

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
	// Entry is the name a test runner selects a test entry function by (TestCreate): a slice
	// that names the tests it runs starts only from the entries among them.
	Entry string `json:"entry,omitempty"`
	// NotRoot keeps a unit from being a starting point of the slice whose
	// file declares it (a type whose test-local twin stands for it there).
	NotRoot bool `json:"not_root,omitempty"`
	// Label is the unit as a developer names it (api.validate, Type.Method, POST /notes) for a
	// plan's reasons; without one the core shows a plain form of the id.
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

var TypedKinds = []string{LinkBind, LinkDependentsOnly, LinkFollow}

func (l Link) Typed() bool { return slices.Contains(TypedKinds, l.Kind) }

// Link says that when From is bound, To is bound too.
type Link struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Kind      string `json:"kind,omitempty"`
	Narrowing string `json:"narrowing,omitempty"`
	// While is when the link holds: off (the default, a wide link standing while its narrowing
	// is off) or on (a narrow link replacing it while the narrowing is on).
	While string `json:"while,omitempty"`
	// IfLinked holds the link only for slices whose binaries link the target's group: a method
	// reached through an interface exists only where its package is linked in.
	IfLinked bool `json:"if_linked,omitempty"`
	// Ref marks a link from a reference in the source, not a type's methods or an interface's
	// implementations: only a reference makes a new name count as bound.
	Ref bool `json:"ref,omitempty"`
}

// Namespaces the core owns besides file:. A link may name an id in them that no sbom lists:
// it has no digest, never changes and is never dangling.
const (
	// GroupPrefix names a group as a link end, group:<group>: a link into it
	// says the group is linked.
	GroupPrefix = "group:"
	// NodePrefix names a junction that is no unit, node:<namespace>:<rest>, such as where an
	// interface method meets its implementations.
	NodePrefix = "node:"
)

func CoreOwned(id string) bool {
	return strings.HasPrefix(id, GroupPrefix) || strings.HasPrefix(id, NodePrefix)
}

// Problem says what is wrong with a link, empty when nothing is; a misspelt kind is refused,
// since it would be a plain link, followed further than the plugin meant.
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

type LinksAnswer struct {
	Answer

	Links      []Link       `json:"links,omitempty"`
	Sets       []Set        `json:"sets,omitempty"`
	CannotTell []CannotTell `json:"cannot_tell,omitempty"`
	DigestFrom []DigestFrom `json:"digest_from,omitempty"`
}

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
	// Claimed are the plugin's own claims in the tree, when the core has them: a plugin reads
	// the files it needs from these and walks nothing.
	Claimed []Claim `json:"claimed,omitempty"`
}

// ObserveAnswer is the unit ids a slice touched.
type ObserveAnswer struct {
	Answer

	Touched   []string `json:"touched"`
	SetupLike []string `json:"setup_like,omitempty"`
}

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
