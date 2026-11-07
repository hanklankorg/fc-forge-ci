// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/stretchr/testify/require"
)

func TestRootBaseVerbsIndependentlyRefuseUnsupportedPlans(t *testing.T) {
	for _, defect := range []string{"parent", "parent lineage", "warm", "mixed", "prepared parent", "release", "partition"} {
		t.Run(defect, func(t *testing.T) {
			writeReleaseJobsArtifacts(t)
			require.NoError(t, releaseImagesPlanCmd().Run(t.Context(), releaseJobsPlanArgs("--output", "ordinary.json")))

			body, err := os.ReadFile("ordinary.json")
			require.NoError(t, err)

			var plan pipeline.ReleaseImagesPlan
			require.NoError(t, json.Unmarshal(body, &plan))
			image := &plan.Images[0]
			image.ImageKind, image.Role, image.Flavor, image.BaseInputID = "base", "base", "opaque", strings.Repeat("a", 64)
			image.FinalTagName = image.BaseInputID + "-opaque"

			image.CandidateTag = "staging-" + image.FinalTagName
			for i := range plan.Builds {
				plan.Builds[i].FinalTagName, plan.Builds[i].CandidateTag = image.FinalTagName, image.CandidateTag
			}

			switch defect {
			case "parent":
				image.BaseRef = "registry.example/group/parent@sha256:" + strings.Repeat("c", 64)
			case "parent lineage":
				image.ProvenanceJSON = `{"parent_base":null}`
			case "warm":
				image.CacheTagPrefix = "cache-root"
			case "mixed":
				plan.Images = append(plan.Images, plan.Images[0])
			case "prepared parent":
				plan.Builds[0].RegistryInputs = map[string]string{"PARENT": "registry.example/group/parent@sha256:" + strings.Repeat("c", 64)}
			case "release":
				image.ImageKind = "release"

				image.FinalTagName, image.CandidateTag = "v1.2.3", "staging-v1.2.3"
				for i := range plan.Builds {
					plan.Builds[i].FinalTagName, plan.Builds[i].CandidateTag = image.FinalTagName, image.CandidateTag
				}
			case "partition":
				plan.Builds = plan.Builds[:1]
			}

			body, err = json.Marshal(plan)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile("planted.json", body, 0o600))
			bins := mockbinary.New(t)
			bins.OnPath()

			for _, tool := range []string{"buildah", "trivy", "syft", "cosign"} {
				bins.Add(tool, "exit 99")
			}

			for _, verb := range []string{"plan", "build", "publish"} {
				args := []string{"root-base", verb, "--plan", "planted.json", "--image-repository", "registry.example/group/app", "--release-tag", "v1.2.3", "--release-sha", strings.Repeat("b", 40)}
				switch verb {
				case "plan":
					args = append(args, "--output", "never-plan.json", "--runner-archs", "amd64,arm64")
				case "build":
					args = append(args, "--arch", "amd64", "--metadata-dir", "never-layouts", "--created", baseStartedOnForRoot)
				case "publish":
					args = append(args, "--layouts-dir", "missing-layouts", "--output-dir", "never-payload", "--auth-file", "missing-auth")
				}

				require.ErrorIs(t, rootBaseGroup().Run(t.Context(), args), errs.ErrInvalidConfig, verb)
			}

			for _, tool := range []string{"buildah", "trivy", "syft", "cosign"} {
				require.Empty(t, bins.Invocations(tool))
			}

			require.NoFileExists(t, "never-plan.json")
			require.NoDirExists(t, "never-layouts")
			require.NoDirExists(t, "never-payload")
		})
	}
}

const baseStartedOnForRoot = "2026-01-02T03:04:05Z"
