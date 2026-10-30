package cli

import (
	"os"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/internal/toolcalls"
)

func TestNoBuildEnvironmentReachesTheCommandsUnderTest(t *testing.T) {
	for _, name := range []string{
		envRun, envChange, envCommit, envPR, envAlsoTo, envSuite, envSlice, envLabel, envBatch, envJobURL,
		envToken, envGitURL, envAPIURL, envTrigger, envRunFile,
		toolcalls.EnvBazelisk, toolcalls.EnvConcourseURL, config.EnvConcourseURL,
	} {
		if !testenv.Scrubbed(name) {
			t.Errorf("%s is read by the commands but not dropped for tests", name)
		}

		if v, ok := os.LookupEnv(name); ok {
			t.Errorf("%s=%s is set as the tests start", name, v)
		}
	}
}
