// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The base-images example's planner is what an adopter copies, so the plans
// it prints are held to the admission its release runs them through: a root,
// and a flavor or its siblings on the parent they were given and on no other.
func TestBaseImagesExamplePlansAreAdmitted(t *testing.T) {
	t.Parallel()

	const repository = "registry.example/owner/app-base"

	parent := appbaseimages.BaseImageParent{
		Ref:     repository + "@sha256:" + strings.Repeat("a", 64),
		InputID: strings.Repeat("b", 64),
	}

	plan := func(args ...string) pipeline.ReleaseImagesPlan {
		t.Helper()

		command := exec.CommandContext(t.Context(), "bash", append([]string{"base-plan.sh"}, args...)...)
		command.Dir = filepath.Join(reporoot.Path(t), "examples", "base-images")

		body, err := command.Output()
		require.NoError(t, err)

		admitted, err := pipeline.ReadImageBuildPlan(string(body), repository, "")
		require.NoError(t, err)

		return admitted
	}

	root := plan("base", repository)
	require.NoError(t, appbaseimages.ValidateRootBasePlan(root, repository))

	child := plan("tools", repository, parent.Ref, parent.InputID)
	require.NoError(t, appbaseimages.ValidateChildBasePlan(child, repository, parent))
	require.NotEqual(t, root.Images[0].BaseInputID, child.Images[0].BaseInputID)

	// The flavor's identity follows its parent's.
	moved := plan("tools", repository, parent.Ref, strings.Repeat("c", 64))
	require.NotEqual(t, child.Images[0].BaseInputID, moved.Images[0].BaseInputID)

	// Siblings are one stage: each on the one parent, with an identity of its own.
	siblings := plan("tools,debug", repository, parent.Ref, parent.InputID)
	require.NoError(t, appbaseimages.ValidateChildBasePlan(siblings, repository, parent))
	require.Len(t, siblings.Images, 2)
	require.Equal(t, child.Images[0], siblings.Images[0])
	require.NotEqual(t, siblings.Images[0].BaseInputID, siblings.Images[1].BaseInputID)
	require.ErrorIs(t, appbaseimages.ValidateRootBasePlan(plan("base,tools", repository), repository), errs.ErrInvalidConfig)

	other := parent
	other.Ref = repository + "@sha256:" + strings.Repeat("d", 64)
	require.ErrorIs(t, appbaseimages.ValidateChildBasePlan(child, repository, other), errs.ErrInvalidConfig)
	require.ErrorIs(t, appbaseimages.ValidateRootBasePlan(child, repository), errs.ErrInvalidConfig)
}
