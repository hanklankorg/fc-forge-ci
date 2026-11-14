// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitadapter "github.com/diggsweden/forge-ci/internal/adapters/git"
	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/stretchr/testify/require"
)

func TestPreparedBumpPlanUsesOnlyVerifiedPatchesAndEngineTextWriters(t *testing.T) {
	for _, failure := range []string{"", "digest", "unplanned", "duplicate", "linked-changelog", "directory-changelog"} {
		t.Run("case-"+failure, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)

			for _, dir := range []string{"one", "two", "gradle"} {
				require.NoError(t, os.Mkdir(dir, 0o700))
			}

			first := strings.ReplaceAll(applyBumpPOM, "%s", "0.1.9")
			second := strings.ReplaceAll(applyBumpPOM, "%s", "0.1.8")

			writeFile(t, root, "one/pom.xml", first)
			writeFile(t, root, "two/pom.xml", second)
			writeFile(t, root, "gradle/gradle.properties", "version=0.1.0\n")
			writeFile(t, root, "full.md", "# Reviewed changelog\n")

			if failure == "linked-changelog" {
				writeFile(t, root, "one/target.md", "unchanged link target\n")
				require.NoError(t, os.Symlink("target.md", filepath.Join(root, "one/CHANGELOG.md")))
			}

			if failure == "directory-changelog" {
				require.NoError(t, os.Mkdir(filepath.Join(root, "one/CHANGELOG.md"), 0o700))
			}

			completePatch := strings.Replace(applyBumpPatch, "@@ -3,4 +3,4 @@", "@@ -3,5 +3,5 @@", 1) + " </project>\n"

			patch := strings.ReplaceAll(completePatch, "pom.xml", "one/pom.xml") +
				strings.ReplaceAll(strings.ReplaceAll(completePatch, "pom.xml", "two/pom.xml"), "0.1.9", "0.1.8")
			if failure == "unplanned" {
				patch = strings.ReplaceAll(patch, "two/pom.xml", "outside/pom.xml")
			}
			// Two version jobs' patches joined, both changing one file.
			if failure == "duplicate" {
				patch += strings.ReplaceAll(completePatch, "pom.xml", "one/pom.xml")
			}

			writeFile(t, root, "native.patch", patch)
			sum := sha256.Sum256([]byte(patch))
			plan := pipeline.ReleasePrepareStagePlan{Version: pipeline.ReleasePlanVersion, Stage: "prepare",
				Targets: pipeline.ReleasePrepareTargets{VersionBump: pipeline.TargetPlan[pipeline.PlannedArtifact]{Runs: true, Items: []pipeline.PlannedArtifact{
					{Name: "one", ProjectType: projecttype.Maven, WorkingDirectory: "one"},
					{Name: "two", ProjectType: projecttype.Maven, WorkingDirectory: "two"},
					{Name: "gradle", ProjectType: projecttype.Gradle, WorkingDirectory: "gradle"},
				}}}}
			body, err := json.Marshal(plan)
			require.NoError(t, err)

			in := appversion.BumpPlanInput{PlanJSON: string(body), Version: "0.2.0", ChangelogFile: "full.md", PatchPath: "native.patch", PatchSHA256: hex.EncodeToString(sum[:]),
				PathsFile: filepath.Join(root, "bump-paths")}
			if failure == "digest" {
				in.PatchSHA256 = strings.Repeat("0", 64)
			}
			// No Maven/npm/Cargo adapters exist in the signing-side call.
			err = appversion.BumpPlan(t.Context(), appversion.BumpOps{Git: gitadapter.New()}, fakeoutputsink.New(t), io.Discard, io.Discard, output.Annotator{}, in)

			if failure != "" {
				if failure == "linked-changelog" {
					target, readErr := os.ReadFile(filepath.Join(root, "one/target.md"))
					require.NoError(t, readErr)

					if string(target) != "unchanged link target\n" {
						t.Errorf("preflight failure changed the link target: %q", target)
					}

					link, statErr := os.Lstat(filepath.Join(root, "one/CHANGELOG.md"))
					require.NoError(t, statErr)
					require.NotZero(t, link.Mode()&os.ModeSymlink)
				}

				require.Error(t, err)

				if failure == "duplicate" {
					require.ErrorContains(t, err, "one/pom.xml twice")
				}

				if failure == "linked-changelog" || failure == "directory-changelog" || failure == "duplicate" {
					require.ErrorIs(t, err, errs.ErrValidation)
				}

				unchanged, readErr := os.ReadFile(filepath.Join(root, "one/pom.xml"))
				require.NoError(t, readErr)
				require.Equal(t, first, string(unchanged))

				return
			}

			require.NoError(t, err)

			for _, file := range []string{"one/pom.xml", "two/pom.xml", "gradle/gradle.properties"} {
				contents, readErr := os.ReadFile(file)
				require.NoError(t, readErr)
				require.Contains(t, string(contents), "0.2.0")
			}

			// Exactly what the bump wrote, for the release commit: no stray
			// file of the checkout, such as the commit message.
			paths, err := os.ReadFile(filepath.Join(root, "bump-paths"))
			require.NoError(t, err)
			require.Equal(t, "one/pom.xml\ntwo/pom.xml\none/CHANGELOG.md\ntwo/CHANGELOG.md\ngradle/CHANGELOG.md\ngradle/gradle.properties\n", string(paths))
		})
	}
}
