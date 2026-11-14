// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"slices"

	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// PreparedLane is one purpose's run on a forge, as the testing matrices list
// it: the cases of its lane in the forge's table (PreparedCases), and the
// live tests it runs beside them, by name. What a lane runs with is the
// caller's environment: FORGE_CI_LIVE_CONSUMER_VISIBILITY and
// FORGE_CI_LIVE_ADMIN_<CHECK>; the builder is each case's own. A private
// lane fixes them for its cases (LaneEnvironment), and a lane for its
// scenarios (PreparedScenarioEnvironment).
type PreparedLane struct {
	// Name is <purpose>-<forge>; Forge is forgejo or gitlab.
	Name, Forge string
	// Lane is the lane of the forge's table whose cases it runs, or none.
	Lane string
	// Scenarios are the live tests the lane runs beside its cases.
	Scenarios []string
}

// PreparedLanes are the lanes, by name. `just test-live-prepared <dir>
// <forge> <pattern>` runs a lane's cases and scenarios from a prepared
// directory. A scenario of harness-inputs-gitlab, recovery-forgejo or
// trust-refusal-forgejo, and the rerun of forgejo17-forgejo, takes its
// consumer from there too
// (PreparedScenarioCase), and build-capability-gitlab the Buildah
// job image. interruption-manual-gitlab takes the operator's hand and runs
// through `just test-live-gitlab-interrupted-job`.
var PreparedLanes = []PreparedLane{
	{Name: "base-graph-forgejo", Forge: forgeForgejo, Lane: LaneBaseGraph},
	{Name: "base-graph-gitlab", Forge: forgeGitLab, Lane: LaneBaseGraph},
	{Name: "distro-packages-forgejo", Forge: forgeForgejo, Lane: LaneDistroPackages},
	{Name: "gpg-files-forgejo", Forge: forgeForgejo, Lane: LaneGPGFiles},
	{Name: "gpg-files-gitlab", Forge: forgeGitLab, Lane: LaneGPGFiles},
	{Name: laneBuildCapabilityGitLab, Forge: forgeGitLab, Scenarios: []string{"TestGitLabRelease_BuildImageCapability"}},
	{Name: "harness-inputs-gitlab", Forge: forgeGitLab, Scenarios: []string{
		gitlabBranchProtectionTest, gitlabPreseededTest, gitlabRecoveryTest,
	}},
	{Name: laneInterruptionManualGitLab, Forge: forgeGitLab, Scenarios: []string{"TestGitLabWorkflow_InterruptedJob"}},
	{Name: "k3s-private-android-gitlab", Forge: forgeGitLab, Lane: LanePrivateAndroid},
	{Name: "k3s-private-forgejo", Forge: forgeForgejo, Lane: LanePrivate},
	// The minimal signed release and the narrower policy scenarios run in
	// the go runtime image, beside the demos' gates and releases.
	{Name: laneK3sPrivateGitLab, Forge: forgeGitLab, Lane: LanePrivate, Scenarios: []string{
		"TestGitLabRelease_MinimalSignedArtifacts",
		"TestGitLabCheck_ChecksOutPrivateConsumerWithJobToken",
		"TestGitLabCheck_ChecksOutTheConsumerPipelineCommit",
		"TestGitLabPolicy_ConsumerCannotReplaceTheSigningPipeline",
		"TestGitLabPolicy_JobTokenCannotWriteThePublicationRegistry",
		"TestGitLabRelease_MinimalBranchProtection",
		"TestGitLabRelease_ResourceGroupSpansTheChild",
		"TestGitLabRelease_PromotionRetryAfterRegistryWrite",
		"TestGitLabBootstrap_PublishesAndDownloadsTheRecordedBinary",
	}},
	{Name: "lifecycle-forgejo", Forge: forgeForgejo, Lane: LaneLifecycle},
	{Name: "lifecycle-gitlab", Forge: forgeGitLab, Lane: LaneLifecycle},
	{Name: "release-variants-forgejo", Forge: forgeForgejo, Lane: LaneReleaseVariants},
	{Name: "release-variants-gitlab", Forge: forgeGitLab, Lane: LaneReleaseVariants},
	{Name: "snapshot-forgejo", Forge: forgeForgejo, Lane: LaneSnapshot},
	{Name: "snapshot-gitlab", Forge: forgeGitLab, Lane: LaneSnapshot},
	// The release scenarios that expect another end than a release.
	{Name: "recovery-forgejo", Forge: forgeForgejo, Scenarios: []string{"TestForgejoRelease_InterruptedRecovery"}},
	{Name: "trust-refusal-forgejo", Forge: forgeForgejo, Scenarios: []string{"TestForgejoRelease_RejectsPreseededProvenance"}},
	// The scenarios of Forgejo 17 or later (NeedsForgejo17Actions), which
	// skip on an older Forgejo: the native step summary, and the rerun of
	// an interrupted release in its own run.
	{Name: "forgejo17-forgejo", Forge: forgeForgejo, Scenarios: []string{forgejoNativeSummaryTest, forgejoRerunTest}},
}

const (
	gitlabBranchProtectionTest = "TestGitLabRelease_ConsumerBranchProtection"
	forgejoRerunTest           = "TestForgejoRelease_RerunAfterInterruption"
	// An in-runner probe, which runs the binary the launcher hands every
	// run and takes nothing from the prepared directory.
	forgejoNativeSummaryTest = "TestInRunner_NativeStepSummary"
)

// The lanes named in code: the first by what does not run it, the others
// because their scenarios take their images from the prepared directory
// (PreparedScenarioEnvironment).
const (
	// The lane `just test-live-prepared` does not run: it takes the
	// operator's hand, through `just test-live-gitlab-interrupted-job`.
	laneInterruptionManualGitLab = "interruption-manual-gitlab"

	laneBuildCapabilityGitLab = "build-capability-gitlab"
	laneK3sPrivateGitLab      = "k3s-private-gitlab"
)

// PreparedScenarioEnvironment is what a lane's scenario reads from its
// environment and prepared, the prepared directory's prepared.env, holds,
// with what the lane fixes for it, so a run cannot leave either out. A
// scenario of k3s-private-gitlab runs private with the reproducible-build
// check and kaniko, in the go runtime image, with the prepared signer and
// kaniko job image. The Buildah probe of build-capability-gitlab runs
// private with Buildah, on the prepared Buildah job image. A missing image
// is refused, naming its key. Another scenario takes nothing here: one of
// harness-inputs-gitlab, recovery-forgejo or trust-refusal-forgejo, and the
// rerun of forgejo17-forgejo, takes a case (PreparedScenarioCase), and the
// interruption and the native summary probe nothing at all.
func PreparedScenarioEnvironment(scenario string, prepared map[string]string) (map[string]string, error) {
	at := slices.IndexFunc(PreparedLanes, func(lane PreparedLane) bool { return slices.Contains(lane.Scenarios, scenario) })
	if at < 0 {
		return map[string]string{}, nil
	}

	var (
		environment map[string]string
		images      map[string]string
	)

	switch PreparedLanes[at].Name {
	case laneK3sPrivateGitLab:
		environment = map[string]string{
			consumerVisibilityEnv: visibilityPrivate, adminInputEnvironment(enforcement.ReproducibleBuilds): variableTrue,
			GitLabImageBuilderEnv: ImageBuilderKaniko,
		}
		images = map[string]string{
			gitLabJobArchiveEnv: gitLabJobArchiveEnv, signerImageEnv: signerImageEnv,
			gitLabSignerArchiveEnv: gitLabSignerArchiveEnv, KanikoJobImageArchiveEnv: KanikoJobImageArchiveEnv,
		}
	case laneBuildCapabilityGitLab:
		environment = map[string]string{consumerVisibilityEnv: visibilityPrivate, GitLabImageBuilderEnv: ImageBuilderBuildah}
		images = map[string]string{gitLabBuildArchiveEnv: GitLabSnapshotBuilderArchiveEnv(ImageBuilderBuildah)}
	}

	for name, key := range images {
		if prepared[key] == "" {
			return nil, fmt.Errorf("%s: %s: %s has no %s; prepare its image: %w", PreparedLanes[at].Name, scenario, PreparedEnvFile, key, errs.ErrValidation)
		}

		environment[name] = prepared[key]
	}

	return environment, nil
}

// preparedScenarioDemos are the demos the scenarios of harness-inputs-gitlab,
// recovery-forgejo, trust-refusal-forgejo and the rerun of
// forgejo17-forgejo run on, by scenario. The first push, the refused
// hand-offs and the Forgejo recovery and rerun need a native release build,
// the Go CLI's. The GitLab recovery needs one policy-owned release
// image in the project's own repository under the version's tag, the Maven
// service's.
var preparedScenarioDemos = map[string]string{
	gitlabBranchProtectionTest: "hello-go-cli",
	gitlabPreseededTest:        "hello-go-cli",
	gitlabRecoveryTest:         "hello-maven-service",

	"TestForgejoRelease_InterruptedRecovery":        "hello-go-cli",
	"TestForgejoRelease_RejectsPreseededProvenance": "hello-go-cli",
	forgejoRerunTest: "hello-go-cli",
}

// PreparedScenarioCase is the consumer a scenario takes from the prepared
// directory on forge: the private lane's release case of the scenario's
// demo in the forge's table, so the scenario runs with that case's settings,
// images and lane environment (LaneEnvironment). A scenario that takes none
// has none.
func PreparedScenarioCase(forge, scenario string) (PreparedCase, bool) {
	demo, takes := preparedScenarioDemos[scenario]
	if !takes {
		return PreparedCase{}, false
	}

	cases := PreparedCases(forge)

	at := slices.IndexFunc(cases, func(c PreparedCase) bool {
		return c.Lane == LanePrivate && c.Demo == demo && c.Event == TrialRelease
	})
	if at < 0 {
		return PreparedCase{}, false
	}

	return cases[at], true
}
