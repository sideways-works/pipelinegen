package packages

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/config"
)

// Base is a registry-image resource of the root pipeline file: its
// repository, and the digest its version pins, if any.
type Base struct {
	Repository, Digest string
}

// Bases are the registry-image resources of a root pipeline file, by name;
// false when the file can't be read as one.
func Bases(include []byte) (map[string]Base, bool) {
	var file struct {
		Resources []struct {
			Name    string         `yaml:"name"`
			Type    string         `yaml:"type"`
			Source  map[string]any `yaml:"source"`
			Version map[string]any `yaml:"version"`
		} `yaml:"resources"`
	}

	// an error may quote the file, which may hold secrets' names: it is not passed on
	if yaml.Unmarshal(include, &file) != nil {
		return nil, false
	}

	out := map[string]Base{}

	for _, r := range file.Resources {
		if r.Type != "registry-image" {
			continue
		}

		repo, _ := r.Source["repository"].(string) //nolint:errcheck // checked by the caller
		digest, _ := r.Version["digest"].(string)  //nolint:errcheck // optional
		out[r.Name] = Base{Repository: repo, Digest: digest}
	}

	return out, true
}

// Use is how the steps of a suite that run in one image get the suite's
// packages on CI: from a cached image, Key, built on Base; or, with Key
// empty, installed by each task, for the reason Why.
type Use struct {
	Suite string
	// Image is the registry-image resource the steps run in; empty for a
	// suite whose task files name their own.
	Image string
	Base  Base
	Key   string
	Why   string
}

// Uses are, for every suite that declares packages, how each image its
// steps run in gets them; bases are the root pipeline file's, nil when
// it has none or can't be read.
func Uses(cfg *config.Config, bases map[string]Base) []Use {
	var out []Use

	for _, name := range cfg.SuiteNames() {
		ps := cfg.PackagesFor(name)
		if len(ps) == 0 {
			continue
		}

		for _, img := range suiteImages(cfg.Suites[name]) {
			out = append(out, use(cfg, bases, name, img, ps))
		}
	}

	return out
}

func use(cfg *config.Config, bases map[string]Base, suite, img string, ps config.Packages) Use {
	u := Use{Suite: suite, Image: img}

	b, declared := bases[img]
	u.Base = b

	switch {
	case cfg.Images.Registry == "":
		u.Why = "no images.registry names where cached images are kept"
	case img == "":
		u.Why = "its image is its task files' own: name it in suites." + suite + ".image for a cached image"
	case cfg.Concourse.Include == "" || bases == nil:
		u.Why = "no root pipeline file (concourse.include) can be read to find " + img + "'s digest"
	case !declared || b.Repository == "" || strings.Contains(b.Repository, "(("):
		u.Why = img + " is no registry-image resource of " + cfg.Concourse.Include + " with a plain repository"
	case b.Digest == "":
		u.Why = img + " is a tag, which can move: pin its version by digest in " + cfg.Concourse.Include + " for a cached image"
	default:
		u.Key = Key(b.Digest, ps)
	}

	return u
}

// suiteImages are the images a suite's steps run in: the suite's, and its
// prepare step's when that is another.
func suiteImages(s config.Suite) []string {
	out := []string{s.Image}

	if s.HasSteps() {
		for _, i := range s.StepsOf(config.StepPrepare) {
			if img := s.Steps[i].Image; img != "" && !slices.Contains(out, img) {
				out = append(out, img)
			}
		}
	}

	return out
}

// TaskFiles tells whether some steps of a suite run the repository's own
// task files, whose scripts a runner can't put an install in front of:
// only a cached image brings those steps the packages.
func TaskFiles(s config.Suite) bool {
	return !s.HasSteps() && (s.Prepare != "" || s.RunSlice != "") || len(s.WholeSuite) > 0
}

// Ref is a cached image's reference in a registry.
func Ref(registry, key string) string { return registry + ":" + key }

// Short is a key as a person reads it.
func Short(key string) string {
	if len(key) > 12 {
		return key[:12]
	}

	return key
}

// Describe says how a use gets its packages, for check's note.
func (u Use) Describe(cfg *config.Config) string {
	what := fmt.Sprintf("suite %s's packages (%s)", u.Suite, strings.Join(Names(cfg.PackagesFor(u.Suite)), ", "))
	if u.Image != "" {
		what = fmt.Sprintf("suite %s's packages (%s) in %s", u.Suite, strings.Join(Names(cfg.PackagesFor(u.Suite)), ", "), u.Image)
	}

	if u.Key != "" {
		return fmt.Sprintf("%s come in a cached image, %s, built once on %s@%s", what, Ref(cfg.Images.Registry, Short(u.Key)+"…"), u.Base.Repository, u.Base.Digest)
	}

	return what + " are installed by each task, with a download cache: " + u.Why
}
