// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

func distroPackagesConfigPlan(t *testing.T, declared *config.DistroPackages) pipeline.ConfigPlan {
	t.Helper()

	cfg := &config.Config{
		Artifacts:      []config.Artifact{{Name: "cli", ProjectType: projecttype.Go}},
		DistroPackages: declared,
	}
	require.NoError(t, config.Derive(cfg))

	return pipeline.NewConfigPlan(cfg)
}

func forgePackages() *config.DistroPackages {
	return &config.DistroPackages{}
}

// The plan carries every place resolved, and the policy releases' stage
// plans, Forgejo's among them, admit the block.
func TestConfigPlanResolvesDistroPackagesAndPolicyReleaseAdmitsThem(t *testing.T) {
	t.Parallel()

	plan := distroPackagesConfigPlan(t, forgePackages())

	require.Equal(t, "stable", plan.DistroPackages.Debian.Distribution)
	require.Equal(t, "main", plan.DistroPackages.Debian.Component)
	require.Equal(t, "stable", plan.DistroPackages.RPM.Group)
	require.Equal(t, "stable", plan.DistroPackages.Alpine.Branch)
	require.Equal(t, "main", plan.DistroPackages.Alpine.Repository)

	_, _, err := pipeline.NewPolicyReleaseStagePlans(plan)
	require.NoError(t, err)

	require.Nil(t, distroPackagesConfigPlan(t, nil).DistroPackages)
}

func TestConfigPlanRefusesUnresolvedOrInvalidDistroPackages(t *testing.T) {
	t.Parallel()

	unresolved := distroPackagesConfigPlan(t, forgePackages())
	unresolved.DistroPackages = forgePackages()
	require.ErrorIs(t, pipeline.ValidateConfigPlan(unresolved), errs.ErrInvalidConfig)

	invalid := distroPackagesConfigPlan(t, forgePackages())
	invalid.DistroPackages.RPM.Group = "../other"
	require.ErrorIs(t, pipeline.ValidateConfigPlan(invalid), errs.ErrInvalidConfig)
}

// The GitHub release plans from the declaration and has no registry for
// the packages: it refuses the block before anything is built.
func TestGitHubReleasePlanRefusesDistroPackages(t *testing.T) {
	t.Parallel()

	input := pipeline.ReleasePlanInput{ReleaseSBOMs: "none", RepositoryVisibility: "public"}

	input.ConfigPlan = distroPackagesConfigPlan(t, nil)
	_, err := pipeline.NewReleasePlan(input)
	require.NoError(t, err)

	input.ConfigPlan = distroPackagesConfigPlan(t, forgePackages())
	_, err = pipeline.NewReleasePlan(input)
	require.ErrorIs(t, err, errs.ErrUnsupported)
	require.ErrorContains(t, err, "the GitHub release cannot publish .deb, .rpm and .apk files to a registry")
}
