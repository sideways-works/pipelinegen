package packages

import (
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

var pg = config.Package{Name: "postgresql-client", By: map[string]string{config.FamilyApk: "postgresql16-client"}}

func TestScriptPerManager(t *testing.T) {
	s := Script(config.Packages{{Name: "jq"}, pg, {Name: "git"}, {Name: "jq"}})

	for _, line := range []string{
		"if command -v apt-get >/dev/null 2>&1; then pg_m=apt-get pg_want='git jq postgresql-client' pg_none=''",
		"elif command -v apk >/dev/null 2>&1; then pg_m=apk pg_want='git jq postgresql16-client' pg_none=''",
		"elif command -v dnf >/dev/null 2>&1; then pg_m=dnf pg_want='git jq postgresql-client' pg_none=''",
		"elif command -v microdnf >/dev/null 2>&1; then pg_m=microdnf pg_want='git jq postgresql-client' pg_none=''",
		"elif command -v yum >/dev/null 2>&1; then pg_m=yum pg_want='git jq postgresql-client' pg_none=''",
		"no package manager pipelinegen knows (apt-get, apk, dnf, microdnf, yum) is in this image to install git jq postgresql-client",
	} {
		if !strings.Contains(s, line) {
			t.Errorf("no line %q in\n%s", line, s)
		}
	}

	if !strings.HasPrefix(s, "# pipelinegen: install what the steps need: git, jq, postgresql-client apk=postgresql16-client\n(\n") || !strings.HasSuffix(s, "\n)\n") {
		t.Fatalf("not one subshell:\n%s", s)
	}

	if Script(nil) != "" {
		t.Fatal("no packages, no script")
	}
}

func TestScriptNamesNoneForAFamily(t *testing.T) {
	s := Script(config.Packages{{By: map[string]string{config.FamilyApk: "build-base"}}})

	if !strings.Contains(s, "then pg_m=apt-get pg_want='' pg_none=' apk=build-base'\n") ||
		!strings.Contains(s, "then pg_m=apk pg_want='build-base' pg_none=''\n") {
		t.Fatal(s)
	}
}

func TestKey(t *testing.T) {
	base := "sha256:" + strings.Repeat("a", 64)
	k := Key(base, config.Packages{{Name: "jq"}, {Name: "git"}})

	if len(k) != 64 || strings.Trim(k, "0123456789abcdef") != "" {
		t.Fatalf("key %q is no hex digest", k)
	}

	same := []config.Packages{
		{{Name: "git"}, {Name: "jq"}},
		{{Name: "jq"}, {Name: "git"}, {Name: "jq"}},
	}
	for _, ps := range same {
		if got := Key(base, ps); got != k {
			t.Errorf("%v: %s, want %s", ps.Strings(), got, k)
		}
	}

	other := map[string]string{
		"another base digest":   Key("sha256:"+strings.Repeat("b", 64), config.Packages{{Name: "jq"}, {Name: "git"}}),
		"another package":       Key(base, config.Packages{{Name: "jq"}, {Name: "git"}, {Name: "make"}}),
		"one package fewer":     Key(base, config.Packages{{Name: "jq"}}),
		"another family's name": Key(base, config.Packages{{Name: "jq"}, {Name: "git", By: map[string]string{config.FamilyApk: "git-lite"}}}),
	}
	for what, got := range other {
		if got == k {
			t.Errorf("%s gives the same key", what)
		}
	}
}

func TestMissing(t *testing.T) {
	have := map[string]bool{"git": true, "postgresql16-client": true}
	ps := config.Packages{{Name: "git"}, {Name: "jq"}, pg, {By: map[string]string{config.FamilyApt: "make"}}}

	got := Missing(ps, func(n string) bool { return have[n] })
	if want := []string{"make", "jq"}; !slices.Equal(got, want) {
		t.Fatalf("Missing: %q, want %q", got, want)
	}
}

func TestOnHost(t *testing.T) {
	present := OnHost(t.Context())

	if !present("sh") {
		t.Fatal("sh is on PATH")
	}

	if present("pipelinegen-no-such-package") {
		t.Fatal("a package no machine has")
	}
}
