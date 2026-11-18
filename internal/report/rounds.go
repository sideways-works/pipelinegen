package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// A continuous run a runner can't plan one build at a time keeps a create-only record of each round:
// every build that decides reads them after writing its own, so of two at once each sees the other.
const (
	// RoundStarted is the record that a build of the round began.
	RoundStarted = "started"
	// RoundPlanning is the record that it stopped waiting and plans.
	RoundPlanning = "planning"

	roundFile = "round.json"
)

var ErrRoundRecorded = errors.New("the round has that record already")

// AllRounds is where every run's round records live.
const AllRounds = refRoot + "rounds/"

func RoundsPrefix(run, change string) string {
	return AllRounds + run + "/" + RefPart(change) + "/"
}

// RoundRef is one record of the round of id: RoundStarted or
// RoundPlanning.
func (id ID) RoundRef(kind string) string {
	return RoundsPrefix(id.Run, id.Change) + short(id.Commit) + "/" + kind
}

type RoundRecord struct {
	Commit string    `json:"commit"`
	At     time.Time `json:"at"`
	JobURL string    `json:"job_url,omitempty"`
}

// RecordRound writes the round's record create-only and returns its version, or ErrRoundRecorded;
// over, when set, is the version the record must have instead.
func RecordRound(ctx context.Context, store *gitstore.Store, id ID, kind, jobURL, over string) (string, error) {
	up, err := RoundUpdate(id, kind, jobURL, over)
	if err != nil {
		return "", err
	}

	written, err := store.Write(ctx, "round "+kind+" "+id.Run+" "+short(id.Commit), up)
	if errors.Is(err, gitstore.ErrStale) {
		return "", fmt.Errorf("%w: %s", ErrRoundRecorded, up.Ref)
	}

	return written[up.Ref], err
}

// RoundUpdate is the write of a record of the round of id, for a writer
// that writes it with others in one write.
func RoundUpdate(id ID, kind, jobURL, over string) (gitstore.Update, error) {
	if err := id.Check(); err != nil {
		return gitstore.Update{}, err
	}

	data, err := json.Marshal(RoundRecord{Commit: id.Commit, At: time.Now().UTC().Truncate(time.Second), JobURL: jobURL})
	if err != nil {
		return gitstore.Update{}, fmt.Errorf("round: %w", err)
	}

	return gitstore.Update{Ref: id.RoundRef(kind), Old: over, Files: map[string][]byte{roundFile: append(data, '\n')}}, nil
}

// Round is what a round's records say: its commit (12 characters), the
// version and time of each record it has, by kind.
type Round struct {
	Commit  string
	Records map[string]gitstore.Ref
}

func (r Round) Last() time.Time {
	var last time.Time

	for _, ref := range r.Records {
		if ref.Time.After(last) {
			last = ref.Time
		}
	}

	return last
}

// Rounds reads a run's round records of a change, by commit.
func Rounds(ctx context.Context, store *gitstore.Store, run, change string) (map[string]Round, error) {
	refs, err := store.List(ctx, RoundsPrefix(run, change))
	if err != nil {
		return nil, err
	}

	out := map[string]Round{}

	for name, ref := range refs {
		commit, kind, ok := strings.Cut(strings.TrimPrefix(name, RoundsPrefix(run, change)), "/")
		if !ok {
			continue
		}

		r, seen := out[commit]
		if !seen {
			r = Round{Commit: commit, Records: map[string]gitstore.Ref{}}
		}

		r.Records[kind] = ref
		out[commit] = r
	}

	return out, nil
}
