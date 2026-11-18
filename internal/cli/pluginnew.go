package cli

import (
	"archive/tar"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/forge"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/plugin"
	"github.com/sideways-works/plugkit/semver"
)

// PluginsRepository holds the plugins pipelinegen's authors release, and the templates a new
// plugin starts from, tagged templates/vX.Y.Z.
const PluginsRepository = "https://github.com/sideways-works/pipelinegen-plugins"

const templatesDir = "templates"

var (
	errTemplate = errors.New("can't start the plugin")
	pluginName  = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	moduleLine  = regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`)
)

func pluginNew(ctx context.Context, args []string, stdout io.Writer) error {
	var into, from, version, module string

	fs := flag.NewFlagSet("plugin new", flag.ContinueOnError)
	pathVar(fs, &into, "into", "", "the directory to write the plugin into, new or empty (default <name>-<kind> in the current directory)", writes)
	textVar(fs, &from, "from", PluginsRepository, "the git URL of the repository whose templates/vX.Y.Z tags hold the templates, one directory a kind under templates/")
	textVar(fs, &version, "version", "", "the templates' release, vX.Y.Z (default: the newest that is no prerelease)")
	textVar(fs, &module, "module", "", "the new plugin's Go module path (default: the template's, with templates/<kind> made <kind>/<name>)")

	pos, err := parseMixed(ctx, fs, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return fmt.Errorf("%w: plugin new <kind> <name> [--into <dir>]", errUsage)
	}

	kind, name := pos[0], pos[1]

	switch {
	case !slices.Contains(plugin.Kinds, kind):
		return fmt.Errorf("%w: kind %q: one of %s", errUsage, kind, strings.Join(plugin.Kinds, ", "))
	case !pluginName.MatchString(name) || strings.HasSuffix(name, "-"+kind):
		return fmt.Errorf("%w: name %q: lower case letters, digits and dashes, without the kind (go makes go-%s)", errUsage, name, kind)
	}

	if into == "" {
		into = absOr(name + "-" + kind)
	}

	if ents, err := os.ReadDir(into); err == nil && len(ents) > 0 {
		return fmt.Errorf("%w: %s holds files already", errTemplate, into)
	}

	tag, files, err := fetchTemplate(ctx, config.CloneURL(from), version, kind)
	if err != nil {
		return err
	}

	t, err := fromTemplate(files, kind, name, module, newDir(ctx, into, kind, name))
	if err != nil {
		return fmt.Errorf("%w: %s %s: %w", errTemplate, tag, kind, err)
	}

	if err := writeTemplate(into, t.files); err != nil {
		return err
	}

	return say(stdout, "wrote the %s template of %s %s into %s: module %s, program %s, %d files\n", kind, from, tag, into, t.module, t.program, len(t.files))
}

type templateFile struct {
	data []byte
	exec bool
}

// fetchTemplate reads templates/<kind>/ at the tag of version, or of the newest release that is
// no prerelease, by paths below it.
func fetchTemplate(ctx context.Context, from, version, kind string) (string, map[string]templateFile, error) {
	tag, err := templateTag(ctx, from, version)
	if err != nil {
		return "", nil, err
	}

	work, err := os.MkdirTemp("", "pipelinegen-template-")
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", errTemplate, err)
	}
	defer os.RemoveAll(work) //nolint:errcheck // a scratch directory

	git := gitx.Repo{Dir: work}

	if _, err := git.Git(ctx, "init", "-q"); err != nil {
		return "", nil, fmt.Errorf("%w: %w", errTemplate, err)
	}

	if _, err := git.Git(ctx, "fetch", "-q", "--depth", "1", "--no-tags", from, "refs/tags/"+tag); err != nil {
		return "", nil, fmt.Errorf("%w: %w", errTemplate, err)
	}

	dir := templatesDir + "/" + kind

	if _, err := git.Git(ctx, "cat-file", "-e", "FETCH_HEAD:"+dir); err != nil {
		return "", nil, fmt.Errorf("%w: %s of %s has no %s", errTemplate, tag, from, dir)
	}

	archive, err := git.Git(ctx, "archive", "--format=tar", "FETCH_HEAD", dir)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", errTemplate, err)
	}

	files, err := untar(archive, dir)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %s: %w", errTemplate, tag, err)
	}

	return tag, files, nil
}

func templateTag(ctx context.Context, from, version string) (string, error) {
	if version != "" {
		if _, ok := forge.VersionOf(version, ""); !ok {
			return "", fmt.Errorf("%w: --version %q: a version vX.Y.Z", errUsage, version)
		}

		return templatesDir + "/" + version, nil
	}

	out, err := gitx.Repo{}.Git(ctx, "ls-remote", "--tags", "--refs", from, "refs/tags/"+templatesDir+"/v*")
	if err != nil {
		return "", fmt.Errorf("%w: %w", errTemplate, err)
	}

	var (
		best    semver.Version
		bestTag string
	)

	for line := range strings.Lines(out) {
		_, ref, _ := strings.Cut(strings.TrimSpace(line), "\t")
		tag := strings.TrimPrefix(ref, "refs/tags/")

		if v, ok := forge.VersionOf(tag, templatesDir); ok && !v.Prerelease() && (bestTag == "" || v.Compare(best) > 0) {
			best, bestTag = v, tag
		}
	}

	if bestTag == "" {
		return "", fmt.Errorf("%w: %s has no tag %s/vX.Y.Z that is no prerelease", errTemplate, from, templatesDir)
	}

	return bestTag, nil
}

// maxTemplate bounds what a template may unpack to.
const maxTemplate = 50 << 20

// untar reads the regular files under dir of a git archive; a link is refused, so nothing is
// written outside the new plugin's directory.
func untar(archive, dir string) (map[string]templateFile, error) {
	files := map[string]templateFile{}
	tr := tar.NewReader(strings.NewReader(archive))
	total := 0

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err //nolint:wrapcheck // the caller says whose
		}

		rel, ok := strings.CutPrefix(h.Name, dir+"/")

		switch {
		case h.Typeflag == tar.TypeXGlobalHeader || h.Typeflag == tar.TypeDir:
			continue
		case !ok || rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "../"):
			return nil, fmt.Errorf("%s: a path outside %s", h.Name, dir) //nolint:err113 // what the archive holds
		case h.Typeflag != tar.TypeReg:
			return nil, fmt.Errorf("%s: a link or special file; a template holds files only", h.Name) //nolint:err113 // what the archive holds
		}

		total += int(h.Size)
		if total > maxTemplate {
			return nil, fmt.Errorf("over %d bytes", maxTemplate) //nolint:err113 // a bound of ours
		}

		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err //nolint:wrapcheck // the caller says whose
		}

		files[rel] = templateFile{data: data, exec: h.Mode&0o111 != 0}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("%s holds no file", dir) //nolint:err113 // what the archive holds
	}

	return files, nil
}

type template struct {
	files           map[string]templateFile
	module, program string
}

// fromTemplate is a template's files with its module and program renamed: the module its go.mod
// declares becomes module, the one program under cmd/ becomes <name>-<kind>, and the template's
// own directory, templates/<kind>, becomes dir, in every file's text and path.
func fromTemplate(files map[string]templateFile, kind, name, module, dir string) (template, error) {
	gomod, ok := files["go.mod"]
	if !ok {
		return template{}, errors.New("no go.mod") //nolint:err113 // the template's
	}

	m := moduleLine.FindSubmatch(gomod.data)
	if m == nil {
		return template{}, errors.New("its go.mod declares no module") //nolint:err113 // the template's
	}

	oldModule := string(m[1])

	var programs []string

	for p := range files {
		if rest, ok := strings.CutPrefix(p, "cmd/"); ok {
			if program, _, deeper := strings.Cut(rest, "/"); deeper {
				programs = append(programs, program)
			}
		}
	}

	slices.Sort(programs)

	if programs = slices.Compact(programs); len(programs) != 1 {
		return template{}, fmt.Errorf("a template holds one program under cmd/, and this one %d", len(programs)) //nolint:err113 // the template's
	}

	if module == "" {
		module = defaultModule(oldModule, kind, name)
	}

	program := name + "-" + kind
	r := strings.NewReplacer(oldModule, module, programs[0], program)
	// the template's directory as a whole path part: templates/sbomx is another one
	own := regexp.MustCompile(regexp.QuoteMeta(templatesDir+"/"+kind) + `([A-Za-z0-9_.-]?)`)
	replace := func(text string) string {
		return own.ReplaceAllStringFunc(r.Replace(text), func(m string) string {
			if strings.HasSuffix(m, "/"+kind) {
				return dir
			}

			return m
		})
	}

	out := template{files: map[string]templateFile{}, module: module, program: program}
	for p, f := range files {
		out.files[replace(p)] = templateFile{data: []byte(replace(string(f.data))), exec: f.exec}
	}

	return out, nil
}

// newDir is the new plugin's directory as its files name it: its path in the git work tree that
// holds it, else <kind>/<name>, where a plugin of the plugins repository lives.
func newDir(ctx context.Context, into, kind, name string) string {
	parent := filepath.Dir(into)
	for {
		if _, err := os.Stat(parent); err == nil || filepath.Dir(parent) == parent {
			break
		}

		parent = filepath.Dir(parent)
	}

	top, err := gitx.Repo{Dir: parent}.Git(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return kind + "/" + name
	}

	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return kind + "/" + name
	}

	below, err := filepath.Rel(parent, into)
	if err != nil {
		return kind + "/" + name
	}

	rel, err := filepath.Rel(strings.TrimSpace(top), filepath.Join(resolved, below))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return kind + "/" + name
	}

	return filepath.ToSlash(rel)
}

// defaultModule puts the new plugin beside the others of its repository: .../templates/sbom makes
// .../sbom/<name>, any other module path .../<name>-<kind>.
func defaultModule(template, kind, name string) string {
	if base, ok := strings.CutSuffix(template, "/"+templatesDir+"/"+kind); ok {
		return base + "/" + kind + "/" + name
	}

	return path.Dir(template) + "/" + name + "-" + kind
}

func writeTemplate(into string, files map[string]templateFile) error {
	for p, f := range files {
		dest := filepath.Join(into, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return fmt.Errorf("%w: %w", errTemplate, err)
		}

		mode := os.FileMode(0o644)
		if f.exec {
			mode = 0o755
		}

		if err := os.WriteFile(dest, f.data, mode); err != nil {
			return fmt.Errorf("%w: %w", errTemplate, err)
		}
	}

	return nil
}
