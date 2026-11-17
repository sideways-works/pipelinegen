package cli

import (
	"context"
	"debug/elf"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/container"
	"github.com/sideways-works/pipelinegen/internal/fetch"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/packages"
)

var (
	errImage      = errors.New("can't run in the image")
	errUndeclared = errors.New("no such image")
)

// The kinds of a build's tasks.
const (
	kindPrepare = "prepare"
	kindBatch   = "batch"
)

// The build's tools, under tools/.
const (
	toolSelf     = "pipelinegen"
	toolPlugins  = "plugins"
	toolBazelisk = "bazelisk"
)

// imageFlags say where a local build's steps run: in the images the config names, as CI runs them,
// or on this machine.
type imageFlags struct {
	noImage, asMe bool
	tool          string
}

func (f *imageFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&f.noImage, "no-image", false, "run the suites' steps on this machine, not in the images the config names")
	textVar(fs, &f.tool, "container-tool", "", "the container tool that runs the steps in their images, docker or podman "+
		"(default whichever is on PATH, docker first); the containers are on the default bridge network")
	fs.BoolVar(&f.asMe, "as-me", false, "run the steps in their images as you, not as root as CI does (a privileged suite runs as root either way)")
}

// imageBox is how a local build runs its steps in their images; nil runs them on this machine.
type imageBox struct {
	flags imageFlags
	tool  string
	// linux is the pipelinegen each image platform runs, once found.
	linux map[string]string
	// made is each base image, pulled and with the suite's packages, by the base's reference.
	made map[string]packaged
}

// packaged is a task's image as it runs: with the suite's packages when it has any, its platform, and
// the install that then finds them there.
type packaged struct {
	ref, platform string
	before        []string
}

// taskImage is an image a task runs in, and what named it.
type taskImage struct {
	ref, named string
}

// image is the image the suite's prepare or batch runs in on CI: the suite's (or its prepare step's)
// resource of the root pipeline file, or the task file's own; none when nothing names one.
func (b *localBuild) image(ctx context.Context, kind, taskFile string) (taskImage, error) {
	s := b.cfg.Suites[b.suite]
	name, key := s.Image, "suites."+b.suite+".image"

	if kind == kindPrepare && s.HasSteps() {
		for _, i := range s.StepsOf(config.StepPrepare) {
			if img := s.Steps[i].Image; img != "" {
				name, key = img, fmt.Sprintf("suites.%s.steps[%d].image", b.suite, i)
			}
		}
	}

	if name != "" {
		ref, err := rootImage(ctx, b.cfg, b.root, b.ch.Commit, name)
		if err != nil {
			return taskImage{}, fmt.Errorf("%w: %s: %w", errImage, key, err)
		}

		return taskImage{ref: ref, named: key + " (" + name + ")"}, nil
	}

	if taskFile == "" {
		return taskImage{}, nil
	}

	t, err := readTask(filepath.Join(b.root, taskFile))
	if err != nil {
		return taskImage{}, err
	}

	if len(t.ImageResource.Source) == 0 {
		return taskImage{}, nil
	}

	ref, err := imageRef(t.ImageResource.Source, t.ImageResource.Version)
	if err != nil {
		return taskImage{}, fmt.Errorf("%w: %s's image_resource: %w", errImage, taskFile, err)
	}

	return taskImage{ref: ref, named: taskFile + "'s image_resource"}, nil
}

// rootImage is the registry-image resource name of the root pipeline file (concourse.include), read at
// commit as the config is.
func rootImage(ctx context.Context, cfg *config.Config, root, commit, name string) (string, error) {
	include := cfg.Concourse.Include
	if include == "" {
		return "", fmt.Errorf("%w: it names %s, but concourse.include names no root pipeline file to declare it in", errUndeclared, name)
	}

	data, ok, err := gitx.Repo{Dir: root}.Show(ctx, commit, "./"+include)
	if err != nil {
		return "", err
	}

	if !ok {
		return "", fmt.Errorf("%w: it names %s, but %s has no %s", errUndeclared, name, short12(commit), include)
	}

	var file struct {
		Resources []struct {
			Name    string         `yaml:"name"`
			Type    string         `yaml:"type"`
			Source  map[string]any `yaml:"source"`
			Version map[string]any `yaml:"version"`
		} `yaml:"resources"`
	}

	// a parse error may quote the file, which may hold secrets' names: only its name is said
	if yaml.Unmarshal(data, &file) != nil {
		return "", fmt.Errorf("%w: %s can't be read as YAML", errUndeclared, include)
	}

	for _, r := range file.Resources {
		if r.Name == name && r.Type == "registry-image" {
			return imageRef(r.Source, r.Version)
		}
	}

	return "", fmt.Errorf("%w: it names %s, which %s doesn't declare as a registry-image resource", errUndeclared, name, include)
}

// imageRef is a registry-image source as a reference docker and podman pull: by the digest a pinned
// version names, else by its tag.
func imageRef(source, version map[string]any) (string, error) {
	repo, _ := source["repository"].(string) //nolint:errcheck // checked below
	tag, _ := source["tag"].(string)         //nolint:errcheck // optional
	digest, _ := version["digest"].(string)  //nolint:errcheck // optional

	switch {
	case repo == "":
		return "", fmt.Errorf("%w: its source names no repository", errUndeclared)
	case strings.Contains(repo+tag+digest, "(("):
		return "", fmt.Errorf("%w: its source takes a var, which only the pipeline has", errUndeclared)
	case digest != "":
		return repo + "@" + digest, nil
	case tag != "":
		return repo + ":" + tag, nil
	default:
		return repo + ":latest", nil
	}
}

// inImage runs cmd in img in dir as CI's task would: the build's directories and tools at the paths
// the build has them, so every path the steps are given holds, and only the task's environment.
func (b *localBuild) inImage(ctx context.Context, img taskImage, kind, dir string, cmd []string, params map[string]string) error {
	p, err := b.packaged(ctx, img, kind)
	if err != nil {
		return err
	}

	img.ref = p.ref

	// the steps after, report batch-end among them, run here and read the tools as this machine's
	defer b.keepHostTools()()

	r, back, err := b.containerRun(ctx, img, kind, dir, cmd, params, platformOf(p.platform))
	if err != nil {
		return err
	}

	r.Before = p.before

	c, err := r.Command(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", errImage, err)
	}

	c.Stdout, c.Stderr = b.output(), os.Stderr

	warnf(os.Stderr, "pipelinegen: suite %s's %s runs in %s (%s), with %s\n", b.suite, kind, img.ref, img.named, filepath.Base(b.box.tool))

	err = c.Run()

	if back != nil {
		handBack(ctx, *back)
	}

	return err //nolint:wrapcheck // the caller says which task
}

// packaged pulls img and makes it the image with the suite's packages, once a build: a run makes its
// tasks' images before its memory check, and the tasks find them made.
func (b *localBuild) packaged(ctx context.Context, img taskImage, kind string) (packaged, error) {
	if p, ok := b.box.made[img.ref]; ok {
		return p, nil
	}

	if b.box.tool == "" {
		tool, err := container.Find(b.box.flags.tool)
		if err != nil {
			return packaged{}, fmt.Errorf("%w: suite %s's %s runs in an image (%s), and %w: install docker or podman, or pass --no-image to run on this machine",
				errImage, b.suite, kind, img.named, err)
		}

		b.box.tool = tool
	}

	base := img.ref

	platform, err := container.Ensure(ctx, b.box.tool, base, os.Stderr)
	if err != nil {
		return packaged{}, fmt.Errorf("%w: %w; is %s running? --no-image runs on this machine", errImage, err, filepath.Base(b.box.tool))
	}

	before, err := b.withPackages(ctx, &img)
	if err != nil {
		return packaged{}, err
	}

	p := packaged{ref: img.ref, platform: platform, before: before}

	if b.box.made == nil {
		b.box.made = map[string]packaged{}
	}

	b.box.made[base] = p

	return p, nil
}

// installPackages makes the images of the build's tasks with the suite's packages before the rest of
// the batch, as CI installs them in front of each task's command: the memory check and the setup come
// after. Tasks that run on this machine install nothing; the machine is checked for the packages.
func (b *localBuild) installPackages(ctx context.Context) error {
	if len(b.cfg.PackagesFor(b.suite)) == 0 {
		return nil
	}

	for _, t := range b.tasks() {
		img, err := b.boxImage(ctx, t.kind, t.file)
		if err != nil {
			return err
		}

		if img.ref == "" {
			b.hostPackages(ctx)

			continue
		}

		if _, err := b.packaged(ctx, img, t.kind); err != nil {
			return err
		}
	}

	return nil
}

// buildTask is one task of a suite's build: its kind and its task file, none for a suite of steps.
type buildTask struct{ kind, file string }

// tasks are the tasks run runs, in its order.
func (b *localBuild) tasks() []buildTask {
	s := b.cfg.Suites[b.suite]
	if s.HasSteps() {
		return []buildTask{{kindPrepare, ""}, {kindBatch, ""}}
	}

	var out []buildTask
	if s.Prepare != "" {
		out = append(out, buildTask{kindPrepare, s.Prepare})
	}

	return append(out, buildTask{kindBatch, s.RunSlice})
}

// withPackages swaps img for the image with the suite's packages installed on it, made the first time
// and kept in the local image store, and is the install that then finds them there: it installs
// nothing, and fails a step that would run without them.
func (b *localBuild) withPackages(ctx context.Context, img *taskImage) ([]string, error) {
	ps := b.cfg.PackagesFor(b.suite)
	if len(ps) == 0 {
		return nil, nil
	}

	ref, built, err := packages.Local(ctx, b.box.tool, img.ref, ps, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("%w: suite %s's packages (%s) on %s: %w", errImage, b.suite, strings.Join(packages.Names(ps), ", "), img.ref, err)
	}

	made := "made before"
	if built {
		made = "made now"
	}

	warnf(os.Stderr, "pipelinegen: suite %s's packages (%s) are in %s, %s on %s\n", b.suite, strings.Join(packages.Names(ps), ", "), ref, made, img.ref)

	img.ref = ref

	return []string{packages.Script(ps)}, nil
}

// hostPackages says which packages the suite declares that this machine seems to lack: steps that run
// here install nothing.
func (b *localBuild) hostPackages(ctx context.Context) {
	ps := b.cfg.PackagesFor(b.suite)
	if len(ps) == 0 || b.hostChecked {
		return
	}

	b.hostChecked = true

	if missing := packages.Missing(ps, packages.OnHost(ctx)); len(missing) > 0 {
		warnf(os.Stderr, "pipelinegen: suite %s declares packages this machine has under no command or installed package of their name: %s; "+
			"its steps run here and install nothing, so install them, or run in the suite's image without --no-image\n", b.suite, strings.Join(missing, ", "))
	}
}

// handBack gives the user what a root container wrote, however it ended: failed, or stopped.
func handBack(ctx context.Context, r container.Run) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()

	c, err := r.Command(ctx)
	if err == nil {
		var out []byte

		if out, err = c.CombinedOutput(); err != nil {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
	}

	if err != nil {
		warnf(os.Stderr, "pipelinegen: what the steps wrote as root in %s stays root's (%v): sudo chown -R %d:%d %s\n",
			strings.Join(r.Cmd[3:], " "), err, os.Getuid(), os.Getgid(), strings.Join(r.Cmd[3:], " "))
	}
}

// keepHostTools notes where the build's tools link to on this machine, and returns what links them
// there again.
func (b *localBuild) keepHostTools() func() {
	links := map[string]string{}

	for _, name := range []string{toolSelf, toolPlugins, toolBazelisk} {
		p := filepath.Join(b.dir, "tools", name)
		if target, err := os.Readlink(p); err == nil {
			links[p] = target
		}
	}

	return func() {
		for p, target := range links {
			if err := replaceLink(target, p); err != nil {
				warnf(os.Stderr, "pipelinegen: %s: %v\n", p, err)
			}
		}
	}
}

// containerRun is the container a task of the build runs in, on an image of platform plat, and the one
// that hands back what it wrote as root, if any.
func (b *localBuild) containerRun(ctx context.Context, img taskImage, kind, dir string, cmd []string, params map[string]string, plat string) (container.Run, *container.Run, error) {
	if !strings.HasPrefix(plat, "linux-") {
		return container.Run{}, nil, fmt.Errorf("%w: %s is a %s image; pipelinegen runs in Linux images", errImage, img.ref, plat)
	}

	mounts, err := b.imageTools(ctx, plat)
	if err != nil {
		return container.Run{}, nil, err
	}

	// a privileged suite starts its own container engine, which needs root
	privileged := b.cfg.Suites[b.suite].Privileged
	user, userns, handBack := container.Owner(ctx, b.box.tool, b.box.flags.asMe && !privileged)

	// layered as on CI: the params, then pipelinegen's own, which nothing overrides
	env := map[string]string{}
	maps.Copy(env, b.cfg.Params(localRun, b.suite))
	maps.Copy(env, params)

	switch {
	case user != "":
		// the user has no home in the image; Go's and npm's caches go under it
		env["HOME"] = "/tmp"
	case handBack:
		// root in the container, and source is this user's: git would refuse it as another's
		env["GIT_CONFIG_COUNT"], env["GIT_CONFIG_KEY_0"], env["GIT_CONFIG_VALUE_0"] = "1", "safe.directory", "*"
	}

	maps.Copy(env, b.ownEnv())

	r := container.Run{
		Tool: b.box.tool, Image: img.ref, Name: container.Name(b.suite + "-" + kind), Dir: dir,
		Mounts: mounts, Env: env, User: user, UserNS: userns, Privileged: privileged, Cmd: cmd,
	}

	if !handBack {
		return r, nil, nil
	}

	back := container.HandBack(r, filepath.Dir(b.dir))

	return r, &back, nil
}

// imageTools links the build's tools to builds for the image's platform and mounts what the build
// refers to at the same paths: its own directory, the repository's git directory that source is a
// worktree of, read only, and the tools.
func (b *localBuild) imageTools(ctx context.Context, plat string) ([]container.Mount, error) {
	tool, err := b.linuxTool(ctx, plat)
	if err != nil {
		return nil, err
	}

	pf, err := pluginsFor(ctx, b.cfg, b.plugins, plat, suitePlugins(b.cfg, b.suite, b.cfg.Records(localRun, b.suite)), true)
	if err != nil {
		return nil, err
	}

	if err := replaceLink(tool, filepath.Join(b.dir, "tools", toolSelf)); err != nil {
		return nil, fmt.Errorf("%w: %w", errImage, err)
	}

	if err := replaceLink(pf.dir, filepath.Join(b.dir, "tools", toolPlugins)); err != nil {
		return nil, fmt.Errorf("%w: %w", errImage, err)
	}

	common, err := gitx.Repo{Dir: b.root}.Git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}

	m := &mountSet{}
	m.add(filepath.Dir(b.dir), false)
	m.add(strings.TrimSpace(common), true)
	m.add(tool, true)

	if _, err := os.Stat(pf.dir); err == nil {
		m.add(pf.dir, true)
	}

	for _, name := range slices.Sorted(maps.Keys(pf.files)) {
		m.add(pf.files[name], true)
	}

	// bazelisk is this machine's: it runs in the image only when that is this machine's platform
	if bz, err := os.Readlink(filepath.Join(b.dir, "tools", toolBazelisk)); err == nil && plat == config.Platform() {
		m.add(bz, true)
	} else if err == nil {
		_ = os.Remove(filepath.Join(b.dir, "tools", toolBazelisk)) //nolint:errcheck // a link to a binary the image can't run
	}

	return m.mounts, nil
}

type mountSet struct {
	mounts []container.Mount
	seen   map[string]bool
}

// add mounts p at its own path, the file a link names in its place.
func (m *mountSet) add(p string, readOnly bool) {
	abs, err := filepath.Abs(p)
	if err != nil || m.seen[abs] {
		return
	}

	file, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return
	}

	if m.seen == nil {
		m.seen = map[string]bool{}
	}

	m.seen[abs] = true
	m.mounts = append(m.mounts, container.Mount{Host: file, At: abs, ReadOnly: readOnly})
}

// linuxTool is a pipelinegen the image's platform runs: this one when it is a static build for it,
// else the release concourse.planner.bootstrap pins, fetched for that platform as the bootstrap does.
func (b *localBuild) linuxTool(ctx context.Context, plat string) (string, error) {
	if p, ok := b.box.linux[plat]; ok {
		return p, nil
	}

	p, err := b.findLinuxTool(ctx, plat)
	if err != nil {
		return "", err
	}

	if b.box.linux == nil {
		b.box.linux = map[string]string{}
	}

	b.box.linux[plat] = p

	return p, nil
}

func (b *localBuild) findLinuxTool(ctx context.Context, plat string) (string, error) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}

	if err != nil {
		return "", fmt.Errorf("%w: %w", errImage, err)
	}

	if plat == config.Platform() && staticELF(exe) {
		return exe, nil
	}

	boot := b.cfg.Concourse.Planner.Bootstrap
	if boot.Release == nil {
		return "", fmt.Errorf("%w: the image is %s, and this pipelinegen isn't a static build for it; concourse.planner.bootstrap pins no release to fetch: "+
			"build one (CGO_ENABLED=0 GOOS=linux GOARCH=%s go build -trimpath -o pipelinegen ./cmd/pipelinegen) and run it, or pass --no-image",
			errImage, plat, strings.TrimPrefix(plat, "linux-"))
	}

	repo := boot.Repository
	if repo == "" {
		repo = "sideways-works/pipelinegen"
	}

	pin := fetch.Pin{Name: toolSelf, Repository: repo, Version: boot.Release.Version, Sums: boot.Release.Sums, URL: os.Getenv(envReleasesURL)}
	if boot.Release.SHA256 != "" {
		pin.SHA256 = config.Checksums{config.LinuxAMD64: boot.Release.SHA256}
	}

	got, err := releases().Fetch(ctx, pin, plat)
	if err != nil {
		return "", fmt.Errorf("%w: the image is %s, and this pipelinegen isn't a static build for it: the pinned release: %w; "+
			"build one with CGO_ENABLED=0, or pass --no-image", errImage, plat, err)
	}

	warnf(os.Stderr, "pipelinegen: this pipelinegen isn't a static %s build, so the image runs the pinned release %s\n", plat, boot.Release.Version)

	return got.Path, nil
}

var elfMachines = map[string]elf.Machine{"linux-amd64": elf.EM_X86_64, "linux-arm64": elf.EM_AARCH64}

// staticELF says whether exe runs in any Linux image of its platform: it names no dynamic loader.
func staticELF(exe string) bool {
	f, err := elf.Open(exe)
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // read only

	if f.Machine != elfMachines[config.Platform()] {
		return false
	}

	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return false
		}
	}

	return true
}
