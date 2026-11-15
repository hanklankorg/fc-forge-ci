// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// recordReleaseImageInputs are the step's input variables. The runner defines
// every one, empty when the caller omits it, so each case starts from all of
// them defined and empty.
func recordReleaseImageInputs() []string {
	return []string{
		"INPUT_ROLE", "INPUT_IMAGE", "INPUT_DIGEST", "INPUT_TAG", "INPUT_RELEASE_TAG", "INPUT_SBOM",
		"INPUT_CANDIDATE_TAG", "INPUT_CANDIDATE", "INPUT_MOVING_TAG", "INPUT_FLAVOR", "INPUT_BASE_REF",
		"INPUT_BASE_INPUT_ID", "INPUT_LEDGER", "INPUT_PLATFORM",
	}
}

// The record step forwards every input to its own engine flag (distinct values
// catch a swapped pair), pins the SBOM file it records, defaults it to the
// engine's single-platform image SBOM name, turns the candidate switch into
// LEDGER_DERIVE_CANDIDATE_TAG, and refuses an empty required input or a
// missing SBOM before the engine starts. Ledger semantics are covered by the external black-box
// suite.
func TestRecordReleaseImage(t *testing.T) {
	t.Parallel()

	const sbomBody = `{"bomFormat":"CycloneDX"}`

	const defaultSBOM = "dist/role-v-1.2.3-abcdef0-analyzed-container-linux-amd64-sbom.cyclonedx.json"

	sbomPin := fmt.Sprintf("%x", sha256.Sum256([]byte(sbomBody)))
	script := actionStep(t, "record-release-image", "") // the only step has no id
	required := []string{"INPUT_ROLE=role-v", "INPUT_IMAGE=image-v", "INPUT_DIGEST=digest-v", "INPUT_TAG=tag-v"}
	run := func(t *testing.T, inputs ...string) (*sandbox, result) {
		t.Helper()
		box := newSandbox(t, "record-release-image")
		box.write("sbom-v", sbomBody, 0o600)
		box.write(defaultSBOM, sbomBody, 0o600)
		box.write("bin/git", "#!/bin/sh\nset -eu\ntest \"$*\" = 'rev-parse HEAD'\necho abcdef0123456789abcdef0123456789abcdef01\n", 0o700)

		env := append(installRecordingFakes(box), "PATH="+filepath.Join(box.root, "bin")+":"+os.Getenv("PATH"))
		for _, name := range recordReleaseImageInputs() {
			env = append(env, name+"=")
		}

		return box, box.run(script, append(env, inputs...)...)
	}

	for _, test := range []struct {
		name   string
		inputs []string
		derive string
		argv   []string
	}{
		{
			"forwards every input to its own flag",
			append(slices.Clone(required), "INPUT_RELEASE_TAG=release-tag-v", "INPUT_SBOM=sbom-v",
				"INPUT_CANDIDATE_TAG=candidate-tag-v", "INPUT_MOVING_TAG=moving-tag-v", "INPUT_FLAVOR=flavor-v",
				"INPUT_BASE_REF=base-ref-v", "INPUT_BASE_INPUT_ID=base-input-id-v", "INPUT_LEDGER=ledger-v",
				"INPUT_CANDIDATE=true"),
			"true",
			[]string{"container", "ledger", "add", "--ledger", "ledger-v", "--role", "role-v", "--image-name", "image-v",
				"--digest", "digest-v", "--final-tag-name", "tag-v", "--sbom", "sbom-v", "--sbom-sha256", sbomPin,
				"--tag", "release-tag-v", "--flavor", "flavor-v", "--candidate-tag-name", "candidate-tag-v",
				"--moving-tag-name", "moving-tag-v", "--base-ref", "base-ref-v", "--base-input-id", "base-input-id-v"},
		},
		{
			"keeps the default ledger and SBOM name, a false switch and empty optional values",
			append(slices.Clone(required), "INPUT_RELEASE_TAG=v1.2.3"),
			"false",
			[]string{"container", "ledger", "add", "--ledger", "dist/release-images.json", "--role", "role-v",
				"--image-name", "image-v", "--digest", "digest-v", "--final-tag-name", "tag-v",
				"--sbom", defaultSBOM, "--sbom-sha256", sbomPin, "--tag", "v1.2.3", "--flavor", "", "--candidate-tag-name", "", "--moving-tag-name", "",
				"--base-ref", "", "--base-input-id", ""},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			s, got := run(t, test.inputs...)
			if got.status != 0 || !slices.Equal(s.argv(), test.argv) {
				t.Fatalf("status %d argv %q; stderr %s", got.status, s.argv(), got.stderr)
			}

			if derive, ok := recorded(s).env["LEDGER_DERIVE_CANDIDATE_TAG"]; !ok || derive != test.derive {
				t.Fatalf("LEDGER_DERIVE_CANDIDATE_TAG = %q (set %t), want %q", derive, ok, test.derive)
			}
		})
	}

	t.Run("refuses a missing SBOM before the engine", func(t *testing.T) {
		t.Parallel()

		s, got := run(t, append(slices.Clone(required), "INPUT_SBOM=dist/absent.cyclonedx.json")...)
		if got.status == 0 || s.argv() != nil || !strings.Contains(got.stderr, "no SBOM at dist/absent.cyclonedx.json") {
			t.Fatalf("status %d argv %q stderr %q", got.status, s.argv(), got.stderr)
		}
	})

	t.Run("refuses the default SBOM name without a release tag", func(t *testing.T) {
		t.Parallel()

		s, got := run(t, required...)
		if got.status == 0 || s.argv() != nil || !strings.Contains(got.stderr, "release-tag is required") {
			t.Fatalf("status %d argv %q stderr %q", got.status, s.argv(), got.stderr)
		}
	})

	for _, missing := range required {
		name, _, _ := strings.Cut(missing, "=")
		t.Run("refuses an empty "+name+" before the engine", func(t *testing.T) {
			t.Parallel()

			s, got := run(t, slices.DeleteFunc(slices.Clone(required), func(entry string) bool { return entry == missing })...)
			if got.status == 0 || s.argv() != nil || !strings.Contains(got.stderr, name) {
				t.Fatalf("status %d argv %q stderr %q", got.status, s.argv(), got.stderr)
			}
		})
	}
}
