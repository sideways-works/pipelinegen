// Package s3test is an S3 bucket in memory behind an httptest server, for tests: it checks each
// request's signature on its own, records the requests, and answers PUT, ListObjectsV2 (with
// pages and a delimiter) and DELETE.
package s3test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Object is one stored object: its body as sent and the headers it was put with.
type Object struct {
	Body   []byte
	Header http.Header
}

// Request is one request the server answered, its key without the bucket.
type Request struct {
	Method string
	Key    string
	Query  map[string]string
	Header http.Header
	Status int
}

// Server is one bucket. Its fields are set before the first request.
type Server struct {
	*httptest.Server

	Bucket    string
	PathStyle bool
	Region    string
	KeyID     string
	Secret    string
	// PageSize is how many keys and prefixes a listing page holds at most; 0 is a thousand.
	PageSize int
	// Fail, when it gives a status, answers the request with it instead.
	Fail func(r Request) int

	mu       sync.Mutex
	objects  map[string]Object
	requests []Request
}

// New starts a server for bucket; t stops it when the test ends.
func New(t *testing.T, bucket string, pathStyle bool) *Server {
	t.Helper()

	s := &Server{Bucket: bucket, PathStyle: pathStyle, Region: "eu-central-1", KeyID: "AKIDTEST", Secret: "secret/test+key", objects: map[string]Object{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)

	return s
}

// Objects are the bucket's objects by key.
func (s *Server) Objects() map[string]Object {
	s.mu.Lock()
	defer s.mu.Unlock()

	return maps.Clone(s.objects)
}

// Keys are the bucket's keys, sorted.
func (s *Server) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

// Put stores an object as if a request had.
func (s *Server) Put(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.objects[key] = Object{Header: http.Header{}}
}

// Requests are the requests answered so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.requests)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	key, ok := s.key(r)
	if !ok {
		s.answer(w, Request{Method: r.Method}, http.StatusNotFound, "NoSuchBucket")

		return
	}

	query := map[string]string{}
	for k, vs := range r.URL.Query() {
		query[k] = vs[0]
	}

	req := Request{Method: r.Method, Key: key, Query: query, Header: r.Header.Clone()}

	if code := s.verify(r, body); code != "" {
		s.answer(w, req, http.StatusForbidden, code)

		return
	}

	if s.Fail != nil {
		if status := s.Fail(req); status != 0 {
			s.answer(w, req, status, "InternalError")

			return
		}
	}

	switch {
	case r.Method == http.MethodPut && key != "":
		s.mu.Lock()
		s.objects[key] = Object{Body: body, Header: r.Header.Clone()}
		s.mu.Unlock()
		s.answer(w, req, http.StatusOK, "")
	case r.Method == http.MethodDelete && key != "":
		s.mu.Lock()
		delete(s.objects, key)
		s.mu.Unlock()
		s.answer(w, req, http.StatusNoContent, "")
	case r.Method == http.MethodGet && key == "" && query["list-type"] == "2":
		s.list(w, req)
	default:
		s.answer(w, req, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

func (s *Server) key(r *http.Request) (string, bool) {
	path := strings.TrimPrefix(r.URL.Path, "/")

	if s.PathStyle {
		bucket, key, _ := strings.Cut(path, "/")

		return key, bucket == s.Bucket
	}

	return path, strings.HasPrefix(r.Host, s.Bucket+".")
}

func (s *Server) answer(w http.ResponseWriter, req Request, status int, code string) {
	req.Status = status

	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	if code == "" {
		w.WriteHeader(status)

		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	// as S3 does, the refusal echoes what it signed: a client must never print it
	if _, err := fmt.Fprintf(w, "<Error><Code>%s</Code><Message>refused</Message><AWSAccessKeyId>%s</AWSAccessKeyId>"+
		"<StringToSign>secret material</StringToSign></Error>", code, s.KeyID); err != nil {
		return
	}
}

type listResult struct {
	XMLName  xml.Name `xml:"ListBucketResult"`
	Contents []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken,omitempty"`
}

// tokenPrefix holds the characters a client must encode in a query.
const tokenPrefix = "page+/= "

type entry struct {
	name   string
	folded bool
}

// entries are the keys under prefix in order, those past the delimiter folded into one prefix each.
func (s *Server) entries(prefix, delim string) []entry {
	var out []entry

	seen := map[string]bool{}

	for _, k := range s.Keys() {
		if !strings.HasPrefix(k, prefix) {
			continue
		}

		rest := k[len(prefix):]
		if i := strings.Index(rest, delim); delim != "" && i >= 0 {
			p := prefix + rest[:i+len(delim)]
			if !seen[p] {
				seen[p] = true
				out = append(out, entry{p, true})
			}

			continue
		}

		out = append(out, entry{k, false})
	}

	return out
}

func (s *Server) list(w http.ResponseWriter, req Request) {
	entries := s.entries(req.Query["prefix"], req.Query["delimiter"])
	start := 0

	if tok, ok := req.Query["continuation-token"]; ok {
		n, err := strconv.Atoi(strings.TrimPrefix(tok, tokenPrefix))
		if !strings.HasPrefix(tok, tokenPrefix) || err != nil {
			s.answer(w, req, http.StatusBadRequest, "InvalidArgument")

			return
		}

		start = n
	}

	size := s.PageSize
	if size == 0 {
		size = 1000
	}

	end := min(start+size, len(entries))

	var res listResult

	for _, e := range entries[start:end] {
		if e.folded {
			res.CommonPrefixes = append(res.CommonPrefixes, struct {
				Prefix string `xml:"Prefix"`
			}{e.name})
		} else {
			res.Contents = append(res.Contents, struct {
				Key string `xml:"Key"`
			}{e.name})
		}
	}

	if end < len(entries) {
		res.IsTruncated, res.NextContinuationToken = true, tokenPrefix+strconv.Itoa(end)
	}

	data, err := xml.Marshal(res)
	if err != nil {
		s.answer(w, req, http.StatusInternalServerError, "InternalError")

		return
	}

	req.Status = http.StatusOK

	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/xml")

	if _, err := w.Write(data); err != nil {
		return
	}
}

const accessDenied = "AccessDenied"

// verify works out the request's signature from what arrived, independently of the client, and
// names what is wrong with it.
func (s *Server) verify(r *http.Request, body []byte) string {
	auth := r.Header.Get("Authorization")

	const alg = "AWS4-HMAC-SHA256 "
	if !strings.HasPrefix(auth, alg) {
		return accessDenied
	}

	fields := map[string]string{}

	for f := range strings.SplitSeq(strings.TrimPrefix(auth, alg), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(f), "=")
		fields[k] = v
	}

	sum := sha256.Sum256(body)
	if r.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(sum[:]) {
		return "XAmzContentSHA256Mismatch"
	}

	date := r.Header.Get("X-Amz-Date")
	if len(date) != len("20060102T150405Z") {
		return accessDenied
	}

	scope := date[:8] + "/" + s.Region + "/s3/aws4_request"
	if fields["Credential"] != s.KeyID+"/"+scope {
		return "InvalidAccessKeyId"
	}

	signed := strings.Split(fields["SignedHeaders"], ";")
	for _, must := range []string{"host", "x-amz-content-sha256", "x-amz-date"} {
		if !slices.Contains(signed, must) {
			return accessDenied
		}
	}

	var canon strings.Builder

	canon.WriteString(r.Method + "\n" + uriEncode(r.URL.Path, false) + "\n" + canonicalQuery(r) + "\n")

	for _, h := range signed {
		v := r.Header.Get(h)
		if h == "host" {
			v = r.Host
		}

		canon.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}

	canon.WriteString("\n" + fields["SignedHeaders"] + "\n" + r.Header.Get("X-Amz-Content-Sha256"))

	h := sha256.Sum256([]byte(canon.String()))
	toSign := "AWS4-HMAC-SHA256\n" + date + "\n" + scope + "\n" + hex.EncodeToString(h[:])

	key := []byte("AWS4" + s.Secret)
	for _, part := range []string{date[:8], s.Region, "s3", "aws4_request", toSign} {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(part))
		key = m.Sum(nil)
	}

	if !hmac.Equal([]byte(fields["Signature"]), []byte(hex.EncodeToString(key))) {
		return "SignatureDoesNotMatch"
	}

	return ""
}

func canonicalQuery(r *http.Request) string {
	q := r.URL.Query()

	names := make([]string, 0, len(q))
	for k := range q {
		names = append(names, k)
	}

	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, k := range names {
		parts = append(parts, uriEncode(k, true)+"="+uriEncode(q.Get(k), true))
	}

	return strings.Join(parts, "&")
}

func uriEncode(s string, slash bool) string {
	var b strings.Builder

	for _, c := range []byte(s) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 || c == '/' && !slash {
			b.WriteByte(c)

			continue
		}

		fmt.Fprintf(&b, "%%%02X", c)
	}

	return b.String()
}
