package plugin

import (
	"encoding/json"
	"time"
)

// KindConnector is a plugin that knows where code lives: a git host's
// API (proposed changes, commit statuses, comments) or a plain git server.
const KindConnector = "connector"

// The calls of a connector. CallLinks, shared with correlators, answers a
// connector's web links.
const (
	CallIdentify = "identify"
	CallChanges  = "changes"
	CallStatus   = "status"
	CallStatuses = "statuses"
	CallComment  = "comment"
	CallSubject  = "subject"
)

// ConnectorCalls are a connector's calls besides describe, in a stable
// order.
var ConnectorCalls = []string{CallIdentify, CallChanges, CallStatus, CallStatuses, CallComment, CallSubject, CallLinks}

// CallPurposes says, per connector call, the one purpose whose token its
// requests may carry. Identify and links send no requests, and no call
// carries the read token: it is git's credential, the only one a task
// running a proposed change's code may hold.
var CallPurposes = map[string]string{
	CallChanges:  PurposeChanges,
	CallStatus:   PurposeReport,
	CallStatuses: PurposeReport,
	CallComment:  PurposeReport,
	CallSubject:  PurposeReport,
}

// The capabilities a connector's describe may list.
const (
	// CapChanges lists proposed changes.
	CapChanges = "changes"
	// CapStatuses sets and reads commit statuses.
	CapStatuses = "statuses"
	// CapComment keeps one comment on a proposed change.
	CapComment = "comment"
	// CapDraft says whether a change is a draft.
	CapDraft = "draft"
	// CapLabels lists a change's labels.
	CapLabels = "labels"
	// CapReadySince says when a change last became ready for review.
	CapReadySince = "ready_since"
	// CapForks tells a change from a fork apart from one of the same
	// repository; without it every change counts as a fork.
	CapForks = "forks"
	// CapHeadRef fetches a change's head by a ref of its own on the
	// code remote.
	CapHeadRef = "head_ref"
)

// FilterCapabilities says which capability each pull request filter of a
// run needs: a filter the connector can't answer for fails the config,
// since leaving it out would run changes the run means to skip.
var FilterCapabilities = map[string]string{
	"draft":          CapDraft,
	"labels":         CapLabels,
	"labels_without": CapLabels,
	"once_per_ready": CapReadySince,
}

// Where a change comes from. Only a change known to come from the same
// repository as the code runs: unknown counts as a fork.
const (
	FromSame    = "same"
	FromFork    = "fork"
	FromUnknown = "unknown"
)

// The core's states of a commit status; a connector maps them to its
// host's names (describe's states).
const (
	StatePending = "pending"
	StateSuccess = "success"
	StateFailure = "failure"
	StateError   = "error"
)

// States are the core's states of a commit status, in a stable order.
var States = []string{StatePending, StateSuccess, StateFailure, StateError}

// ConnectorDescription is what a connector's describe adds to the common
// one.
type ConnectorDescription struct {
	// APIHosts are the hosts besides the code remote's that requests may
	// go to, as scheme://host[:port]; the config must name each again
	// before any request goes there.
	APIHosts []string `json:"api_hosts,omitempty"`
	// RequestHeaders are the header names a request may set as literals.
	// The core refuses any other, and always Authorization, Cookie, Host
	// and Proxy-*: a credential goes only where the core puts it.
	RequestHeaders  []string       `json:"request_headers,omitempty"`
	ResponseHeaders []string       `json:"response_headers,omitempty"`
	Auth            *ConnectorAuth `json:"auth,omitempty"`
	// Purposes are the purposes the connector needs a token for (read,
	// changes, report); the store's belongs to the store.
	Purposes []string          `json:"purposes,omitempty"`
	GitUser  string            `json:"git_user,omitempty"`
	Limits   *ConnectorLimits  `json:"limits,omitempty"`
	States   map[string]string `json:"states,omitempty"`
	// Accelerators name, per runner, a CI resource of the host's own that
	// notices a new change sooner than the sweep; the runner reads the
	// settings, the core doesn't.
	Accelerators map[string]map[string]any `json:"accelerators,omitempty"`
	// Fallback is the request that sets a status, per core state, with
	// {commit}, {context}, {text} and {url} filled in when the pipeline is
	// made: a hook that reports a broken build sends it without
	// pipelinegen.
	Fallback map[string]HTTPRequest `json:"fallback,omitempty"`
}

// ConnectorAuth says how a token goes on a request: in Header, after
// Scheme and a space when there is a scheme (Authorization: Bearer <token>,
// PRIVATE-TOKEN: <token>).
type ConnectorAuth struct {
	Header string `json:"header"`
	Scheme string `json:"scheme,omitempty"`
}

// ConnectorLimits are what a host takes.
type ConnectorLimits struct {
	StatusTextMax int  `json:"status_text_max,omitempty"`
	CommentMax    int  `json:"comment_max,omitempty"`
	Markdown      bool `json:"markdown,omitempty"`
	Mermaid       bool `json:"mermaid,omitempty"`
}

// ExchangeRequest is one round of a connector call. A connector keeps no
// state between rounds: each round carries the call's arguments again and
// every response of every round before it.
type ExchangeRequest struct {
	Request

	Args      json.RawMessage `json:"args,omitempty"`
	Responses []HTTPResponse  `json:"responses,omitempty"`
}

// Round is a connector's answer to one round: requests for the core to
// send, or Done with the call's answer.
type Round struct {
	Requests []HTTPRequest   `json:"requests,omitempty"`
	Done     json.RawMessage `json:"done,omitempty"`
}

// HTTPRequest is a request a connector asks the core to send.
type HTTPRequest struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Auth    *HTTPAuth         `json:"auth,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// HTTPAuth names the purpose whose token a request carries; it must be
// the call's (CallPurposes).
type HTTPAuth struct {
	Purpose string `json:"purpose"`
}

// HTTPResponse is the host's answer to one request, after the core's
// retries.
type HTTPResponse struct {
	ID       string            `json:"id"`
	Status   int               `json:"status"`
	Headers  map[string]string `json:"headers,omitempty"`
	BodyFile string            `json:"body_file,omitempty"`
}

// IdentifyArgs ask whether a remote is the connector's.
type IdentifyArgs struct {
	Remote string `json:"remote"`
}

// Identity is identify's answer.
type Identity struct {
	Mine       bool   `json:"mine"`
	Repository string `json:"repository,omitempty"`
	Web        string `json:"web,omitempty"`
	GitRemote  string `json:"git_remote,omitempty"`
	// HeadRef is the ref a change's head is fetched by, with {id} for the
	// change's number (refs/pull/{id}/head); empty fetches the head
	// branch and checks the commit.
	HeadRef string `json:"head_ref,omitempty"`
}

// ChangesArgs ask for the open proposed changes onto a base branch.
// Filters are a hint the connector may narrow its listing with; the core
// applies them again to whatever comes back.
type ChangesArgs struct {
	Base    string         `json:"base"`
	Filters ChangesFilters `json:"filters"`
}

// ChangesFilters are a run's pull request filters.
type ChangesFilters struct {
	Draft         *bool    `json:"draft,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	LabelsWithout []string `json:"labels_without,omitempty"`
	Head          string   `json:"head,omitempty"`
	// ReadySince asks for each change's ready_since: the core asks only
	// for a run that tests a change once per ready, since telling it may
	// cost a request per change.
	ReadySince bool `json:"ready_since,omitempty"`
}

// ChangesAnswer is changes' answer.
type ChangesAnswer struct {
	Changes []Change `json:"changes"`
}

// Change is one open proposed change.
type Change struct {
	ID         string   `json:"id"`
	Number     int      `json:"number"`
	Base       string   `json:"base"`
	Head       string   `json:"head"`
	HeadBranch string   `json:"head_branch,omitempty"`
	Draft      bool     `json:"draft,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	// From is FromSame, FromFork or FromUnknown; anything else counts as
	// unknown.
	From       string     `json:"from"`
	Updated    time.Time  `json:"updated"`
	ReadySince *time.Time `json:"ready_since,omitempty"`
}

// StatusArgs set one commit status.
type StatusArgs struct {
	Commit  string `json:"commit"`
	Context string `json:"context"`
	State   string `json:"state"`
	Text    string `json:"text,omitempty"`
	URL     string `json:"url,omitempty"`
}

// StatusesArgs ask for a commit's newest status per context.
type StatusesArgs struct {
	Commit string `json:"commit"`
}

// StatusesAnswer is statuses' answer.
type StatusesAnswer struct {
	Statuses []Status `json:"statuses"`
}

// Status is a commit's newest status in one context, its state in the
// core's names.
type Status struct {
	Context string     `json:"context"`
	State   string     `json:"state"`
	Text    string     `json:"text,omitempty"`
	URL     string     `json:"url,omitempty"`
	Updated *time.Time `json:"updated,omitempty"`
}

// CommentArgs keep one comment on a change: the one whose body holds
// Marker is updated, a new one made when there is none, and others with
// the marker removed.
type CommentArgs struct {
	Change string `json:"change"`
	Marker string `json:"marker"`
	Body   string `json:"body"`
}

// SubjectArgs ask for a commit's subject line.
type SubjectArgs struct {
	Commit string `json:"commit"`
}

// SubjectAnswer is subject's answer.
type SubjectAnswer struct {
	Subject string `json:"subject"`
}

// LinksArgs ask for web links; links sends no requests.
type LinksArgs struct {
	Commit string `json:"commit,omitempty"`
	File   string `json:"file,omitempty"`
	Change string `json:"change,omitempty"`
}

// WebLinks is the connector's links' answer: a page per thing asked
// about, empty where the host has none.
type WebLinks struct {
	Commit string `json:"commit,omitempty"`
	File   string `json:"file,omitempty"`
	Change string `json:"change,omitempty"`
}
