// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// What the lifecycle's service readiness reads: where the published image
// answers, and the fixture a service needs beside it, if any.
const (
	serviceReadinessURLEnv = "FORGE_CI_LIVE_SERVICE_READINESS_URL"
	serviceFixtureEnv      = "FORGE_CI_LIVE_SERVICE_FIXTURE"
)

// forEachConsumerCheckout runs trial on the consumer checkouts the run names.
// A checkout named by FORGE_CI_LIVE_CONSUMER is the only one, with the event
// and settings of the caller's environment. Otherwise the prepared directory
// (livetest.PreparedEnv) gives one per case of the forge's table
// (livetest.PreparedCases) with one of events, each the subtest the case
// names, on the demo's checkout there. The subtest's environment is what
// `just test-live-consumer <forge>` hands a trial: the checkout, the event
// and the case's settings, with the prepared images and the case's builder.
// With neither, the test is skipped with usage.
func forEachConsumerCheckout(t *testing.T, forge provider.ForgeAPI, usage string, trial func(t *testing.T, root string), events ...string) {
	t.Helper()

	if root := os.Getenv(consumerCheckoutEnv); root != "" {
		trial(t, root)

		return
	}

	prepared := os.Getenv(livetest.PreparedEnv)
	if prepared == "" {
		t.Skip("no consumer checkout is named; run `" + usage + "`, or set " + livetest.PreparedEnv + " to run the prepared demos")
	}

	env, err := livetest.ReadPreparedEnvFile(filepath.Join(prepared, livetest.PreparedEnvFile))
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range livetest.PreparedCases(string(forge)) {
		if !slices.Contains(events, c.Event) {
			continue
		}

		t.Run(c.Name(), func(t *testing.T) {
			trial(t, usePreparedCase(t, forge, c, prepared, env))
		})
	}
}

// registerLaneScenario registers a live scenario that a lane of
// livetest.PreparedLanes runs beside its cases. In a run from a prepared
// directory it gives the scenario the images and the settings of its lane
// (livetest.PreparedScenarioEnvironment), as a case of the table gets its
// own; a missing image is refused.
func registerLaneScenario(t *testing.T) {
	t.Helper()

	livetest.RegisterLiveScenario(t)

	prepared := os.Getenv(livetest.PreparedEnv)
	if prepared == "" {
		return
	}

	env, err := livetest.ReadPreparedEnvFile(filepath.Join(prepared, livetest.PreparedEnvFile))
	if err != nil {
		t.Fatal(err)
	}

	environment, err := livetest.PreparedScenarioEnvironment(t.Name(), env)
	if err != nil {
		t.Fatal(err)
	}

	for name, value := range environment {
		t.Setenv(name, value)
	}
}

// forScenarioConsumer runs trial on the consumer checkout a scenario runs
// on. A checkout named by FORGE_CI_LIVE_CONSUMER is the one, with the
// caller's environment. Otherwise the prepared directory gives the demo of
// the scenario's case on forge (livetest.PreparedScenarioCase), in the subtest the
// case names and with the case's environment, as a case of the table runs.
// With neither, the scenario is not covered, for the reason given.
func forScenarioConsumer(t *testing.T, forge provider.ForgeAPI, notCovered string, trial func(t *testing.T, root string)) {
	t.Helper()

	if root := os.Getenv(consumerCheckoutEnv); root != "" {
		trial(t, root)

		return
	}

	prepared := os.Getenv(livetest.PreparedEnv)

	c, takes := livetest.PreparedScenarioCase(string(forge), t.Name())
	if prepared == "" || !takes {
		livetest.NotCoveredf(t, "%s", notCovered)
	}

	env, err := livetest.ReadPreparedEnvFile(filepath.Join(prepared, livetest.PreparedEnvFile))
	if err != nil {
		t.Fatal(err)
	}

	t.Run(c.Name(), func(t *testing.T) {
		trial(t, usePreparedCase(t, forge, c, prepared, env))
	})
}

// usePreparedCase gives the test the environment of a case on forge and
// returns the case's checkout, its demo's in the prepared directory. The
// case is refused when env, the directory's prepared.env, lacks an image of
// it.
func usePreparedCase(t *testing.T, forge provider.ForgeAPI, trialCase livetest.PreparedCase, prepared string, env map[string]string) string {
	t.Helper()

	root := filepath.Join(prepared, "demos", trialCase.Demo)

	settings, environment, err := preparedCaseInputs(forge, trialCase, env)
	if err == nil && forge == provider.ForgeGitLab {
		err = trialCase.RequireGitLabFurtherArchives(root, env)
	}

	if err != nil {
		t.Fatal(err)
	}

	if forge == provider.ForgeForgejo && trialCase.BuildsWithKaniko() {
		settings = append(settings, "CI_IMAGE_BUILDER_JOB_IMAGE="+pushForgejoKanikoJobImage(t, env[livetest.KanikoJobImageArchiveEnv]))
	}

	t.Setenv(consumerCheckoutEnv, root)
	t.Setenv(consumerEventEnv, trialCase.Event)
	t.Setenv(consumerSettingsEnv, strings.Join(settings, "\n"))
	t.Setenv(livetest.ImageBuilderEnv(forge), trialCase.Builder)

	for name, value := range environment {
		t.Setenv(name, value)
	}

	for name, value := range trialCase.LaneEnvironment(string(forge)) {
		t.Setenv(name, value)
	}

	if trialCase.Lane == livetest.LaneBaseGraph {
		t.Setenv(baseInventoryEnv, "1")
	}

	return root
}

// pushForgejoKanikoJobImage pushes the prepared kaniko job image archive
// for a Forgejo case, labelled for the consumer trial's layer under the
// run's own account, as a scenario does. Every event and visibility serves
// the layer from that one address, so one push serves the case. The image
// goes to a package of its own, which the trial's repositories do not share
// and so do not remove between visibilities.
func pushForgejoKanikoJobImage(t *testing.T, archive string) string {
	t.Helper()

	target := livetest.Accept(t, provider.ForgeForgejo)

	layer, err := livetest.ConsumerTrialLayer()
	if err != nil {
		t.Fatal(err)
	}

	repo := livetest.ScratchRepo("kaniko-job-image")
	livetest.SweepPackagesAfter(t, target, repo)

	return livetest.PushKanikoJobImage(t, target, repo, archive, layer)
}

// preparedCaseInputs are the settings and the environment of a case on forge.
func preparedCaseInputs(forge provider.ForgeAPI, c livetest.PreparedCase, env map[string]string) ([]string, map[string]string, error) {
	if forge == provider.ForgeGitLab {
		environment, err := c.GitLabEnvironment(env)

		return c.GitLabSettings(), environment, err
	}

	settings, err := c.Settings(env)
	environment := livetest.PreparedForgejoEnvironment(env)

	// A shipped demo's service answers here and takes no fixture, as
	// `just test-live-consumer forgejo <checkout> lifecycle` sets.
	if c.Event == livetest.TrialLifecycle {
		environment[serviceReadinessURLEnv] = "http://127.0.0.1:8080/health"
		environment[serviceFixtureEnv] = ""
	}

	return settings, environment, err
}
