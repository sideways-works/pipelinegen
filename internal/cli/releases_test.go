package cli

import (
	"net/http"
	"net/http/httptest"
	"sync"
)

// testReleases are the release files the tests serve, by path: /<repository>/releases/download/<version>/<file>.
var testReleases = struct {
	sync.Mutex

	files map[string][]byte
}{files: map[string][]byte{}}

func serveReleases() string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		testReleases.Lock()
		data, ok := testReleases.files[req.URL.Path]
		testReleases.Unlock()

		if !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		_, _ = w.Write(data) //nolint:errcheck // a test server
	}))

	return srv.URL
}

func serveRelease(repository, version, file string, data []byte) {
	testReleases.Lock()
	defer testReleases.Unlock()

	testReleases.files["/"+repository+"/releases/download/"+version+"/"+file] = data
}
