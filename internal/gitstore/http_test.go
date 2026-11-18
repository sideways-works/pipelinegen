package gitstore

import (
	"net/http/cgi" //nolint:gosec // serves the test's own remote; the Go in use is far past the CVE
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// httpRemote serves a bare remote over git's smart HTTP from the test's own server; with anyWant
// false it serves no object by id that no ref advertises, as some hosts don't.
func httpRemote(t *testing.T, anyWant bool) string {
	t.Helper()

	dir := strings.TrimPrefix(bare(t), "file://")

	settings := [][]string{{"http.receivepack", "true"}}
	if !anyWant {
		settings = append(settings, []string{"--unset", "uploadpack.allowAnySHA1InWant"})
	}

	for _, kv := range settings {
		if out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "config"}, kv...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}

	execPath, err := exec.CommandContext(t.Context(), "git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(&cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + filepath.Dir(dir), "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)

	return srv.URL + "/" + filepath.Base(dir)
}

func TestRecordsWrittenOverOthersLandOverHTTP(t *testing.T) {
	for _, anyWant := range []bool{true, false} {
		t.Run(map[bool]string{true: "any id", false: "advertised only"}[anyWant], func(t *testing.T) {
			ctx := t.Context()
			remote := httpRemote(t, anyWant)

			start := groupedStore(t, remote, "1")
			state(t, start, "a.spec.ts", "running")
			state(t, start, "b.spec.ts", "pending")

			end := groupedStore(t, remote, "1")
			state(t, end, "a.spec.ts", "passed")
			state(t, end, "b.spec.ts", "failed")

			if _, err := groupedStore(t, remote, "").Compact(ctx, runsRoot+run+"/", "e2e"); err != nil {
				t.Fatal(err)
			}

			state(t, groupedStore(t, remote, "9"), "a.spec.ts", "errored")

			got := readAll(t, remote)
			for slice, want := range map[string]string{"a.spec.ts": "errored", "b.spec.ts": "failed"} {
				if !strings.Contains(got[stateRef(slice)], "state.json="+want) {
					t.Errorf("%s reads %q", slice, got[stateRef(slice)])
				}
			}
		})
	}
}

func TestAPushOverHTTPIsThinWhateverItIsTold(t *testing.T) {
	remote := httpRemote(t, true)
	dir := filepath.Join(t.TempDir(), "work")
	trace := filepath.Join(t.TempDir(), "trace")

	for _, args := range [][]string{
		{"init", "--quiet", dir},
		{"-C", dir, "commit", "--quiet", "--allow-empty", "-m", "x"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...)

		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b")

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}

	cmd := exec.CommandContext(t.Context(), "git", "-C", dir, "push", "--quiet", "--no-thin", remote, "HEAD:refs/pipelinegen/test/x")

	cmd.Env = append(os.Environ(), "GIT_TRACE="+trace)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}

	var sendPack string

	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.Contains(line, "send-pack") && strings.Contains(line, "--stateless-rpc") {
			sendPack = line
		}
	}

	if !strings.Contains(sendPack, " --thin") {
		t.Fatalf("send-pack over HTTP without --thin:\n%s", data)
	}
}

func TestSuitesEndingOneAfterAnotherLand(t *testing.T) {
	for _, over := range []string{"file", "http"} {
		t.Run(over, func(t *testing.T) {
			ctx := t.Context()

			remote := bare(t)
			if over == "http" {
				remote = httpRemote(t, false)
			}

			end := func(suite, batch, body string) {
				t.Helper()

				ref := runsRoot + run + "/" + suite + "/" + body + ".spec.ts/="
				if _, err := groupedStore(t, remote, batch).Write(ctx, "state", Update{Ref: ref, Any: true, Files: files("state.json", body)}); err != nil {
					t.Fatalf("%s's slice: %v", suite, err)
				}

				s := groupedStore(t, remote, "")
				if _, err := s.Write(ctx, "end", Update{Ref: runsRoot + run + "/_end/" + suite, Any: true, Files: files("end.json", suite+" ended at "+body)}); err != nil {
					t.Fatalf("%s's end: %v", suite, err)
				}

				if _, err := groupedStore(t, remote, "").Compact(ctx, runsRoot+run+"/", suite); err != nil {
					t.Fatalf("%s's compaction: %v", suite, err)
				}
			}

			end("e2e", "1", "first")
			end("unit", "1", "second")
			end("e2e", "9", "third")

			got := readAll(t, remote)
			for name, want := range map[string]string{
				runsRoot + run + "/_end/e2e":              "end.json=e2e ended at third",
				runsRoot + run + "/_end/unit":             "end.json=unit ended at second",
				runsRoot + run + "/e2e/first.spec.ts/=":   "state.json=first",
				runsRoot + run + "/unit/second.spec.ts/=": "state.json=second",
				runsRoot + run + "/e2e/third.spec.ts/=":   "state.json=third",
			} {
				if got[name] != want {
					t.Errorf("%s reads %q, want %q", name, got[name], want)
				}
			}
		})
	}
}
