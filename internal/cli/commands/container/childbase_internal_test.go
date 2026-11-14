// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/stretchr/testify/require"
)

func TestChildBaseVerbsRefuseUnadmittedParentBeforeTools(t *testing.T) {
	for _, defect := range []string{"missing projection", "malformed projection", "unknown projection field", "mutable ref", "same id", "wrong expected ref", "missing lineage", "missing registry inputs", "mixed", "warm", "moving tag", "no prepared inputs", "kaniko"} {
		t.Run(defect, func(t *testing.T) {
			env := testenv.New(t)
			t.Chdir(env.Temp)

			id := strings.Repeat("a", 64)
			parent := appbaseimages.BaseImageParent{Ref: "parent.example/shared/base@sha256:" + strings.Repeat("b", 64), InputID: strings.Repeat("c", 64)}
			lineage, err := json.Marshal(map[string]any{"parent_base": parent})
			require.NoError(t, err)

			plan := pipeline.ReleaseImagesPlan{
				Images: []pipeline.ReleaseImage{{Name: "child", Role: "base", ImageKind: "base", Flavor: "opaque", BaseInputID: id, Scan: true,
					ImageRepository: "registry.example/org/base", Archs: "amd64", FinalTagName: id + "-opaque", CandidateTag: "staging-" + id + "-opaque", ProvenanceJSON: string(lineage)}},
				Builds: []pipeline.ReleaseImageBuild{{Name: "child", Arch: "amd64", Platform: "linux/amd64", ContainerFile: "Containerfile", Context: ".",
					ImageRepository: "registry.example/org/base", FinalTagName: id + "-opaque", CandidateTag: "staging-" + id + "-opaque",
					BuildArgs: "PARENT=" + parent.Ref, RegistryInputs: map[string]string{"PARENT": parent.Ref}}},
			}
			projection, err := json.Marshal(parent)
			require.NoError(t, err)

			switch defect {
			case "missing projection":
				projection = nil
			case "malformed projection":
				projection = []byte(`not JSON`)
			case "unknown projection field":
				projection = []byte(`{"ref":"bad","input_id":"bad","unknown":true}`)
			case "mutable ref":
				projection = []byte(strings.ReplaceAll(string(projection), parent.Ref, "parent.example/shared/base:latest"))
			case "same id":
				projection = []byte(strings.ReplaceAll(string(projection), parent.InputID, id))
			case "wrong expected ref":
				projection = []byte(strings.ReplaceAll(string(projection), "shared/base", "wrong/base"))
			case "missing lineage":
				plan.Images[0].ProvenanceJSON = `{}`
			case "missing registry inputs":
				plan.Builds[0].RegistryInputs = nil
			case "mixed":
				plan.Images = append(plan.Images, plan.Images[0])
			case "warm":
				plan.Images[0].CacheTagPrefix = "cache-child"
			case "moving tag":
				plan.Images[0].MovingTagName = "latest"
			}

			body, err := json.Marshal(plan)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile("plan.json", body, 0o600))
			bins := mockbinary.New(t)
			bins.OnPath()

			for _, binary := range []string{"buildah", "syft", "trivy", "cosign"} {
				bins.Add(binary, "exit 99")
			}

			for _, verb := range []string{"plan", "build", "publish"} {
				if defect == "no prepared inputs" && verb != "build" {
					continue
				}
				// An otherwise admitted plan is refused for its builder at
				// plan time, before a parent is fetched.
				if defect == "kaniko" && verb != "plan" {
					continue
				}

				args := []string{"child-base", verb, "--plan", "plan.json", "--image-repository", "registry.example/org/base", "--release-tag", "v1.2.3", "--release-sha", strings.Repeat("d", 40)}
				if len(projection) != 0 {
					args = append(args, "--parent-base-json", string(projection))
				}

				switch verb {
				case "plan":
					args = append(args, "--output", "never-plan", "--runner-input", `build-runs-on-linux-amd64=["builder"]`)
				case "build":
					args = append(args, "--arch", "amd64", "--metadata-dir", "never-layouts", "--created", "2026-01-02T03:04:05Z")
				case "publish":
					args = append(args, "--layouts-dir", "missing-layouts", "--output-dir", "never-payload", "--auth-file", "missing-auth")
				}

				if defect == "kaniko" {
					require.ErrorContains(t, childBaseGroup().Run(t.Context(), append(args, "--image-builder", "kaniko")), "builds with Buildah")

					continue
				}

				require.Error(t, childBaseGroup().Run(t.Context(), args), verb)
			}

			for _, binary := range []string{"buildah", "syft", "trivy", "cosign"} {
				require.Empty(t, bins.Invocations(binary))
			}

			require.NoFileExists(t, "never-plan")
			require.NoDirExists(t, "never-layouts")
			require.NoDirExists(t, "never-payload")
		})
	}
}
