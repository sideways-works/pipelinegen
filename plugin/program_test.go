package plugin

import "testing"

func TestARepositoryNamesItsProgramKindFirst(t *testing.T) {
	for repository, want := range map[string]string{
		"o/pipelinegen-sbom-go":                                "go-sbom",
		"o/pipelinegen-correlator-react":                       "react-correlator",
		"o/pipelinegen-runner-concourse":                       "concourse-runner",
		"o/pipelinegen-driver-go":                              "go-driver",
		"o/pipelinegen-connector-github":                       "github-connector",
		"o/pipelinegen-correlator-oapi-codegen":                "oapi-codegen-correlator",
		"https://github.com/o/pipelinegen-sbom-go.git":         "go-sbom",
		"git@github.com:o/pipelinegen-correlator-gorm.git":     "gorm-correlator",
		"o/pipelinegen-sbom-go/":                               "go-sbom",
		"sideways-works/pipelinegen":                           "pipelinegen",
		"https://github.com/sideways-works/pipelinegen.git":    "pipelinegen",
		"sideways-works/go-sbom":                               "go-sbom",
		"sideways-works/concourse-runner":                      "concourse-runner",
		"acme/whatever":                                        "whatever",
		"acme/pipelinegen-whatever":                            "pipelinegen-whatever",
		"o/pipelinegen-sbom":                                   "pipelinegen-sbom",
		"o/pipelinegen-driver-":                                "pipelinegen-driver-",
		"o/pipelinegen-":                                       "pipelinegen-",
		"o/pipelinegen-sboms-go":                               "pipelinegen-sboms-go",
		"o/pipelinegen-runner-concourse-runner":                "concourse-runner-runner",
		"widget":                                               "widget",
		"https://github.com/o/pipelinegen-correlator-sql.git/": "sql-correlator",
	} {
		if got := Program(repository); got != want {
			t.Errorf("Program(%q) = %q, want %q", repository, got, want)
		}
	}
}
