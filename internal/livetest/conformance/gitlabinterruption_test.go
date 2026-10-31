// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// This opt-in fixture is stopped through the documented launcher only after
// its job emits the ready marker. The expected evidence is interruption plus
// normal cleanup, never a passing test produced by waiting out the sleep.
func TestGitLabWorkflow_InterruptedJob(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	if os.Getenv("FORGE_CI_LIVE_INTERRUPT_PROBE") != "gitlab-workflow" {
		livetest.NotCoveredf(t, "active-job interruption requires FORGE_CI_LIVE_INTERRUPT_PROBE=gitlab-workflow and an observed launcher signal")
	}

	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "active-job interruption requires a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	repo := livetest.NewScratchRepo(t, target, "interrupt-workflow")
	t.Logf("interruption fixture: %s", livetest.RepoSlug(target, repo))

	workflow := `interrupt-probe:
  image: ` + livetest.ProbeImage + `
  variables:
    GIT_STRATEGY: empty
  script:
    - printf 'fci-live-interruption-ready\n'
    - sleep 180
`
	conclusion := livetest.RunWorkflow(t, target, repo, "interrupt-probe", workflow)
	t.Fatalf("interruption fixture ended normally (%s); stop the launcher after observing the ready marker", conclusion)
}
