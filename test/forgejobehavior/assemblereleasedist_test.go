// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"slices"
	"testing"
)

// assembleFakeEngine records the provider the step selects ahead of the argv.
const assembleFakeEngine = `#!/usr/bin/env bash
printf '%s\0' "FORGE_CI_PROVIDER=${FORGE_CI_PROVIDER-<unset>}" "$@" >"${FAKE_ARGV_LOG:?}"
exit "${FAKE_ENGINE_STATUS:-0}"
`

// Both composite steps forward their inputs to the engine under the Forgejo
// provider and propagate its status. Dist assembly and upload semantics belong
// to the engine and its companion suite.
func TestAssembleReleaseDist(t *testing.T) {
	t.Parallel()
	action := loadAction(t, "assemble-release-dist")
	action.requireSteps(t, 2)
	assemble, upload := action.Runs.Steps[0].Run, action.Runs.Steps[1].Run

	t.Run("binds each step variable to its input", func(t *testing.T) {
		t.Parallel()

		action.requireStepEnv(t, 0, map[string]string{
			"ARTIFACT_NAMES":              "${{ inputs['artifact-names'] }}",
			"ARTIFACT_TRANSFER_PLAN_JSON": "${{ inputs['artifact-transfer-plan-json'] }}",
			"DIST_DIR":                    "${{ inputs.path }}",
			"LEDGER_FILES":                "${{ inputs['ledger-files'] }}",
			"LEDGER_EXPECTED_COUNT":       "${{ inputs['ledger-expected-count'] }}",
			"RELEASE_IMAGES_PATH":         "${{ inputs['release-images-path'] }}",
			"PRUNE_DIRS":                  "${{ inputs['prune-dirs'] }}",
		})
		action.requireStepEnv(t, 1, map[string]string{
			"ASSEMBLE_UPLOAD_ARTIFACT_NAME":  "${{ inputs.artifact-name }}",
			"ASSEMBLE_UPLOAD_PATH":           "${{ inputs.path }}",
			"ASSEMBLE_UPLOAD_RETENTION_DAYS": "${{ inputs.retention-days }}",
		})
	})

	common := []string{
		"ARTIFACT_TRANSFER_PLAN_JSON=plan-v", "ARTIFACT_NAMES=names-v", "DIST_DIR=dist-v/",
		"LEDGER_FILES=ledgers-v", "LEDGER_EXPECTED_COUNT=3", "PRUNE_DIRS=prune-v",
	}
	assembleArgv := func(imagesPath string) []string {
		return []string{
			"FORGE_CI_PROVIDER=forgejo", "release", "assemble-dist",
			"--artifact-transfer-plan-json", "plan-v", "--artifact-names", "names-v", "--path", "dist-v/",
			"--ledger-files", "ledgers-v", "--ledger-expected-count", "3",
			"--release-images-path", imagesPath, "--prune-dirs", "prune-v",
		}
	}
	uploadArgv := []string{
		"FORGE_CI_PROVIDER=forgejo", "artifact", "upload", "--name", "artifact-v", "--dir", "dist-v",
		"--retention-days", "7", "--if-no-files", "error", "--include-hidden",
	}
	uploadEnv := []string{"ASSEMBLE_UPLOAD_ARTIFACT_NAME=artifact-v", "ASSEMBLE_UPLOAD_PATH=dist-v", "ASSEMBLE_UPLOAD_RETENTION_DAYS=7"}

	for _, test := range []struct {
		name, script string
		env          []string
		want         int
		argv         []string
	}{
		{
			"defaults the images path under the dist directory without a doubled slash", assemble,
			append(slices.Clone(common), "RELEASE_IMAGES_PATH=", "FAKE_ENGINE_STATUS=0"), 0, assembleArgv("dist-v/release-images.json"),
		},
		{
			"forwards an explicit images path", assemble,
			append(slices.Clone(common), "RELEASE_IMAGES_PATH=custom/images.json", "FAKE_ENGINE_STATUS=0"), 0, assembleArgv("custom/images.json"),
		},
		{
			"propagates the assembly status", assemble,
			append(slices.Clone(common), "RELEASE_IMAGES_PATH=", "FAKE_ENGINE_STATUS=9"), 9, assembleArgv("dist-v/release-images.json"),
		},
		{"forwards the upload inputs and fixed flags", upload, append(slices.Clone(uploadEnv), "FAKE_ENGINE_STATUS=0"), 0, uploadArgv},
		{"propagates the upload status", upload, append(slices.Clone(uploadEnv), "FAKE_ENGINE_STATUS=4"), 4, uploadArgv},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			s := newSandbox(t, "assemble-release-dist")
			s.write(".forgejo/bin/forge-ci", assembleFakeEngine, 0o700)

			got := s.run(test.script, test.env...)
			if got.status != test.want || !slices.Equal(s.argv(), test.argv) {
				t.Fatalf("status %d argv %q; stderr %s", got.status, s.argv(), got.stderr)
			}
		})
	}
}
