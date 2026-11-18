// Package packages installs what a suite's steps declare: the install script, the key of an image
// with them installed, that image built locally, and the check of a machine running steps itself.
package packages

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Manager is a package manager the install looks for, and the family whose names it installs.
type Manager struct {
	Command, Family string
}

// Managers are looked for in this order: the first on the image's PATH installs.
var Managers = []Manager{
	{"apt-get", config.FamilyApt},
	{"apk", config.FamilyApk},
	{"dnf", config.FamilyRPM},
	{"microdnf", config.FamilyRPM},
	{"yum", config.FamilyRPM},
}

const (
	EnvCache = plugin.EnvPackageCache
	EnvClean = plugin.EnvPackageClean
)

// Script is a POSIX shell script that installs what the image lacks with its first manager, in a
// subshell, so it sets nothing for what follows and fails when a package can't be installed.
func Script(ps config.Packages) string {
	ps = ps.Sorted()
	if len(ps) == 0 {
		return ""
	}

	var b strings.Builder

	b.WriteString("# pipelinegen: install what the steps need: " + strings.Join(ps.Strings(), ", ") + "\n(\nset -eu\n")

	for i, m := range Managers {
		want, none := spelled(ps, m.Family)

		kw := "elif"
		if i == 0 {
			kw = "if"
		}

		fmt.Fprintf(&b, "%s command -v %s >/dev/null 2>&1; then pg_m=%s pg_want='%s' pg_none='%s'\n", kw, m.Command, m.Command, strings.Join(want, " "), strings.Join(none, ", "))
	}

	commands := make([]string, 0, len(Managers))
	for _, m := range Managers {
		commands = append(commands, m.Command)
	}

	// a message names pipelinegen only as its prefix: the kit reads "pipelinegen <word>" as a call of the tool
	fmt.Fprintf(&b, "else\n  echo \"pipelinegen: this image has none of the package managers %s to install %s\" >&2\n  exit 1\nfi\n",
		strings.Join(commands, ", "), strings.Join(names(ps), " "))
	b.WriteString(scriptBody)

	return b.String()
}

// The body after the manager is found: pg_m is its command, pg_want the names it installs,
// pg_none the packages that name none for its family.
const scriptBody = `if [ -n "$pg_none" ]; then
  echo "pipelinegen: these packages name nothing for $pg_m: $pg_none" >&2
  exit 1
fi
pg_has() {
  case $pg_m in
  apt-get) case "$(dpkg-query -W -f '${Status}' "$1" 2>/dev/null)" in *" ok installed") ;; *) return 1 ;; esac ;;
  apk) apk info -e "$1" >/dev/null 2>&1 ;;
  *) rpm -q "$1" >/dev/null 2>&1 ;;
  esac
}
pg_missing=
for pg_p in $pg_want; do
  pg_has "$pg_p" || pg_missing="$pg_missing $pg_p"
done
[ -n "$pg_missing" ] || exit 0
if [ "$(id -u)" != 0 ]; then
  echo "pipelinegen: can't install$pg_missing with $pg_m: the step runs as user $(id -u), not root" >&2
  exit 1
fi
pg_cache=${PIPELINEGEN_PACKAGE_CACHE:-}
case $pg_cache in ''|/*) ;; *) pg_cache=$PWD/$pg_cache ;; esac
echo "pipelinegen: installing$pg_missing with $pg_m" >&2
case $pg_m in
apt-get)
  set -- -q -o Dpkg::Use-Pty=0 -o APT::Keep-Downloaded-Packages=true
  if [ -n "$pg_cache" ]; then
    mkdir -p "$pg_cache/apt/partial"
    set -- "$@" -o "Dir::Cache::Archives=$pg_cache/apt"
  fi
  DEBIAN_FRONTEND=noninteractive apt-get "$@" update >/dev/null
  DEBIAN_FRONTEND=noninteractive apt-get "$@" install -y --no-install-recommends $pg_missing >/dev/null
  if [ -n "${PIPELINEGEN_PACKAGE_CLEAN:-}" ]; then apt-get clean; rm -rf /var/lib/apt/lists/*; fi
  ;;
apk)
  if [ -n "$pg_cache" ]; then
    mkdir -p "$pg_cache/apk"
    apk add --no-progress -q --cache-dir "$pg_cache/apk" --update-cache $pg_missing
  else
    apk add --no-progress -q --no-cache $pg_missing
  fi
  ;;
*)
  set -- -y
  [ -z "$pg_cache" ] || set -- "$@" --setopt=keepcache=1 "--setopt=cachedir=$pg_cache/$pg_m"
  $pg_m "$@" install $pg_missing >/dev/null
  [ -z "${PIPELINEGEN_PACKAGE_CLEAN:-}" ] || $pg_m clean all >/dev/null
  ;;
esac
for pg_p in $pg_missing; do
  pg_has "$pg_p" || { echo "pipelinegen: $pg_m installed no $pg_p" >&2; exit 1; }
done
)
`

// spelled are the names of ps for a family, and the packages that name none for it.
func spelled(ps config.Packages, family string) (want, none []string) { //nolint:nonamedreturns // two lists of names
	for _, p := range ps {
		n, ok := p.For(family)
		if !ok {
			none = append(none, p.String())

			continue
		}

		if !slices.Contains(want, n) {
			want = append(want, n)
		}
	}

	return want, none
}

// names are the packages as a person reads them: the plain name, else the first family's spelling.
func names(ps config.Packages) []string {
	out := make([]string, 0, len(ps))

	for _, p := range ps {
		n := p.Name
		if n == "" {
			for _, f := range config.Families {
				if s, ok := p.By[f]; ok {
					n = s

					break
				}
			}
		}

		out = append(out, n)
	}

	return out
}

func Names(ps config.Packages) []string { return names(ps.Sorted()) }

// Key is the key of an image with ps installed on base, a digest: the same base and packages in
// any order, installed the same way, give the same key, and any difference another.
func Key(base string, ps config.Packages) string {
	ps = ps.Sorted()

	var b strings.Builder

	b.WriteString("pipelinegen cached image 1\nbase " + base + "\n")

	for _, p := range ps {
		b.WriteString("package " + p.String() + "\n")
	}

	script := sha256.Sum256([]byte(Script(ps)))
	b.WriteString("install " + hex.EncodeToString(script[:]) + "\n")

	key := sha256.Sum256([]byte(b.String()))

	return hex.EncodeToString(key[:])
}
