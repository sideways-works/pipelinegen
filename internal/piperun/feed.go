package piperun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// The files of a session's feed directory ($PIPERUN_FEED): chunks <c>.json from 1, end once the pool
// has nothing left for the session, the session's result lines, and the files its items kept.
const (
	EndFile     = "end"
	ResultsFile = "results.jsonl"
	FilesDir    = "files"
)

// The outcomes an item's end line names.
const (
	Passed  = "passed"
	Failed  = "failed"
	Errored = "errored"
)

// Chunk is one chunk of a session's items, written whole through a rename.
type Chunk struct {
	Chunk int    `json:"chunk"`
	Items []Item `json:"items"`
}

type Item struct {
	Key string `json:"key"`
}

// Feed reads a session's chunks in order. piperun writes chunk c+1 only once every item of chunk c
// has a start, requeue or end line, so a session asks for the next chunk only after reporting each.
type Feed struct {
	Dir string
	// Every is how often a wait for the next chunk looks again.
	Every time.Duration
}

var errChunk = errors.New("chunk")

// Next waits for chunk c and returns it, or false once the end is there and chunk c isn't.
func (f Feed) Next(ctx context.Context, c int) (Chunk, bool, error) {
	every := f.Every
	if every <= 0 {
		every = 100 * time.Millisecond
	}

	for {
		ch, ok, err := f.read(c)
		if err != nil || ok {
			return ch, ok, err
		}

		if _, err := os.Stat(filepath.Join(f.Dir, EndFile)); err == nil {
			// a chunk written just before the end still counts
			ch, ok, err := f.read(c)

			return ch, ok, err
		}

		select {
		case <-ctx.Done():
			return Chunk{}, false, fmt.Errorf("waiting for chunk %d: %w", c, ctx.Err())
		case <-time.After(every):
		}
	}
}

func (f Feed) read(c int) (Chunk, bool, error) {
	p := filepath.Join(f.Dir, strconv.Itoa(c)+".json")

	data, err := os.ReadFile(p) //nolint:gosec // the session's own feed
	if errors.Is(err, os.ErrNotExist) {
		return Chunk{}, false, nil
	}

	if err != nil {
		return Chunk{}, false, fmt.Errorf("%w %d: %w", errChunk, c, err)
	}

	var ch Chunk
	if err := json.Unmarshal(data, &ch); err != nil {
		return Chunk{}, false, fmt.Errorf("%w %s: %w", errChunk, p, err)
	}

	if ch.Chunk != c {
		return Chunk{}, false, fmt.Errorf("%w %s: it says it is chunk %d", errChunk, p, ch.Chunk)
	}

	return ch, true, nil
}

// MaxLine is the longest result line, its newline included: under 4 KB, so one write appends it whole.
const MaxLine = 4095

// End is an item's end line.
type End struct {
	Key        string   `json:"key"`
	Outcome    string   `json:"outcome"`
	Summary    string   `json:"summary,omitempty"`
	Files      []string `json:"files,omitempty"`
	DurationMS int64    `json:"duration_ms,omitempty"`
	CPUSeconds float64  `json:"cpu_seconds,omitempty"`
	PeakMB     int64    `json:"peak_mb,omitempty"`
}

type startLine struct {
	Key   string `json:"key"`
	Start bool   `json:"start"`
}

type requeueLine struct {
	Key     string `json:"key"`
	Requeue bool   `json:"requeue"`
	Reason  string `json:"reason,omitempty"`
}

// Results appends a session's result lines, one write each.
type Results struct {
	Path string

	mu sync.Mutex
}

func (r *Results) Start(key string) error {
	return r.line(startLine{Key: key, Start: true})
}

// Requeue hands an item back to the pool; only before its start line does piperun take it.
func (r *Results) Requeue(key, reason string) error {
	l := requeueLine{Key: key, Requeue: true, Reason: reason}

	data, err := fitText(func(s string) any { l.Reason = s; return l }, reason)
	if err != nil {
		return err
	}

	return r.append(data)
}

// End writes the item's end; a summary too long for one line is cut, and files that still don't fit
// are left out, the last first.
func (r *Results) End(e End) error {
	data, err := EncodeEnd(e)
	if err != nil {
		return err
	}

	return r.append(data)
}

var errLine = errors.New("result line")

// EncodeEnd is the end line as it is written, newline included. Files are left out, the last first,
// until the line holds them with up to keepSummary bytes of the summary; then the summary is cut to fit.
func EncodeEnd(e End) ([]byte, error) {
	summary := e.Summary

	short := summary
	if len(short) > keepSummary {
		n := keepSummary
		for n > 0 && !utf8.RuneStart(short[n]) {
			n--
		}

		short = short[:n]
	}

	for len(e.Files) > 0 {
		e.Summary = short

		data, err := json.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errLine, err)
		}

		if len(data)+1 <= MaxLine {
			break
		}

		e.Files = e.Files[:len(e.Files)-1]
	}

	return fitText(func(s string) any { e.Summary = s; return e }, summary)
}

const keepSummary = 256

const cutMark = " [cut]"

// fitText encodes the line text gives, with the text cut at a rune boundary to the longest that fits:
// JSON's escapes make a byte of text up to six of the line, so the cut is searched for.
func fitText(line func(text string) any, text string) ([]byte, error) {
	encode := func(t string) ([]byte, bool, error) {
		data, err := json.Marshal(line(t))
		if err != nil {
			return nil, false, fmt.Errorf("%w: %w", errLine, err)
		}

		return append(data, '\n'), len(data)+1 <= MaxLine, nil
	}

	data, ok, err := encode(text)
	if err != nil || ok {
		return data, err
	}

	lo, hi := 0, len(text)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		for mid > 0 && !utf8.RuneStart(text[mid]) {
			mid--
		}

		if _, ok, err := encode(text[:mid] + cutMark); err != nil {
			return nil, err
		} else if ok && mid > lo {
			lo = mid
		} else {
			hi = mid - 1
			for hi > lo && !utf8.RuneStart(text[hi]) {
				hi--
			}
		}
	}

	cut := ""
	if lo > 0 {
		cut = text[:lo] + cutMark
	}

	data, ok, err = encode(cut)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, fmt.Errorf("%w: %d bytes without its text, more than %d", errLine, len(data), MaxLine)
	}

	return data, nil
}

func (r *Results) line(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %w", errLine, err)
	}

	if len(data)+1 > MaxLine {
		return fmt.Errorf("%w: %d bytes, more than %d", errLine, len(data)+1, MaxLine)
	}

	return r.append(append(data, '\n'))
}

func (r *Results) append(data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	f, err := os.OpenFile(r.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: %w", errLine, err)
	}

	_, werr := f.Write(data)
	cerr := f.Close()

	if err := errors.Join(werr, cerr); err != nil {
		return fmt.Errorf("%w %s: %w", errLine, r.Path, err)
	}

	return nil
}

// What a session's result lines already say of an item.
const (
	Started  = "start"
	Ended    = "end"
	Requeued = "requeue"
)

// Reported reads what the result lines already in path say of each item: its end, else its last
// requeue or start. A last line without its newline is still being written, and is left out.
func Reported(path string) (map[string]string, error) {
	out := map[string]string{}

	data, err := os.ReadFile(path) //nolint:gosec // the session's own results
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}

	if err != nil {
		return nil, fmt.Errorf("results: %w", err)
	}

	for _, line := range completeLines(data) {
		var l struct {
			Key     string `json:"key"`
			Start   bool   `json:"start"`
			Requeue bool   `json:"requeue"`
			Outcome string `json:"outcome"`
		}

		if json.Unmarshal(line, &l) != nil || l.Key == "" {
			continue
		}

		switch {
		case l.Outcome != "":
			out[l.Key] = Ended
		case out[l.Key] == Ended:
		case l.Requeue:
			out[l.Key] = Requeued
		case l.Start:
			out[l.Key] = Started
		}
	}

	return out, nil
}

func completeLines(data []byte) [][]byte {
	var out [][]byte

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)

	for sc.Scan() {
		out = append(out, bytes.Clone(sc.Bytes()))
	}

	if len(out) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		out = out[:len(out)-1]
	}

	return out
}
