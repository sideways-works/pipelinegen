package config

import (
	"strings"
	"testing"
)

func TestASuitesTimeLimit(t *testing.T) {
	noLock := strings.Replace(withRuns, "    lock: {name: browsers, minutes: 90}\n", "", 1)

	for _, tc := range []struct {
		name, from, to   string
		e2ePR, e2eNight  int
		key              string
		warns            string
		limitsPlan, step int
	}{
		{"default", noLock, "", DefaultSuiteMinutes, DefaultSuiteMinutes, "suites.e2e.minutes", "", DefaultPlanMinutes, DefaultStepMinutes},
		{"the lock's old minutes", withRuns, "", 90, 90, "suites.e2e.minutes", "suites.e2e.lock.minutes is the old spelling of suites.e2e.minutes", DefaultPlanMinutes, DefaultStepMinutes},
		{"the suite's own", noLock, "    run_slice: ci/tasks/e2e-slice.yml\n    minutes: 45\n", 45, 45, "suites.e2e.minutes", "", DefaultPlanMinutes, DefaultStepMinutes},
		{"a run's", noLock, "{e2e: {params: {PROJECTS: all}, minutes: 600}}", DefaultSuiteMinutes, 600, "runs.nightly.suites.e2e.minutes", "", DefaultPlanMinutes, DefaultStepMinutes},
		{"a run's old lock_minutes", withRuns, "{e2e: {params: {PROJECTS: all}, lock_minutes: 240}}", 90, 240, "runs.nightly.suites.e2e.minutes", "runs.nightly.suites.e2e.lock_minutes is the old spelling", DefaultPlanMinutes, DefaultStepMinutes},
		{"limits", noLock + "limits: {plan: 30, steps: 10}\n", "", DefaultSuiteMinutes, DefaultSuiteMinutes, "suites.e2e.minutes", "", 30, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.from
			if tc.to != "" {
				text = replaceOne(t, text, tc.to)
			}

			c, err := Parse([]byte(text))
			if err != nil {
				t.Fatal(err)
			}

			if got := c.SuiteMinutes("pr", "e2e"); got != tc.e2ePR {
				t.Errorf("pr: %d, want %d", got, tc.e2ePR)
			}

			if got := c.SuiteMinutes("nightly", "e2e"); got != tc.e2eNight {
				t.Errorf("nightly: %d, want %d", got, tc.e2eNight)
			}

			if got := c.SuiteMinutesKey("nightly", "e2e"); got != tc.key {
				t.Errorf("key %s, want %s", got, tc.key)
			}

			warned := strings.Join(c.OldSpellings, "\n")
			if (tc.warns == "") != (warned == "") || !strings.Contains(warned, tc.warns) {
				t.Errorf("warnings %q, want %q", warned, tc.warns)
			}

			if c.Limits.PlanMinutes() != tc.limitsPlan || c.Limits.StepMinutes() != tc.step {
				t.Errorf("limits %d %d", c.Limits.PlanMinutes(), c.Limits.StepMinutes())
			}
		})
	}
}

func replaceOne(t *testing.T, text, to string) string {
	t.Helper()

	from := "{e2e: {params: {PROJECTS: all}}}"
	if strings.HasPrefix(to, "    run_slice") {
		from = "    run_slice: ci/tasks/e2e-slice.yml\n"
	}

	if !strings.Contains(text, from) {
		t.Fatalf("the fixture has no %q", from)
	}

	return strings.Replace(text, from, to, 1)
}

func TestBadTimeLimitsAreRefused(t *testing.T) {
	for want, text := range map[string]string{
		"minutes is 45 and lock.minutes 90":         replaceOne(t, withRuns, "    run_slice: ci/tasks/e2e-slice.yml\n    minutes: 45\n"),
		"minutes is 300 and lock_minutes 240":       strings.Replace(withRuns, "{e2e: {params: {PROJECTS: all}}}", "{e2e: {params: {PROJECTS: all}, minutes: 300, lock_minutes: 240}}", 1),
		"shorter than batch_minutes (100)":          replaceOne(t, withRuns, "    run_slice: ci/tasks/e2e-slice.yml\n    batch_minutes: 100\n"),
		"wait_minutes is for a continuous run":      strings.Replace(withRuns, "    record: true\n", "    record: true\n    wait_minutes: 30\n", 1),
		"limits: plan and steps are minutes":        withRuns + "limits: {steps: -1}\n",
		"suite e2e: minutes must be at least 1":     replaceOne(t, withRuns, "    run_slice: ci/tasks/e2e-slice.yml\n    minutes: -3\n"),
		"suite e2e: its time limit (90 minutes) is": replaceOne(t, withRuns, "    run_slice: ci/tasks/e2e-slice.yml\n    batch_minutes: 91\n"),
	} {
		if _, err := Parse([]byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
}

func TestThePlanningTasksDefaultLimit(t *testing.T) {
	if got := (Limits{}).PlanMinutes(); got != 120 {
		t.Fatalf("limits.plan defaults to %d minutes, want 120", got)
	}
}
