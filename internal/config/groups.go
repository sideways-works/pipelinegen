package config

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sideways-works/plugkit/pin"
)

var pluginPath = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*(/[A-Za-z0-9_-][A-Za-z0-9_.-]*)*$`)

func validRepository(repository string) bool {
	_, err := pin.ParseRepository(repository, nil)

	return err == nil
}

var ErrSource = errors.New("not a plugin's source")

// CheckSource says what is wrong with a plugin's repository and path, as an entry would give them.
func CheckSource(repository, dir string) error {
	switch {
	case !validRepository(repository):
		return fmt.Errorf("%w: repository %q: an https or ssh URL to clone, or owner/name on GitHub", ErrSource, repository)
	case dir != "" && !pluginPath.MatchString(dir):
		return fmt.Errorf("%w: path %q: a directory of the repository, such as driver/go, without a leading or trailing slash", ErrSource, dir)
	}

	return nil
}

func validateSource(p Plugin, bad func(string, ...any)) {
	if p.Repository == "" {
		return
	}

	if err := CheckSource(p.Repository, p.Path); err != nil {
		bad("plugin %s: %s", p.Name, strings.TrimPrefix(err.Error(), ErrSource.Error()+": "))
	}
}

// ungroup replaces each group of plugins by its entries, each given the group's repository, and
// says what keeps a group from being read so.
func ungroup(ps []Plugin) ([]Plugin, []string) {
	if !slices.ContainsFunc(ps, func(p Plugin) bool { return p.With != nil }) {
		return ps, nil
	}

	var (
		out      []Plugin
		problems []string
	)

	for i, p := range ps {
		if p.With == nil {
			out = append(out, p)

			continue
		}

		where := "plugins[" + strconv.Itoa(i) + "]"
		if p.Repository != "" {
			where += " (" + p.Repository + ")"
		}

		group := p
		group.Repository, group.With = "", nil

		switch {
		case !reflect.DeepEqual(group, Plugin{}):
			problems = append(problems, where+": a group takes repository and with only; name, kind, path, release and the rest go on each entry under with")
		case p.Repository == "":
			problems = append(problems, where+": a group needs the repository its entries share")
		case len(p.With) == 0:
			problems = append(problems, where+": with lists no plugin")
		}

		for j, e := range p.With {
			at := fmt.Sprintf("%s.with[%d]", where, j)
			if e.Name != "" {
				at = "plugin " + e.Name
			}

			switch {
			case e.Repository != "":
				problems = append(problems, at+": an entry under with takes its group's repository and names none of its own")
			case e.With != nil:
				problems = append(problems, at+": an entry under with is a plugin, not a group")
			}

			e.Repository, e.With = p.Repository, nil
			out = append(out, e)
		}
	}

	return out, problems
}
