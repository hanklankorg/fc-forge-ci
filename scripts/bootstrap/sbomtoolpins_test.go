// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package bootstrap

import (
	"os"
	"regexp"
	"testing"
)

// A Forgejo build job installs the Build SBOM generator from these scripts;
// GitHub and GitLab jobs run it from the runtime image. One forge-ci release
// must write its SBOMs with one version of each generator wherever it runs.
func TestSBOMGeneratorPinsMatchTheRuntimeImage(t *testing.T) {
	t.Parallel()

	containerfile, err := os.ReadFile("../../containers/runtime/Containerfile")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ script, scriptVar, imageArg string }{
		{"install-cyclonedx-gomod.sh", "CYCLONEDX_GOMOD_VERSION", "CYCLONEDX_GOMOD_VERSION"},
		{"install-cargo-cyclonedx.sh", "CARGO_CYCLONEDX_VERSION", "CARGO_CYCLONEDX_VERSION"},
	} {
		script, err := os.ReadFile(tc.script)
		if err != nil {
			t.Fatal(err)
		}

		pinned := regexp.MustCompile(`(?m)^readonly ` + tc.scriptVar + `="([^"]+)"$`).FindSubmatch(script)
		image := regexp.MustCompile(`(?m)^ARG ` + tc.imageArg + `=(\S+)$`).FindSubmatch(containerfile)

		if pinned == nil || image == nil {
			t.Fatalf("%s: pin not found (script %q, image %q)", tc.script, pinned, image)
		}

		if string(pinned[1]) != string(image[1]) {
			t.Errorf("%s pins %s, the runtime image %s", tc.script, pinned[1], image[1])
		}
	}
}
