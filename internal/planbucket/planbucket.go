// Package planbucket copies each plan a trusted step writes to an S3 bucket, under keys a reader
// can list by repository, date and head commit, and deletes the copies older than plans.keep.
// The key layout and the metadata are a contract with what reads the bucket:
// docs/how-to/keeping-plans.md.
package planbucket

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/s3"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

// ContentType is a stored plan's type; its body is the plan's JSON, gzipped.
const ContentType = "application/vnd.pipelinegen.plan+json"

// The user metadata of a stored plan (x-amz-meta-<name>), and of a head's pointer to it.
const (
	MetaVersion           = "version"
	MetaRepository        = "repository"
	MetaRun               = "run"
	MetaChange            = "change"
	MetaHead              = "head"
	MetaBase              = "base"
	MetaAll               = "all"
	MetaSelected          = "selected"
	MetaSlices            = "slices"
	MetaSecondsSelected   = "estimated-seconds-selected"
	MetaSecondsAll        = "estimated-seconds-all"
	MetaPlanKey           = "plan-key"
	headsDir              = "heads/"
	dayFolder             = "2006/01/02/"
	planTime              = "150405"
	headTime              = "20060102T150405"
	nonceBytes            = 4
	defaultCleanupWorkers = 8
)

// Backoff are the waits between the tries of a PUT: a try, and one more after each wait.
var Backoff = []time.Duration{time.Second, 4 * time.Second, 16 * time.Second}

// Bucket is where one repository's plans go.
type Bucket struct {
	Client *s3.Client
	// Base is the key every plan of the repository starts with: <prefix>/<owner>/<repo>/.
	Base       string
	Repository string
	// Keep is how long a plan is kept; 0 deletes none.
	Keep time.Duration
	// Now is the time of the upload, nil time.Now; Sleep waits between tries, nil a timer; Nonce
	// is the random part of the keys, nil 8 random hex digits.
	Now     func() time.Time
	Backoff []time.Duration
	Sleep   func(context.Context, time.Duration) error
	Nonce   func() (string, error)
	Warn    io.Writer
}

// New is the bucket plans.s3 names, for the repository the config names.
func New(cfg *config.Config, creds s3.Credentials) (*Bucket, error) {
	p := cfg.Plans.S3
	if p == nil {
		return nil, errNoBucket
	}

	endpoint, err := url.Parse(p.EndpointURL())
	if err != nil {
		return nil, fmt.Errorf("plans.s3.endpoint: %w", err)
	}

	owner, repo := RepositoryOf(cfg)

	base := owner + "/" + repo + "/"
	if p.Prefix != "" {
		base = p.Prefix + "/" + base
	}

	keep, _ := cfg.Plans.KeepFor()

	return &Bucket{
		Client:     &s3.Client{Endpoint: endpoint, Region: p.Region, Bucket: p.Bucket, PathStyle: p.PathStyle, Creds: creds},
		Base:       base,
		Repository: owner + "/" + repo,
		Keep:       keep,
		Backoff:    Backoff,
	}, nil
}

var errNoBucket = errors.New("no plans.s3 in the config")

// RepositoryOf is the owner and name of the repository the config's forge names: the github
// section's repository, else the last two parts of the code forge's remote or of git.remote,
// else owner "_" and the pipeline's name. Each character outside [A-Za-z0-9._-] becomes "_".
func RepositoryOf(cfg *config.Config) (owner, repo string) {
	if r := cfg.GitHub.Repository; r != "" {
		if o, n, ok := strings.Cut(r, "/"); ok && o != "" && n != "" {
			return Safe(o), Safe(n)
		}
	}

	remotes := []string{cfg.Git.Remote}
	if f := cfg.CodeForge(); f != nil {
		remotes = []string{f.Remote, cfg.Git.Remote}
	}

	for _, remote := range remotes {
		if o, n, ok := remoteRepository(remote); ok {
			return o, n
		}
	}

	return "_", Safe(cfg.Pipeline)
}

// remoteRepository is the owner and name in a remote's path: its last part, less ".git", and the
// parts before it, joined.
func remoteRepository(remote string) (owner, repo string, ok bool) {
	path := remote

	if u, err := url.Parse(remote); err == nil && u.Scheme != "" {
		path = u.Path
	} else if _, p, found := strings.Cut(remote, ":"); found {
		// the scp form, git@host:owner/name.git
		path = p
	}

	parts := strings.FieldsFunc(strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git"), func(r rune) bool { return r == '/' })
	if len(parts) < 2 {
		return "", "", false
	}

	return Safe(strings.Join(parts[:len(parts)-1], "/")), Safe(parts[len(parts)-1]), true
}

var unsafeRE = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Safe is s as one segment of a key: each character outside [A-Za-z0-9._-] replaced by "_", and
// "_" for a segment that would be empty, "." or "..", which some clients read as a path's own.
func Safe(s string) string {
	if s == "" || s == "." || s == ".." {
		return "_"
	}

	return unsafeRE.ReplaceAllString(s, "_")
}

// ascii is a metadata value: each character outside printable ASCII becomes "_".
func ascii(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '_'
		}

		return r
	}, strings.TrimSpace(s))
}

// summary is what the metadata says of a plan, read from the bytes that were written.
type summary struct {
	Version int    `json:"version"`
	Base    string `json:"base"`
	Head    string `json:"head"`
	All     bool   `json:"all"`
	Suites  []struct {
		Selected  []json.RawMessage `json:"selected"`
		Untouched []string          `json:"untouched"`
		Estimate  *struct {
			SelectedSeconds float64 `json:"selected_seconds"`
			AllSeconds      float64 `json:"all_seconds"`
		} `json:"estimate"`
	} `json:"suites"`
}

var headRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Uploaded names what an upload wrote.
type Uploaded struct {
	PlanKey string
	HeadKey string
	Head    string
}

// Upload stores the plan, then the head's pointer to it. A plan that wasn't stored is an error; a
// pointer that wasn't is only warned about, since the plan is listed by date without it.
func (b *Bucket) Upload(ctx context.Context, run, change string, plan []byte) (Uploaded, error) {
	var s summary
	if err := json.Unmarshal(plan, &s); err != nil {
		return Uploaded{}, fmt.Errorf("the plan: %w", err)
	}

	if !headRE.MatchString(s.Head) {
		return Uploaded{}, fmt.Errorf("the plan's head %q is no commit's full hash", ascii(s.Head)) //nolint:err113 // one plan's problem
	}

	nonce, err := b.nonce()
	if err != nil {
		return Uploaded{}, err
	}

	at := b.now().UTC()
	run, change = Safe(run), Safe(change)

	up := Uploaded{
		Head:    s.Head,
		PlanKey: b.Base + at.Format(dayFolder) + run + "/" + change + "/" + at.Format(planTime) + "Z-" + s.Head + "-" + nonce + ".json",
		HeadKey: b.Base + headsDir + s.Head[:2] + "/" + s.Head + "/" + run + "/" + change + "/" + at.Format(headTime) + "Z-" + nonce,
	}

	body, err := gzipped(plan)
	if err != nil {
		return Uploaded{}, err
	}

	header := map[string]string{"Content-Type": ContentType, "Content-Encoding": "gzip"}

	if err := b.put(ctx, up.PlanKey, body, header, b.metadata(s, run, change)); err != nil {
		return Uploaded{}, err
	}

	if err := b.put(ctx, up.HeadKey, nil, nil, map[string]string{MetaPlanKey: up.PlanKey}); err != nil {
		b.warnf("the pointer of head %s: %v", s.Head, err)
	}

	return up, nil
}

func (b *Bucket) metadata(s summary, run, change string) map[string]string {
	selected, slices := 0, 0

	var (
		secsSelected, secsAll float64
		estimated             bool
	)

	for _, sp := range s.Suites {
		selected += len(sp.Selected)
		slices += len(sp.Selected) + len(sp.Untouched)

		if sp.Estimate != nil {
			estimated = true
			secsSelected += sp.Estimate.SelectedSeconds
			secsAll += sp.Estimate.AllSeconds
		}
	}

	meta := map[string]string{
		MetaVersion: strconv.Itoa(s.Version), MetaRepository: ascii(b.Repository), MetaRun: ascii(run), MetaChange: ascii(change),
		MetaHead: s.Head, MetaAll: strconv.FormatBool(s.All), MetaSelected: strconv.Itoa(selected), MetaSlices: strconv.Itoa(slices),
	}

	// an empty value is one some services drop
	if s.Base != "" {
		meta[MetaBase] = ascii(s.Base)
	}

	if estimated {
		meta[MetaSecondsSelected] = strconv.FormatInt(int64(math.Round(secsSelected)), 10)
		meta[MetaSecondsAll] = strconv.FormatInt(int64(math.Round(secsAll)), 10)
	}

	return meta
}

// put tries the PUT once, and again after each of the back-off's waits, whatever failed: a refusal
// S3 gives for a slow upload (400 RequestTimeout) looks like any other.
func (b *Bucket) put(ctx context.Context, key string, body []byte, header, meta map[string]string) error {
	var err error

	for try := 0; ; try++ {
		if err = b.Client.Put(ctx, key, body, header, meta); err == nil || ctx.Err() != nil || try >= len(b.Backoff) {
			return err
		}

		if serr := b.sleep(ctx, b.Backoff[try]); serr != nil {
			return errors.Join(err, serr)
		}
	}
}

// Cleanup deletes, under the repository's key, every day folder whose whole day ended before now
// less Keep, and the pointers of head's shard older than that. It lists level by level, so a
// year or month inside Keep is never listed further. A delete that fails is counted and named in
// the error; the rest go on.
func (b *Bucket) Cleanup(ctx context.Context, head string, now time.Time) error {
	if b.Keep <= 0 {
		return nil
	}

	cutoff := now.UTC().Add(-b.Keep)

	var keys []string

	days, err := b.oldDays(ctx, cutoff)
	errs := []error{err}

	for _, day := range days {
		l, err := b.Client.List(ctx, day, "")
		errs = append(errs, err)
		keys = append(keys, l.Keys...)
	}

	if len(head) >= 2 {
		pointers, err := b.oldPointers(ctx, head[:2], cutoff)
		errs = append(errs, err)
		keys = append(keys, pointers...)
	}

	errs = append(errs, b.deleteAll(ctx, keys))

	return errors.Join(errs...)
}

// oldDays are the day folders whose day ended at or before cutoff.
func (b *Bucket) oldDays(ctx context.Context, cutoff time.Time) ([]string, error) {
	// a period whose first day ended after cutoff holds no day to delete
	ended := func(y, m, d int) bool {
		return !time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1).After(cutoff)
	}

	var (
		out  []string
		errs []error
	)

	years, err := b.levels(ctx, b.Base, 4)
	if err != nil {
		return nil, err
	}

	for _, y := range years {
		if !ended(y.n, 1, 1) {
			continue
		}

		months, err := b.levels(ctx, y.prefix, 2)
		errs = append(errs, err)

		for _, m := range months {
			if m.n < 1 || m.n > 12 || !ended(y.n, m.n, 1) {
				continue
			}

			days, err := b.levels(ctx, m.prefix, 2)
			errs = append(errs, err)

			for _, d := range days {
				// a folder named for no real date isn't a day's
				if t := time.Date(y.n, time.Month(m.n), d.n, 0, 0, 0, 0, time.UTC); t.Day() == d.n && ended(y.n, m.n, d.n) {
					out = append(out, d.prefix)
				}
			}
		}
	}

	return out, errors.Join(errs...)
}

type level struct {
	prefix string
	n      int
}

// levels are the folders right under prefix named by a number of width digits.
func (b *Bucket) levels(ctx context.Context, prefix string, width int) ([]level, error) {
	l, err := b.Client.List(ctx, prefix, "/")
	if err != nil {
		return nil, err
	}

	var out []level

	for _, p := range l.Prefixes {
		name := strings.TrimSuffix(strings.TrimPrefix(p, prefix), "/")
		if len(name) != width || strings.Trim(name, "0123456789") != "" {
			continue
		}

		n, err := strconv.Atoi(name)
		if err != nil {
			continue
		}

		out = append(out, level{p, n})
	}

	return out, nil
}

// oldPointers are the pointers of one shard of the head index made before cutoff, by the time in
// their key.
func (b *Bucket) oldPointers(ctx context.Context, shard string, cutoff time.Time) ([]string, error) {
	l, err := b.Client.List(ctx, b.Base+headsDir+shard+"/", "")
	if err != nil {
		return nil, err
	}

	var out []string

	for _, k := range l.Keys {
		leaf := k[strings.LastIndex(k, "/")+1:]
		if len(leaf) < len(headTime)+1 {
			continue
		}

		t, err := time.Parse(headTime+"Z", leaf[:len(headTime)+1])
		if err == nil && t.Before(cutoff) {
			out = append(out, k)
		}
	}

	return out, nil
}

var errDeletes = errors.New("deletes failed")

// deleteAll deletes keys a few at a time; a key deleted by another step at once is no failure.
func (b *Bucket) deleteAll(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	work := make(chan string)
	failed := make(chan error, len(keys))

	workers := min(defaultCleanupWorkers, len(keys))
	done := make(chan struct{}, workers)

	for range workers {
		go func() {
			for k := range work {
				if err := b.Client.Delete(ctx, k); err != nil {
					failed <- err
				}
			}

			done <- struct{}{}
		}()
	}

	for _, k := range keys {
		work <- k
	}

	close(work)

	for range workers {
		<-done
	}

	close(failed)

	var first error

	n := 0

	for err := range failed {
		if first == nil {
			first = err
		}

		n++
	}

	if n == 0 {
		return nil
	}

	return fmt.Errorf("%w: %d of %d, the first: %w", errDeletes, n, len(keys), first)
}

// Store uploads the plan and, once it is stored, deletes what is older than Keep. It never fails:
// each problem is a warning, and the step goes on.
func (b *Bucket) Store(ctx context.Context, run, change string, plan []byte) {
	up, err := b.Upload(ctx, run, change, plan)
	if err != nil {
		b.warnf("the plan is not in the bucket: %v", err)

		return
	}

	if err := b.Cleanup(ctx, up.Head, b.now()); err != nil {
		b.warnf("cleanup: %v", err)
	}
}

func (b *Bucket) warnf(format string, args ...any) {
	if b.Warn == nil {
		return
	}

	if _, err := fmt.Fprint(b.Warn, secret.Redact(fmt.Sprintf("pipelinegen plan: plans.s3: "+format+"\n", args...))); err != nil {
		return
	}
}

func (b *Bucket) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}

	return time.Now()
}

func (b *Bucket) sleep(ctx context.Context, d time.Duration) error {
	if b.Sleep != nil {
		return b.Sleep(ctx, d)
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("plans.s3: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

func (b *Bucket) nonce() (string, error) {
	if b.Nonce != nil {
		return b.Nonce()
	}

	buf := make([]byte, nonceBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("plans.s3: a nonce: %w", err)
	}

	return hex.EncodeToString(buf), nil
}

func gzipped(data []byte) ([]byte, error) {
	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}

	return buf.Bytes(), nil
}
